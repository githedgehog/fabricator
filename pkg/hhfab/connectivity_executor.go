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

	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/semaphore"
)

// MatrixExecutor runs ICMP reachability checks against a ConnectivityMatrix.
// Phase 1 supports ICMP only; port-forward-only paths are recorded in the
// matrix but skipped here (they require Phase 2's socat checks).
type MatrixExecutor struct {
	opts   TestConnectivityOpts
	sshs   map[string]*sshutil.Config
	matrix *ConnectivityMatrix
}

func NewMatrixExecutor(opts TestConnectivityOpts, sshs map[string]*sshutil.Config, matrix *ConnectivityMatrix) *MatrixExecutor {
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
	errsCh := make(chan error, 1024)

	endpoints := e.matrix.Endpoints()
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
			pair := pairWork{src: src, dst: dst}
			exp := pickExpectation(e.matrix.Get(src.Key(), dst.Key()))
			pair.expectation = exp

			wg.Add(1)
			go func(p pairWork) {
				defer wg.Done()
				if err := e.runPair(ctx, pings, p); err != nil {
					errsCh <- err
				}
			}(pair)
		}
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
	src         Endpoint
	dst         Endpoint
	expectation ConnectivityExpectation
}

// pickExpectation selects the best single expectation from the set returned by
// Matrix.Get. If no proto-agnostic ALLOW entry exists, falls back to DENY so
// the executor runs a negative check.
func pickExpectation(entries []ConnectivityExpectation) ConnectivityExpectation {
	var firstAllow *ConnectivityExpectation
	for i := range entries {
		e := &entries[i]
		if e.ProtoPort != nil {
			continue
		}
		if e.Verdict == VerdictAllow && firstAllow == nil {
			firstAllow = e
		}
	}
	if firstAllow != nil {
		return *firstAllow
	}

	return ConnectivityExpectation{
		Verdict: VerdictDeny,
		Reason:  ReachabilityReasonImplicitDeny,
	}
}

func (e *MatrixExecutor) runPair(ctx context.Context, pings *semaphore.Weighted, p pairWork) error {
	if p.src.IP == (netip.Addr{}) || p.dst.IP == (netip.Addr{}) {
		slog.Debug("Skipping pair with unresolved endpoint", "src", p.src.Key(), "dst", p.dst.Key())

		return nil
	}
	// Port-forward-only destinations don't respond to ICMP on the pool IP;
	// Phase 2's TCP check covers them.
	if p.expectation.Verdict == VerdictAllow && p.expectation.NAT != nil && len(p.expectation.NAT.PortForwards) > 0 && !p.expectation.NAT.DestinationIP.IsValid() {
		return nil
	}

	srcSSH, ok := e.sshs[p.src.Server]
	if !ok {
		return fmt.Errorf("missing ssh for %q", p.src.Server) //nolint:err113
	}

	targetIP := p.dst.IP
	if p.expectation.Verdict == VerdictAllow && p.expectation.NAT != nil && p.expectation.NAT.DestinationIP.IsValid() {
		targetIP = p.expectation.NAT.DestinationIP
	}

	expectedOK := p.expectation.Verdict == VerdictAllow
	slog.Debug("Executor ping",
		"from", p.src.Key(), "to", p.dst.Key(), "targetIP", targetIP.String(),
		"expected", expectedOK, "reason", p.expectation.Reason, "peering", p.expectation.Peering,
	)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.opts.PingsCount+30)*time.Second)
	defer cancel()

	// Pin the source IP. For trunking, a server has multiple IPs and the
	// kernel's default route could pick a source that isn't part of the path
	// we're testing — producing false positives (ping succeeds via the
	// "wrong" VPC peering) or false negatives (the expected peering's source
	// subnet is not selected).
	srcIP := p.src.IP
	if pe := checkPing(ctx, e.opts.PingsCount, pings, p.src.Server, p.dst.Server, srcSSH, targetIP, &srcIP, expectedOK); pe != nil {
		return pe
	}

	return nil
}
