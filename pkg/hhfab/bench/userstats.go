// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kapierrors "k8s.io/apimachinery/pkg/api/errors"
)

// opStat accumulates one operation kind, e.g. "update:connection".
//
// Latencies covers the current reporting window and is drained by each periodic
// line; All keeps every sample so the closing summary describes the run rather
// than whatever happened to arrive after the last report. Users generate
// hundreds of operations, not millions, so keeping both costs nothing.
type opStat struct {
	Count     int64
	Errors    int64
	Rejected  int64
	Timeouts  int64
	Latencies []time.Duration
	All       []time.Duration

	// Win counts operations in the current reporting window, and is reset with
	// Latencies. The periodic line has to describe one set of operations: a
	// cumulative count beside windowed percentiles reads as "8 calls took 0s"
	// when what happened is that none of the 8 finished in this window.
	Win int64
}

// userStats collects latency and errors only. The bench creates load and does
// not diagnose, so nothing here asserts that a result was correct.
type userStats struct {
	mu  sync.Mutex
	ops map[string]*opStat

	conflicts atomic.Int64

	lastErr   error
	lastErrOp string
}

// record files one completed operation.
//
// An error that only reflects the run ending is dropped: a request aborted
// because the operator stopped is not a fault, and counting it would make a
// clean run look like it failed at the last moment.
func (s *userStats) record(ctx context.Context, op string, took time.Duration, err error) {
	if err != nil && ctx.Err() != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stat, ok := s.ops[op]
	if !ok {
		stat = &opStat{}
		s.ops[op] = stat
	}

	stat.Count++
	stat.Win++

	switch {
	case err == nil:
		stat.Latencies = append(stat.Latencies, took)
		stat.All = append(stat.All, took)
	case isRejection(err):
		// A webhook saying no is a different signal from the API being
		// unreachable: it means validation still holds under load.
		stat.Rejected++
		s.lastErr, s.lastErrOp = err, op
	case isTimeout(err):
		stat.Timeouts++
		s.lastErr, s.lastErrOp = err, op
	default:
		stat.Errors++
		s.lastErr, s.lastErrOp = err, op
	}
}

func (s *userStats) conflict() {
	s.conflicts.Add(1)
}

// isRejection reports whether the apiserver refused the request on its merits
// rather than failing to serve it.
func isRejection(err error) bool {
	return kapierrors.IsInvalid(err) ||
		kapierrors.IsForbidden(err) ||
		kapierrors.IsBadRequest(err) ||
		kapierrors.IsNotAcceptable(err) ||
		kapierrors.IsUnsupportedMediaType(err)
}

func isTimeout(err error) bool {
	return kapierrors.IsTimeout(err) ||
		kapierrors.IsServerTimeout(err) ||
		strings.Contains(err.Error(), context.DeadlineExceeded.Error())
}

// userSnapshot is a consistent read of the stats. It is a struct rather than
// several return values so the error does not have to come last.
type userSnapshot struct {
	ops     map[string]opStat
	lastErr error
	lastOp  string
}

// snapshot copies the current totals, draining latencies so each report covers
// its own window rather than the whole run.
func (s *userStats) snapshot(drain bool) userSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]opStat, len(s.ops))

	for name, stat := range s.ops {
		out[name] = *stat

		if drain {
			stat.Latencies = nil
			stat.Win = 0
		}
	}

	return userSnapshot{ops: out, lastErr: s.lastErr, lastOp: s.lastErrOp}
}

// report prints an aggregate line every 30s, which is how a long run is watched
// without per-operation noise.
func (s *userStats) report(ctx context.Context, stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			snap := s.snapshot(true)
			if len(snap.ops) == 0 {
				continue
			}

			args := opTotals(snap.ops)

			if conflicts := s.conflicts.Load(); conflicts > 0 {
				args = append(args, "conflicts", conflicts)
			}

			if snap.lastErr != nil {
				args = append(args, "lastErr", snap.lastOp+": "+snap.lastErr.Error())
			}

			slog.Info("Users", args...)
		}
	}
}

// opTotals renders the per-operation counts and percentiles in a stable order,
// so consecutive lines line up when read in a terminal.
func opTotals(ops map[string]opStat) []any {
	names := make([]string, 0, len(ops))
	for name := range ops {
		names = append(names, name)
	}

	sort.Strings(names)

	var count, errs, rejected, timeouts int64

	args := make([]any, 0, len(names)*2)

	for _, name := range names {
		stat := ops[name]
		count += stat.Count
		errs += stat.Errors
		rejected += stat.Rejected
		timeouts += stat.Timeouts

		// Window count with window percentiles, so both describe the same
		// operations; the cumulative totals are in the head and the summary.
		if stat.Win == 0 {
			continue
		}

		p50, p95 := percentiles(stat.Latencies)
		args = append(args, name, fmt.Sprintf("%d p50=%s p95=%s",
			stat.Win, p50.Truncate(time.Millisecond), p95.Truncate(time.Millisecond)))
	}

	head := []any{"ops", count, "errors", errs}
	if rejected > 0 {
		head = append(head, "rejected", rejected)
	}

	if timeouts > 0 {
		head = append(head, "timeouts", timeouts)
	}

	return append(head, args...)
}

func (s *userStats) summary(took time.Duration) {
	ops := s.snapshot(false).ops

	names := make([]string, 0, len(ops))
	for name := range ops {
		names = append(names, name)
	}

	sort.Strings(names)

	var total, errs, rejected, timeouts int64

	for _, name := range names {
		stat := ops[name]
		total += stat.Count
		errs += stat.Errors
		rejected += stat.Rejected
		timeouts += stat.Timeouts
	}

	slog.Info("Users stopped",
		"ops", total, "errors", errs, "rejected", rejected, "timeouts", timeouts,
		"conflicts", s.conflicts.Load(), "took", took.Truncate(time.Second))

	// Per-operation detail last, where it can be read without scrolling past
	// the run.
	for _, name := range names {
		stat := ops[name]
		p50, p95 := percentiles(stat.All)

		slog.Info("  "+name,
			"count", stat.Count, "errors", stat.Errors, "rejected", stat.Rejected,
			"timeouts", stat.Timeouts,
			"p50", p50.Truncate(time.Millisecond), "p95", p95.Truncate(time.Millisecond))
	}
}
