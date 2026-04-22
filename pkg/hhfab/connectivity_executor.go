// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"go.githedgehog.com/fabricator/pkg/hhfab/connmatrix"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/semaphore"
)

// MatrixExecutor runs ICMP reachability checks against a ConnectivityMatrix.
// Phase 1 supports ICMP only; port-forward-only paths are recorded in the
// matrix but skipped here (they require Phase 2's socat checks).
type MatrixExecutor struct {
	opts   TestConnectivityOpts
	sshs   map[string]*sshutil.Config
	matrix *connmatrix.ConnectivityMatrix
}

func NewMatrixExecutor(opts TestConnectivityOpts, sshs map[string]*sshutil.Config, matrix *connmatrix.ConnectivityMatrix) *MatrixExecutor {
	return &MatrixExecutor{opts: opts, sshs: sshs, matrix: matrix}
}

// Execute walks the matrix and issues one ping per (src, dst) endpoint pair.
// ALLOW expectations produce a positive ping; missing entries produce a
// negative ping (implicit DENY). Port-forward-only entries are skipped.
func (e *MatrixExecutor) Execute(ctx context.Context) error {
	if e.opts.PingsCount <= 0 {
		return nil
	}

	pings := semaphore.NewWeighted(e.opts.PingsParallel)
	wg := &sync.WaitGroup{}

	endpoints := e.matrix.Endpoints()
	pairs := []pairWork{}
	for i := range endpoints {
		for j := range endpoints {
			if i == j {
				continue
			}
			src, dst := endpoints[i], endpoints[j]
			// Skip pairs where both endpoints live on the same server: pinging
			// our own IP always succeeds regardless of fabric configuration
			// and would register as a false positive for implicit-DENY pairs.
			if src.Server == dst.Server {
				continue
			}
			exp := connmatrix.PickExpectation(e.matrix.Get(src.Key(), dst.Key()))
			pairs = append(pairs, pairWork{src: src, dst: dst, expectation: exp})
		}
	}

	// Size the error channel to the exact number of goroutines. A fixed
	// buffer (e.g. 1024) would deadlock once enough pairs error before the
	// drain loop runs — goroutines would block on send while wg.Wait
	// blocks on their wg.Done.
	errsCh := make(chan error, len(pairs))
	for _, pair := range pairs {
		wg.Add(1)
		go func(p pairWork) {
			defer wg.Done()
			if err := e.runPair(ctx, pings, p); err != nil {
				errsCh <- err
			}
		}(pair)
	}

	wg.Wait()
	close(errsCh)
	var joined error
	for err := range errsCh {
		joined = errors.Join(joined, err)
	}

	return joined
}

type pairWork struct {
	src         connmatrix.Endpoint
	dst         connmatrix.Endpoint
	expectation connmatrix.ConnectivityExpectation
}

func (e *MatrixExecutor) runPair(ctx context.Context, pings *semaphore.Weighted, p pairWork) error {
	if p.src.IP == (netip.Addr{}) || p.dst.IP == (netip.Addr{}) {
		slog.Debug("Skipping pair with unresolved endpoint", "src", p.src.Key(), "dst", p.dst.Key())

		return nil
	}
	// Port-forward destinations don't respond to ICMP on the pool IP — only
	// on the configured forwarded ports. Skip whenever port-forward entries
	// are present, regardless of whether DestinationIP happens to be set;
	// Phase 2's TCP check covers them.
	if p.expectation.Verdict == connmatrix.VerdictAllow && p.expectation.NAT != nil && len(p.expectation.NAT.PortForwards) > 0 {
		return nil
	}

	srcSSH, ok := e.sshs[p.src.Server]
	if !ok {
		return fmt.Errorf("missing ssh for %q", p.src.Server) //nolint:err113
	}

	targetIP := p.dst.IP
	if p.expectation.Verdict == connmatrix.VerdictAllow && p.expectation.NAT != nil && p.expectation.NAT.DestinationIP.IsValid() {
		targetIP = p.expectation.NAT.DestinationIP
	}

	expectedOK := p.expectation.Verdict == connmatrix.VerdictAllow
	slog.Debug("Executor ping",
		"from", p.src.Key(), "to", p.dst.Key(), "targetIP", targetIP.String(),
		"expected", expectedOK, "reason", p.expectation.Reason, "peering", p.expectation.Peering,
	)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.opts.PingsCount+30)*time.Second)
	defer cancel()

	// Pin the outgoing interface. For trunking, a server has multiple IPs
	// AND multiple sub-interfaces; binding only the source IP still lets
	// the kernel pick a different egress interface based on routing, which
	// would make the request leave via the wrong VPC (false positive for
	// implicit-DENY pairs, false negative for the intended peering path).
	// Binding the interface forces symmetric routing aligned with the
	// endpoint under test.
	bind := p.src.Interface
	if bind == "" {
		bind = p.src.IP.String()
	}
	// Capture concretely and early-return on success. Returning *PingError
	// directly through an `error` interface would produce a non-nil
	// interface wrapping a nil pointer — downstream type-switch on
	// *PingError would then miscount ping failures.
	pe := checkPing(ctx, e.opts.PingsCount, pings, p.src.Server, p.dst.Server, srcSSH, targetIP, nil, expectedOK, bind)
	if pe == nil {
		return nil
	}

	return pe
}
