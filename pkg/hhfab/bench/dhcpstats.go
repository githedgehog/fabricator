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

	// DORAs started over because no one answered the REQUEST.
	requestRestarts atomic.Int64

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

	// Only a usable answer has a latency: a repeat that got nothing, or only
	// 0.0.0.0, took as long as its retransmits, which says nothing of dhcpd.
	s.repeats[kind][outcome]++
	if outcome != outcomeNoReply && outcome != outcomeZero {
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
	restarts   int64
	late       int64
	dropped    int64
	garbled    int64
	sendErrors int64
	overflowed int64
	readErrors int64

	repeats   [2][repeatOutcomes]int64
	repeatLat [2][]time.Duration

	checks *dhcpChecks

	// The run was stopped during the storm: what it found stands, but it
	// didn't run its course.
	interrupted bool
}

// result closes the round: servers that neither bound nor died in PXE ran out
// of round, and are counted as unbound, unless the round was interrupted and
// they were stopped instead. Bound servers the round timeout stopped before
// they got through their repeats are flagged too: those probes never ran, so
// the round can't pass as clean, whether dhcpd was slow or the timeout short.
func (s *dhcpRoundStats) result(cleanup, storm time.Duration, checks *dhcpChecks, servers []*dhcpServer, interrupted bool) *dhcpRoundResult {
	timedOut := 0
	for _, srv := range servers {
		switch {
		case interrupted, srv.pxeDead, srv.finished:
		case srv.leaseEnd.IsZero():
			timedOut++
			checks.recordf(checkUnbound, "%s: no lease by the end of the round", srv.hostname)
		default:
			checks.recordf(checkCutOff, "%s: bound, but the round timed out before it got through its repeats", srv.hostname)
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
		restarts:   s.requestRestarts.Load(),
		sendErrors: s.sendErrors.Load(),
		repeats:    s.repeats,
		repeatLat:  s.repeatLat,
		checks:     checks,

		interrupted: interrupted,
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
	args = append(args, "zeroOffer", r.zeroOffer, "zeroAck", r.zeroAck, "nakDORA", r.nakDORA, "nakRenewal", r.nakRenewal,
		"requestRestarts", r.restarts, "late", r.late)

	lost := r.dropped > 0 || r.overflowed > 0 || r.readErrors > 0 || r.sendErrors > 0
	if lost || r.garbled > 0 {
		args = append(args, "dropped", r.dropped, "overflowed", r.overflowed, "readErrors", r.readErrors,
			"sendErrors", r.sendErrors, "garbled", r.garbled)
	}

	msg := "Round done"
	if r.interrupted {
		msg = "Round interrupted"
	}

	slog.Info(msg, args...)

	// Not dhcpd's doing, but it skews what is blamed on it.
	if lost {
		slog.Warn("  Packets were lost on the bench host, so retransmits and latencies overstate dhcpd's",
			"dropped", r.dropped, "overflowed", r.overflowed, "readErrors", r.readErrors, "sendErrors", r.sendErrors)
	}
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

// dhcpRunTotals is what the demux counted over the whole run. Replies keep
// coming between rounds, while the next one is set up, so these are taken
// from it rather than added up from the rounds; late ones then also include
// what the readiness probes got after they had their answer.
type dhcpRunTotals struct {
	late, dropped, garbled int64
	overflowed, readErrors int64
	sources                map[netip.Addr]int64
}

func (d *dhcpDemux) totals() dhcpRunTotals {
	return dhcpRunTotals{
		late: d.late.Load(), dropped: d.dropped.Load(), garbled: d.garbled.Load(),
		overflowed: d.overflowed.Load(), readErrors: d.readErrors.Load(),
		sources: d.replySources(),
	}
}

// report prints the run: how long each round's storm and cleanup took, the
// figures over every round, who answered, and the misbehaviour block, which
// always ends with whether anything failed; then all of it read back in plain
// words.
func (s *dhcpSummary) report(opts DHCPOpts, totals dhcpRunTotals) {
	planned := opts.Rounds

	storms := make([]string, 0, len(s.rounds))
	cleanups := make([]string, 0, len(s.rounds))

	var (
		dora, renew, pxe      []time.Duration
		retransmit            []int
		bound, pxeDead, total int64
		timedOut              int
		zeroOffer, zeroAck    int64
		nakDORA, nakRenewal   int64
		restarts              int64
		sendErrors            int64
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
		restarts += r.restarts
		sendErrors += r.sendErrors

		for kind := range repeats {
			for outcome := range repeatOutcomes {
				repeats[kind][outcome] += r.repeats[kind][outcome]
			}
			repeatLat[kind] = append(repeatLat[kind], r.repeatLat[kind]...)
		}
	}

	finished := 0
	for _, r := range s.rounds {
		if !r.interrupted {
			finished++
		}
	}

	msg := "DHCP storm finished"
	if finished < planned {
		msg = "DHCP storm interrupted"
	}

	slog.Info(msg, "rounds", fmt.Sprintf("%d/%d", finished, planned),
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
	args = append(args, "zeroOffer", zeroOffer, "zeroAck", zeroAck, "nakDORA", nakDORA, "nakRenewal", nakRenewal,
		"requestRestarts", restarts, "late", totals.late)
	slog.Info("  replies", args...)

	logRepeats(repeats, repeatLat)

	if len(totals.sources) > 0 {
		addrs := make([]netip.Addr, 0, len(totals.sources))
		for addr := range totals.sources {
			addrs = append(addrs, addr)
		}
		sort.Slice(addrs, func(i, j int) bool { return addrs[i].Less(addrs[j]) })

		parts := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			parts = append(parts, fmt.Sprintf("%s=%d", addr, totals.sources[addr]))
		}

		slog.Info("  replies from", "sources", strings.Join(parts, " "))
	}

	if lost := totals.dropped + totals.overflowed + totals.readErrors + sendErrors; lost > 0 || totals.garbled > 0 {
		args := []any{"dropped", totals.dropped, "overflowed", totals.overflowed, "readErrors", totals.readErrors,
			"sendErrors", sendErrors, "garbled", totals.garbled}
		if lost > 0 {
			slog.Warn("  packets lost on the bench host, so retransmits and latencies overstate dhcpd's", args...)
		} else {
			slog.Info("  packets", args...)
		}
	}

	slog.Info("Misbehaviour over the run")

	// Nothing seen is only worth saying of rounds that ran.
	if len(s.rounds) == 0 {
		slog.Warn("  Nothing was checked, no round ran")
	} else {
		s.checks.log("run")

		if finished < planned {
			slog.Warn(fmt.Sprintf("  Only %d of %d rounds ran their course", finished, planned))
		}
	}

	slog.Info("Reading the run")

	for _, line := range s.explain(dhcpRunFigures{
		finished: finished, planned: planned,
		dora: dora, renew: renew, retransmit: retransmit,
		bound: bound, total: total, pxeDead: pxeDead, timedOut: timedOut,
		restarts: restarts, sendErrors: sendErrors, repeats: repeats,
	}, totals, opts) {
		slog.Info("  " + line)
	}
}

// dhcpRunFigures is what report added up over the rounds, for explain.
type dhcpRunFigures struct {
	finished, planned int

	dora, renew []time.Duration
	retransmit  []int

	bound, total, pxeDead int64
	timedOut              int
	restarts, sendErrors  int64
	repeats               [2][repeatOutcomes]int64
}

// explain reads the run's figures back in plain words: what the bench did, how
// dhcpd coped with it, and whether anything dhcpd did was wrong.
func (s *dhcpSummary) explain(fig dhcpRunFigures, totals dhcpRunTotals, opts DHCPOpts) []string {
	if len(s.rounds) == 0 {
		return []string{"nothing to read: no round ran, see the error above"}
	}

	var out []string

	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	start := "all at once"
	if opts.Spread > 0 {
		start = fmt.Sprintf("spread over %s (%.0f/s)", opts.Spread, float64(opts.Layout.Servers)/opts.Spread.Seconds())
	}

	steps := "DORA, renewal at T1"
	if opts.PXE {
		steps = "PXE firmware DORA, OS DORA, renewal at T1"
	}

	add("load: %d servers per round, %s, dhcpd starting on fresh empty subnets. Per server: %s, then %d repeats "+
		"(reboot-style REQUESTs, alternating its own IP and a peer's IP)", opts.Layout.Servers, start, steps, opts.Repeats)

	storms := make([]string, 0, len(s.rounds))
	for _, r := range s.rounds {
		storms = append(storms, r.storm.Truncate(time.Second).String())
	}

	add("rounds: %d/%d completed, storm took %s (first server start to last server done)",
		fig.finished, fig.planned, strings.Join(storms, ", "))

	if p50, p95, worst, ok := latencySpread(fig.dora); ok {
		add("lease: %d/%d servers bound; DORA p50 %s, p95 %s, max %s (first DISCOVER to final ACK, retries and restarts included)",
			fig.bound, fig.total, p50, p95, worst)
	}

	if fig.timedOut > 0 || fig.pxeDead > 0 {
		add("  not bound: %d without a lease at round end, %d gave up in PXE firmware", fig.timedOut, fig.pxeDead)
	}

	resent := 0
	for _, n := range fig.retransmit {
		resent += n
	}

	if resent == 0 {
		add("retransmits: none, dhcpd answered every request within the client's first 4s timeout, i.e. kept up with the arrival rate")
	} else {
		sorted := slices.Clone(fig.retransmit)
		slices.Sort(sorted)

		add("retransmits: %d, per server p50 %d, max %d. A client resends when no usable answer arrives in time (4s doubling "+
			"to 64s): dhcpd fell behind, or answered with something unusable (see verdict). These waits are what drive DORA "+
			"times up", resent, sorted[nearestRank(len(sorted), 50)], sorted[len(sorted)-1])
	}

	if fig.restarts > 0 {
		add("  restarts: %d DORAs went back to DISCOVER after 5 unanswered REQUESTs, i.e. dhcpd dropped them, as it does "+
			"when lease writes exceed its update budget", fig.restarts)
	}

	if totals.late > 0 {
		add("  late answers: %d, arriving after the client stopped waiting (a resent request already answered, or one "+
			"given up on), a sign of dhcpd working through a backlog", totals.late)
	}

	if p50, p95, worst, ok := latencySpread(fig.renew); ok {
		add("renewals: p50 %s, p95 %s, max %s", p50, p95, worst)
	}

	own, taken := fig.repeats[repeatOwn], fig.repeats[repeatTaken]
	if sumOutcomes(own)+sumOutcomes(taken) > 0 {
		add("repeats: own IP ACKed %d/%d, peer's IP NAKed %d/%d (RFC 2131 expects all of both)",
			own[outcomeAckSame], sumOutcomes(own), taken[outcomeNak], sumOutcomes(taken))
	}

	if lost := totals.dropped + totals.overflowed + totals.readErrors + fig.sendErrors; lost > 0 {
		add("bench host: %d packets lost locally, not by dhcpd, so retransmits and times above overstate dhcpd's", lost)
	} else {
		add("bench host: no packets lost locally, the figures above are dhcpd's alone")
	}

	failed := 0
	for _, name := range checkOrder {
		if s.checks.count(name) > 0 {
			failed++
		}
	}

	switch {
	case failed == 0 && fig.finished == fig.planned:
		add("verdict: no misbehaviour, every answer followed RFC 2131 and the stored leases match what servers were given")
	case failed == 0:
		add("verdict: no misbehaviour in the completed rounds, but only %d/%d completed", fig.finished, fig.planned)
	default:
		add("verdict: failed checks: %d (examples under Misbehaviour above)", failed)

		for _, name := range checkOrder {
			if n := s.checks.count(name); n > 0 {
				add("  %s: %d, %s", name, n, checkMeaning[name])
			}
		}
	}

	return out
}

// latencySpread is the median, 95th percentile and worst of a series, to the
// millisecond.
func latencySpread(lat []time.Duration) (time.Duration, time.Duration, time.Duration, bool) {
	if len(lat) == 0 {
		return 0, 0, 0, false
	}

	sorted := slices.Clone(lat)
	slices.Sort(sorted)

	at := func(p int) time.Duration { return sorted[nearestRank(len(sorted), p)].Truncate(time.Millisecond) }

	return at(50), at(95), sorted[len(sorted)-1].Truncate(time.Millisecond), true
}

func sumOutcomes(outcomes [repeatOutcomes]int64) int64 {
	sum := int64(0)
	for _, n := range outcomes {
		sum += n
	}

	return sum
}
