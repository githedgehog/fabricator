// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// The probe audit answers "what did this connectivity run actually assert",
// which neither connectivity path could report: both count only errors, so a
// green run is indistinguishable from one that probed nothing.
//
// One assertion is one probe execution that produced a verdict, at exactly the
// granularity of the probe helpers' "... result" debug lines: one per
// checkPing, one per iperf3 attempt, one per curl iteration, one per TCP probe,
// one per UDP probe. The helpers are the only thing both paths and all four
// matrix phases go through, so recording there covers a phase nobody thought to
// instrument.
//
// The matrix path additionally claims, from entryOwner over the same endpoint
// pair space the phases walk, which probes it expects to happen. Claims that
// end up with no assertion are the shortfall: the dynamic half of the invariant
// Validate covers statically. The audit only reports; it never changes a
// probe's verdict or a run's exit status.

type ProbeKind string

const (
	ProbeKindPing  ProbeKind = "ping"
	ProbeKindIPerf ProbeKind = "iperf"
	ProbeKindCurl  ProbeKind = "curl"
	ProbeKindTCP   ProbeKind = "tcp"
	ProbeKindUDP   ProbeKind = "udp"
	// the port-forward phase's allow path runs its own iperf3 client rather than
	// checkIPerf, and emits no result line, so it is the one probe a log-based
	// counter cannot see at all
	ProbeKindPortForward ProbeKind = "portforward"
)

// ProbeKinds is the reporting order of the probe kinds.
var ProbeKinds = []ProbeKind{ProbeKindPing, ProbeKindIPerf, ProbeKindCurl, ProbeKindTCP, ProbeKindUDP, ProbeKindPortForward}

type ProbeOutcome string

const (
	// the probe ran and classified the path: one assertion
	ProbeAsserted ProbeOutcome = "asserted"
	// the probe deliberately did not run (knob off, or nothing to measure)
	ProbeSkipped ProbeOutcome = "skipped"
	// the probe ran but could not classify the path, so it asserted nothing
	ProbeInconclusive ProbeOutcome = "inconclusive"
)

// Skip reasons. Each one is a legitimate no-op today, and each one is
// indistinguishable from lost coverage without this record.
const (
	probeSkipPingsDisabled  = "pings disabled"
	probeSkipIPerfsDisabled = "iperfs disabled"
	probeSkipCurlsDisabled  = "curls disabled"
	probeSkipIPerfDeny      = "iperf3 only measures throughput on an allow path"
	probeSkipIPerfBidirRev  = "reverse direction of a bidirectional iperf3 session"
)

// ProbeTarget identifies what a probe was aimed at, in terms both the probe
// helper and a matrix claim can compute independently: the source server, the
// address actually dialed (post-NAT), and the protocol/port.
type ProbeTarget struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Protocol string `json:"protocol,omitempty"`
	Port     uint16 `json:"port,omitempty"`
}

func (t ProbeTarget) String() string {
	out := t.From + " → " + t.To
	if t.Protocol != "" {
		out += fmt.Sprintf(" [%s/%d]", t.Protocol, t.Port)
	}

	return out
}

func (t ProbeTarget) reversed() ProbeTarget {
	t.From, t.To = t.To, t.From

	return t
}

// ProbeRecord is one probe execution.
type ProbeRecord struct {
	Kind    ProbeKind
	Target  ProbeTarget
	Outcome ProbeOutcome
	// Expected reachability the probe was asserting against.
	Expected bool
	// Bidir marks a single iperf3 session that measured both directions, so it
	// asserts the reversed target too.
	Bidir bool
	// Why a skipped or inconclusive record asserted nothing.
	Why string
}

// The target constructors are the join between a probe execution and a matrix
// claim, so both sides must build them the same way. Each kind gets its own
// Protocol tag: the tag is what keeps an iperf3 throughput run against the
// persistent 5201 daemon from colliding with a tcp/5201 proto-port probe.

func pingProbeTarget(from string, toIP netip.Addr) ProbeTarget {
	return ProbeTarget{From: from, To: toIP.String(), Protocol: "icmp"}
}

func iperfProbeTarget(from, to string) ProbeTarget {
	return ProbeTarget{From: from, To: to, Protocol: "iperf3"}
}

func curlProbeTarget(from, to string) ProbeTarget {
	return ProbeTarget{From: from, To: to, Protocol: "http"}
}

func portProbeTarget(from string, toIP netip.Addr, protocol string, port uint16) ProbeTarget {
	return ProbeTarget{From: from, To: toIP.String(), Protocol: protocol, Port: port}
}

func (r ProbeRecord) asserted() ProbeRecord {
	r.Outcome = ProbeAsserted

	return r
}

func (r ProbeRecord) bidir() ProbeRecord {
	r.Outcome = ProbeAsserted
	r.Bidir = true

	return r
}

func (r ProbeRecord) skipped(why string) ProbeRecord {
	r.Outcome = ProbeSkipped
	r.Why = why

	return r
}

func (r ProbeRecord) inconclusive(why string) ProbeRecord {
	r.Outcome = ProbeInconclusive
	r.Why = why

	return r
}

// probeClaim is a matrix entry's statement that some phase will probe it.
type probeClaim struct {
	phase  matrixPhase
	entry  string
	target ProbeTarget
	// filtered records that --source/--destination excluded the pair, a second
	// gate Validate does not model.
	filtered bool
}

type probeAudit struct {
	path string

	mu      sync.Mutex
	records []ProbeRecord
	claims  []probeClaim
	// notProbed counts entries no phase reads by design, by which rule applies.
	notProbed map[string]int
	claimed   map[matrixPhase]int
}

func newProbeAudit(path string) *probeAudit {
	return &probeAudit{path: path, notProbed: map[string]int{}, claimed: map[matrixPhase]int{}}
}

type probeAuditKeyType struct{}

var probeAuditKey = probeAuditKeyType{}

func withProbeAudit(ctx context.Context, a *probeAudit) context.Context {
	return context.WithValue(ctx, probeAuditKey, a)
}

// probeAuditFrom returns nil when no run is being audited, which is the case
// for every caller that has not opted in. All methods are nil-safe.
func probeAuditFrom(ctx context.Context) *probeAudit {
	a, _ := ctx.Value(probeAuditKey).(*probeAudit)

	return a
}

func (a *probeAudit) record(r ProbeRecord) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, r)
}

func (a *probeAudit) claim(c probeClaim) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claims = append(a.claims, c)
	a.claimed[c.phase]++
}

func (a *probeAudit) recordNotProbed(rule string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.notProbed[rule]++
}

// recordProbe is the helpers' entry point: one call per probe execution.
func recordProbe(ctx context.Context, r ProbeRecord) {
	probeAuditFrom(ctx).record(r)
}

// connectivityPathMatrix and connectivityPathLegacy name the two connectivity
// paths in the audit output, so one artifact schema covers both.
const (
	connectivityPathMatrix = "matrix"
	connectivityPathLegacy = "legacy"
)

// externalCurlTarget is the address both paths curl to test external
// connectivity. 8.8.8.8 was giving trouble over virtual external peerings.
const externalCurlTarget = "1.0.0.1"

// claimMatrix records what the matrix expects to be probed, walking the same
// endpoint pair space the phases walk rather than the stored entries: Lookup
// synthesizes a Deny for a pair nobody stored, and the server-to-server phase
// probes those, so counting stored entries would miss most of a run.
func (a *probeAudit) claimMatrix(m *ConnectivityMatrix, inSources, inDestinations func(string) bool) {
	if a == nil || m == nil {
		return
	}
	for _, src := range m.AllEndpoints {
		// every phase probes from a server, so an external source owns nothing
		if src.Server == nil {
			continue
		}
		for _, dst := range m.AllEndpoints {
			entries := append([]ConnectivityExpectation{m.Lookup(src, dst, ProtoPort{})}, m.ProtoPortEntries(src, dst)...)
			for _, e := range entries {
				owner := m.entryOwner(e)
				switch owner {
				case matrixPhaseSkipped:
					a.recordNotProbed(matrixSkipRule(m, e))
				case matrixPhaseUnprobed:
					// Validate rejects these, so this only fires for a caller
					// that reached the phases without it
					a.recordNotProbed(matrixPhaseUnprobed.String())
				case matrixPhaseServerServer, matrixPhasePortForward, matrixPhaseProtoPort, matrixPhaseCurl:
					entry := describeMatrixEntry(e)
					filtered := !claimReaches(owner, src, dst, inSources, inDestinations)
					for _, target := range matrixClaimTargets(owner, e, src, dst) {
						a.claim(probeClaim{phase: owner, entry: entry, target: target, filtered: filtered})
					}
				}
			}
		}
	}
}

// claimReaches reports whether --source/--destination leave the pair in scope.
// This is the second gate entryOwner does not model: an entry can be owned by a
// phase and still legitimately never be probed.
func claimReaches(owner matrixPhase, src, dst *Endpoint, inSources, inDestinations func(string) bool) bool {
	if !inSources(src.Server.Name) {
		return false
	}
	// the curl probe is untargeted, and an external port-forward target has no
	// server name to filter on
	if owner == matrixPhaseCurl || dst.Server == nil {
		return true
	}

	return inDestinations(dst.Server.Name)
}

// matrixClaimTargets is the probes an owned entry expects, built with the same
// constructors the probe helpers use so a claim and a record join exactly.
func matrixClaimTargets(owner matrixPhase, e ConnectivityExpectation, src, dst *Endpoint) []ProbeTarget {
	from := src.Server.Name
	switch owner {
	case matrixPhaseServerServer:
		targets := []ProbeTarget{pingProbeTarget(from, matrixTargetIP(e, dst))}
		// checkIPerf only measures throughput on an allow path, so claiming it
		// for a deny entry would report a shortfall on every isolated pair
		if e.Verdict == VerdictAllow {
			targets = append(targets, iperfProbeTarget(from, dst.Server.Name))
		}

		return targets
	case matrixPhasePortForward:
		return []ProbeTarget{portProbeTarget(from, e.NAT.DestinationIP, "tcp", e.NAT.DestinationPort)}
	case matrixPhaseProtoPort:
		if e.ProtoPort.Protocol == "icmp" {
			return []ProbeTarget{pingProbeTarget(from, matrixTargetIP(e, dst))}
		}

		return []ProbeTarget{portProbeTarget(from, matrixTargetIP(e, dst), e.ProtoPort.Protocol, e.ProtoPort.Port)}
	case matrixPhaseCurl:
		return []ProbeTarget{curlProbeTarget(from, externalCurlTarget)}
	case matrixPhaseUnprobed, matrixPhaseSkipped:
		return nil
	default:
		return nil
	}
}

// matrixSkipRule labels an entry no phase reads by design. entryOwner stays
// authoritative for the classification; this only names the rule for the report.
func matrixSkipRule(m *ConnectivityMatrix, e ConnectivityExpectation) string {
	src, dst := e.Pair.Source, e.Pair.Destination
	switch {
	case src == dst:
		return "selfPair"
	case IsSameEndpointNode(src, dst):
		return "sameNode"
	case dst != nil && dst.Server != nil && m.HasProtoPortEntries(src, dst):
		return "supersededByProtoPort"
	case dst != nil && dst.External != nil:
		return "siblingExternalAllow"
	default:
		return "other"
	}
}

// claimLegacyPair and claimLegacyCurl give the legacy path the same
// claimed-against-asserted columns. It has no matrix and so no ownership to
// reconcile against, but it does compute an expectation per pair, which is the
// same statement in a different shape.
func (a *probeAudit) claimLegacyPair(from, to string, toIP netip.Addr, expected Reachability) {
	entry := from + " → " + to
	a.claim(probeClaim{phase: matrixPhaseServerServer, entry: entry, target: pingProbeTarget(from, toIP)})
	if expected.Reachable {
		a.claim(probeClaim{phase: matrixPhaseServerServer, entry: entry, target: iperfProbeTarget(from, to)})
	}
}

func (a *probeAudit) claimLegacyCurl(from string) {
	a.claim(probeClaim{
		phase:  matrixPhaseCurl,
		entry:  from + " → external",
		target: curlProbeTarget(from, externalCurlTarget),
	})
}

// reportProbeAudit summarizes the run, hands it to the suite's sink when one is
// installed, and names every entry that ended with nothing asserted against it.
// It only reports: a shortfall never changes the run's outcome.
func reportProbeAudit(ctx context.Context, a *probeAudit, took time.Duration, completed bool) ProbeAuditRun {
	run := a.summarize(took.Round(time.Millisecond).String(), completed)
	probeAuditSinkFrom(ctx).add(run)
	if len(run.Shortfall) > 0 {
		slog.Warn("Connectivity run asserted less than the matrix claimed", "path", run.Path, "entries", len(run.Shortfall))
		for _, s := range run.Shortfall {
			slog.Warn("Matrix entry with no assertion", "phase", s.Phase, "entry", s.Entry, "why", s.Why)
		}
	}

	return run
}

// ProbeCounts is the per-kind assertion tally for one run.
type ProbeCounts struct {
	Asserted     int `json:"asserted"`
	Skipped      int `json:"skipped,omitempty"`
	Inconclusive int `json:"inconclusive,omitempty"`
}

// ProbeShortfall is a claim that ended the run with no assertion against it.
type ProbeShortfall struct {
	Phase string `json:"phase"`
	Entry string `json:"entry"`
	Why   string `json:"why"`
}

// ProbeAuditRun is one connectivity run's audit, as reported and serialized.
type ProbeAuditRun struct {
	// Path is "matrix" or "legacy".
	Path string `json:"path"`
	Took string `json:"took"`
	// Completed is false when the run returned before finishing its phases, so
	// a partial report is not read as lost coverage.
	Completed bool                   `json:"completed"`
	Probes    map[string]ProbeCounts `json:"probes"`
	Claims    map[string]int         `json:"claims,omitempty"`
	// NotProbedByDesign counts entries no phase reads on purpose, by rule.
	NotProbedByDesign map[string]int `json:"notProbedByDesign,omitempty"`
	// Filtered counts claims whose pair --source/--destination excluded.
	Filtered  int              `json:"filtered,omitempty"`
	Shortfall []ProbeShortfall `json:"shortfall,omitempty"`
	// Unclaimed lists assertions no claim asked for, which catches the claim
	// builder drifting away from the phases.
	Unclaimed []string `json:"unclaimed,omitempty"`
}

func (p matrixPhase) String() string {
	switch p {
	case matrixPhaseUnprobed:
		return "unprobed"
	case matrixPhaseSkipped:
		return "skipped"
	case matrixPhaseServerServer:
		return "serverServer"
	case matrixPhasePortForward:
		return "portForward"
	case matrixPhaseProtoPort:
		return "protoPort"
	case matrixPhaseCurl:
		return "curl"
	default:
		return fmt.Sprintf("matrixPhase(%d)", int(p))
	}
}

// summarize reconciles the claims against the records. A claim is satisfied by
// at least one asserted record for its target: several claims can collapse onto
// one probe (two endpoints of the same server dial one address, sibling
// externals share one untargeted curl), and a bidirectional iperf3 session
// asserts both directions from one execution.
func (a *probeAudit) summarize(took string, completed bool) ProbeAuditRun {
	if a == nil {
		return ProbeAuditRun{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	run := ProbeAuditRun{Path: a.path, Took: took, Completed: completed, Probes: map[string]ProbeCounts{}}

	assertedBy := map[ProbeTarget][]ProbeKind{}
	unmetBy := map[ProbeTarget]string{}
	for _, r := range a.records {
		counts := run.Probes[string(r.Kind)]
		switch r.Outcome {
		case ProbeAsserted:
			counts.Asserted++
			assertedBy[r.Target] = append(assertedBy[r.Target], r.Kind)
			if r.Bidir {
				rev := r.Target.reversed()
				assertedBy[rev] = append(assertedBy[rev], r.Kind)
			}
		case ProbeSkipped:
			counts.Skipped++
			if _, seen := unmetBy[r.Target]; !seen {
				unmetBy[r.Target] = r.Why
			}
		case ProbeInconclusive:
			counts.Inconclusive++
			if _, seen := unmetBy[r.Target]; !seen {
				unmetBy[r.Target] = r.Why
			}
		}
		run.Probes[string(r.Kind)] = counts
	}

	for _, c := range a.claims {
		if c.filtered {
			run.Filtered++

			continue
		}
		if len(assertedBy[c.target]) > 0 {
			continue
		}
		why := unmetBy[c.target]
		if why == "" {
			why = "no probe recorded"
		}
		run.Shortfall = append(run.Shortfall, ProbeShortfall{Phase: c.phase.String(), Entry: c.entry, Why: why})
	}
	slices.SortFunc(run.Shortfall, func(x, y ProbeShortfall) int {
		if x.Phase != y.Phase {
			return strings.Compare(x.Phase, y.Phase)
		}

		return strings.Compare(x.Entry, y.Entry)
	})

	claimedTargets := map[ProbeTarget]struct{}{}
	for _, c := range a.claims {
		claimedTargets[c.target] = struct{}{}
	}
	if len(a.claims) > 0 {
		seen := map[string]struct{}{}
		for target := range assertedBy {
			if _, ok := claimedTargets[target]; ok {
				continue
			}
			if _, dup := seen[target.String()]; dup {
				continue
			}
			seen[target.String()] = struct{}{}
			run.Unclaimed = append(run.Unclaimed, target.String())
		}
		slices.Sort(run.Unclaimed)
	}

	if len(a.claimed) > 0 {
		run.Claims = map[string]int{}
		for phase, n := range a.claimed {
			run.Claims[phase.String()] = n
		}
	}
	if len(a.notProbed) > 0 {
		run.NotProbedByDesign = map[string]int{}
		for rule, n := range a.notProbed {
			run.NotProbedByDesign[rule] = n
		}
	}

	return run
}

// Asserted is the run's total assertion count.
func (r ProbeAuditRun) Asserted() int {
	var n int
	for _, c := range r.Probes {
		n += c.Asserted
	}

	return n
}

// LogArgs renders the run for the connectivity paths' pass and fail log lines,
// which until now reported only a duration and an error count.
func (r ProbeAuditRun) LogArgs() []any {
	out := []any{"asserted", r.Asserted()}
	for _, kind := range ProbeKinds {
		c, ok := r.Probes[string(kind)]
		if !ok {
			continue
		}
		out = append(out, string(kind), c.Asserted)
	}
	var skipped, inconclusive, claimed int
	for _, c := range r.Probes {
		skipped += c.Skipped
		inconclusive += c.Inconclusive
	}
	for _, n := range r.Claims {
		claimed += n
	}
	if skipped > 0 {
		out = append(out, "skipped", skipped)
	}
	if inconclusive > 0 {
		out = append(out, "inconclusive", inconclusive)
	}
	if claimed > 0 {
		out = append(out, "claimed", claimed)
	}
	if r.Filtered > 0 {
		out = append(out, "filtered", r.Filtered)
	}
	if len(r.Shortfall) > 0 {
		out = append(out, "shortfall", len(r.Shortfall))
	}
	if len(r.Unclaimed) > 0 {
		out = append(out, "unclaimed", len(r.Unclaimed))
	}

	return out
}

// ProbeAuditSink collects the runs of one release test case. Installed on the
// context by the suite runner, so the connectivity call sites need no changes.
type ProbeAuditSink struct {
	mu   sync.Mutex
	runs []ProbeAuditRun
}

type probeAuditSinkKeyType struct{}

var probeAuditSinkKey = probeAuditSinkKeyType{}

func WithProbeAuditSink(ctx context.Context, s *ProbeAuditSink) context.Context {
	return context.WithValue(ctx, probeAuditSinkKey, s)
}

func probeAuditSinkFrom(ctx context.Context) *ProbeAuditSink {
	s, _ := ctx.Value(probeAuditSinkKey).(*ProbeAuditSink)

	return s
}

func (s *ProbeAuditSink) add(run ProbeAuditRun) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, run)
}

func (s *ProbeAuditSink) Runs() []ProbeAuditRun {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.runs)
}

// ProbeAuditReport is the artifact written beside the JUnit results.
type ProbeAuditReport struct {
	Suites []ProbeAuditSuite `json:"suites"`
}

type ProbeAuditSuite struct {
	Name  string           `json:"name"`
	Tests []ProbeAuditTest `json:"tests"`
}

type ProbeAuditTest struct {
	Name string          `json:"name"`
	Runs []ProbeAuditRun `json:"runs"`
}
