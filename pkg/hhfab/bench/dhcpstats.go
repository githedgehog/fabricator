// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// dhcpPhase is where a server is in its round, for the progress line.
type dhcpPhase int

const (
	phasePXE dhcpPhase = iota
	phaseDORA
	phaseRenew
	phaseRepeat
	dhcpPhases
)

var dhcpPhaseNames = [dhcpPhases]string{"pxe", "dora", "renewing", "repeating"}

var repeatKindNames = [...]string{"own", "taken"}

// dhcpRoundStats is what one round's servers are doing and have done.
type dhcpRoundStats struct {
	round  int
	rounds int
	total  int

	phases [dhcpPhases]atomic.Int64
	done   atomic.Int64

	bound      atomic.Int64
	pxeDead    atomic.Int64
	zeroOffers atomic.Int64
	zeroAcks   atomic.Int64
	nakDORA    atomic.Int64
	nakRenewal atomic.Int64
	sendErrors atomic.Int64

	mu         sync.Mutex
	start      time.Time
	doraLat    []time.Duration
	renewLat   []time.Duration
	pxeLat     []time.Duration
	winDORA    []time.Duration
	retransmit []int
	repeats    [2][repeatOutcomes]int64
	repeatLat  [2][]time.Duration
}

func newDHCPRoundStats(round, rounds, total int) *dhcpRoundStats {
	return &dhcpRoundStats{round: round, rounds: rounds, total: total}
}

func (s *dhcpRoundStats) begin(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.start = at
}

func (s *dhcpRoundStats) enter(p dhcpPhase) { s.phases[p].Add(1) }
func (s *dhcpRoundStats) leave(p dhcpPhase) { s.phases[p].Add(-1) }

// observe adds a latency to one of the stats' series.
func (s *dhcpRoundStats) observe(series *[]time.Duration, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	*series = append(*series, d)
	if series == &s.doraLat {
		s.winDORA = append(s.winDORA, d)
	}
}

func (s *dhcpRoundStats) repeat(kind repeatKind, outcome repeatOutcome, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.repeats[kind][outcome]++
	if outcome != outcomeNoReply {
		s.repeatLat[kind] = append(s.repeatLat[kind], d)
	}
}

// finish records a server leaving the round, however it ended.
func (s *dhcpRoundStats) finish(srv *dhcpServer) {
	s.done.Add(1)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.retransmit = append(s.retransmit, srv.retransmits)
}

// report prints a progress line every 30s, which is how a long round is
// watched without per-server noise.
func (s *dhcpRoundStats) report(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			p50, p95 := percentiles(s.winDORA)
			s.winDORA = nil
			retransmits := 0
			for _, n := range s.retransmit {
				retransmits += n
			}
			s.mu.Unlock()

			args := []any{
				"round", fmt.Sprintf("%d/%d", s.round+1, s.rounds),
				"bound", fmt.Sprintf("%d/%d", s.bound.Load(), s.total),
				"done", s.done.Load(),
			}
			for p := range dhcpPhases {
				args = append(args, dhcpPhaseNames[p], s.phases[p].Load())
			}
			args = append(args, "retransmitsDone", retransmits, "zeroOffer", s.zeroOffers.Load(), "zeroAck", s.zeroAcks.Load(),
				"nak", s.nakDORA.Load()+s.nakRenewal.Load(), "pxeDead", s.pxeDead.Load())
			if p50 > 0 {
				args = append(args, "doraP50", p50.Truncate(time.Millisecond), "doraP95", p95.Truncate(time.Millisecond))
			}

			slog.Info("DHCP", args...)
		}
	}
}

// dhcpRoundResult is a finished round, kept for the summary.
type dhcpRoundResult struct {
	round   int
	cleanup time.Duration
	storm   time.Duration

	total    int
	bound    int64
	pxeDead  int64
	timedOut int

	doraLat    []time.Duration
	renewLat   []time.Duration
	pxeLat     []time.Duration
	retransmit []int

	zeroOffer  int64
	zeroAck    int64
	nakDORA    int64
	nakRenewal int64
	late       int64
	dropped    int64
	garbled    int64
	sendErrors int64

	repeats   [2][repeatOutcomes]int64
	repeatLat [2][]time.Duration

	checks *dhcpChecks
}

// result closes the round: servers that neither bound nor died in PXE ran out
// of round, and are counted as unbound.
func (s *dhcpRoundStats) result(cleanup, storm time.Duration, checks *dhcpChecks, servers []*dhcpServer) *dhcpRoundResult {
	timedOut := 0
	for _, srv := range servers {
		if srv.leaseEnd.IsZero() && !srv.pxeDead {
			timedOut++
			checks.record(checkUnbound, "%s: no lease by the end of the round", srv.hostname)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return &dhcpRoundResult{
		round:      s.round,
		cleanup:    cleanup,
		storm:      storm,
		total:      s.total,
		bound:      s.bound.Load(),
		pxeDead:    s.pxeDead.Load(),
		timedOut:   timedOut,
		doraLat:    s.doraLat,
		renewLat:   s.renewLat,
		pxeLat:     s.pxeLat,
		retransmit: s.retransmit,
		zeroOffer:  s.zeroOffers.Load(),
		zeroAck:    s.zeroAcks.Load(),
		nakDORA:    s.nakDORA.Load(),
		nakRenewal: s.nakRenewal.Load(),
		sendErrors: s.sendErrors.Load(),
		repeats:    s.repeats,
		repeatLat:  s.repeatLat,
		checks:     checks,
	}
}

// log prints the round's line, its repeat outcomes and its misbehaviour.
func (r *dhcpRoundResult) log() {
	args := []any{
		"round", r.round + 1,
		"cleanup", r.cleanup.Truncate(time.Second),
		"storm", r.storm.Truncate(time.Second),
		"bound", fmt.Sprintf("%d/%d", r.bound, r.total),
		"pxeDead", r.pxeDead,
		"timedOut", r.timedOut,
	}
	args = append(args, latencyArgs("dora", r.doraLat)...)
	args = append(args, latencyArgs("renew", r.renewLat)...)
	args = append(args, latencyArgs("pxe", r.pxeLat)...)
	args = append(args, retransmitArgs(r.retransmit)...)
	args = append(args, "zeroOffer", r.zeroOffer, "zeroAck", r.zeroAck, "nakDORA", r.nakDORA, "nakRenewal", r.nakRenewal, "late", r.late)

	if r.dropped > 0 || r.garbled > 0 || r.sendErrors > 0 {
		args = append(args, "dropped", r.dropped, "garbled", r.garbled, "sendErrors", r.sendErrors)
	}

	slog.Info("Round done", args...)
	logRepeats(r.repeats, r.repeatLat)
	r.checks.log(fmt.Sprintf("round %d", r.round+1))
}

// latencyArgs is p50/p95/p99/max of a series, or nothing for an empty one.
func latencyArgs(name string, lat []time.Duration) []any {
	if len(lat) == 0 {
		return nil
	}

	sorted := slices.Clone(lat)
	slices.Sort(sorted)

	at := func(p int) time.Duration { return sorted[nearestRank(len(sorted), p)].Truncate(time.Millisecond) }

	return []any{
		name + "P50", at(50), name + "P95", at(95), name + "P99", at(99),
		name + "Max", sorted[len(sorted)-1].Truncate(time.Millisecond),
	}
}

func retransmitArgs(retransmit []int) []any {
	if len(retransmit) == 0 {
		return nil
	}

	sorted := slices.Clone(retransmit)
	slices.Sort(sorted)

	total := 0
	for _, n := range sorted {
		total += n
	}

	return []any{"retransmits", total, "retransmitsP50", sorted[nearestRank(len(sorted), 50)], "retransmitsMax", sorted[len(sorted)-1]}
}

// logRepeats prints each repeat kind's outcomes. For own repeats "ack" is
// right; for taken ones "nak" is, and "ack" is dhcpd granting another
// server's IP.
func logRepeats(repeats [2][repeatOutcomes]int64, lat [2][]time.Duration) {
	for kind := range repeats {
		args := []any{}
		sent := int64(0)

		for outcome := range repeatOutcomes {
			n := repeats[kind][outcome]
			sent += n
			args = append(args, outcome.String(), n)
		}

		if sent == 0 {
			continue
		}

		if p50, p95 := percentiles(slices.Clone(lat[kind])); p50 > 0 {
			args = append(args, "p50", p50.Truncate(time.Millisecond), "p95", p95.Truncate(time.Millisecond))
		}

		slog.Info("  repeats "+repeatKindNames[kind], args...)
	}
}

// dhcpSummary is the whole run.
type dhcpSummary struct {
	rounds []*dhcpRoundResult
	checks *dhcpChecks
}

func newDHCPSummary() *dhcpSummary {
	return &dhcpSummary{checks: newDHCPChecks()}
}

func (s *dhcpSummary) add(res *dhcpRoundResult) {
	s.rounds = append(s.rounds, res)
	s.checks.merge(res.checks)
}

// report prints the run: how long each round's storm and cleanup took, the
// figures over every round, who answered, and the misbehaviour block, which
// always ends with whether anything failed.
func (s *dhcpSummary) report(planned int, sources map[netip.Addr]int64) {
	storms := make([]string, 0, len(s.rounds))
	cleanups := make([]string, 0, len(s.rounds))

	var (
		dora, renew, pxe      []time.Duration
		retransmit            []int
		bound, pxeDead, total int64
		timedOut              int
		zeroOffer, zeroAck    int64
		nakDORA, nakRenewal   int64
		late                  int64
		repeats               [2][repeatOutcomes]int64
		repeatLat             [2][]time.Duration
	)

	for _, r := range s.rounds {
		storms = append(storms, fmt.Sprintf("%d=%s", r.round+1, r.storm.Truncate(time.Second)))
		cleanups = append(cleanups, fmt.Sprintf("%d=%s", r.round+1, r.cleanup.Truncate(time.Second)))
		dora = append(dora, r.doraLat...)
		renew = append(renew, r.renewLat...)
		pxe = append(pxe, r.pxeLat...)
		retransmit = append(retransmit, r.retransmit...)
		bound += r.bound
		pxeDead += r.pxeDead
		total += int64(r.total)
		timedOut += r.timedOut
		zeroOffer += r.zeroOffer
		zeroAck += r.zeroAck
		nakDORA += r.nakDORA
		nakRenewal += r.nakRenewal
		late += r.late

		for kind := range repeats {
			for outcome := range repeatOutcomes {
				repeats[kind][outcome] += r.repeats[kind][outcome]
			}
			repeatLat[kind] = append(repeatLat[kind], r.repeatLat[kind]...)
		}
	}

	msg := "DHCP storm finished"
	if len(s.rounds) < planned {
		msg = "DHCP storm interrupted"
	}

	slog.Info(msg, "rounds", fmt.Sprintf("%d/%d", len(s.rounds), planned),
		"bound", fmt.Sprintf("%d/%d", bound, total), "pxeDead", pxeDead, "timedOut", timedOut)

	if len(storms) > 0 {
		slog.Info("  storms took", "rounds", strings.Join(storms, " "))
		slog.Info("  cleanups took", "rounds", strings.Join(cleanups, " "))
	}

	args := append(latencyArgs("dora", dora), latencyArgs("renew", renew)...)
	args = append(args, latencyArgs("pxe", pxe)...)
	if len(args) > 0 {
		slog.Info("  latency", args...)
	}

	args = retransmitArgs(retransmit)
	args = append(args, "zeroOffer", zeroOffer, "zeroAck", zeroAck, "nakDORA", nakDORA, "nakRenewal", nakRenewal, "late", late)
	slog.Info("  replies", args...)

	logRepeats(repeats, repeatLat)

	if len(sources) > 0 {
		addrs := make([]netip.Addr, 0, len(sources))
		for addr := range sources {
			addrs = append(addrs, addr)
		}
		sort.Slice(addrs, func(i, j int) bool { return addrs[i].Less(addrs[j]) })

		parts := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			parts = append(parts, fmt.Sprintf("%s=%d", addr, sources[addr]))
		}

		slog.Info("  replies from", "sources", strings.Join(parts, " "))
	}

	slog.Info("Misbehaviour over the run")
	s.checks.log("run")
}
