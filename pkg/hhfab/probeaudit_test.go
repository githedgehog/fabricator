// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func externalEP(name string) *Endpoint {
	return &Endpoint{External: &ExternalEndpoint{ExternalName: name}}
}

// allServers is the unfiltered --source/--destination predicate.
func allServers(string) bool { return true }

// claimed builds the matrix's claim set with no source/destination filtering.
func claimed(m *ConnectivityMatrix) *probeAudit {
	a := newProbeAudit(connectivityPathMatrix)
	a.claimMatrix(m, allServers, allServers)

	return a
}

func shortfallEntries(run ProbeAuditRun) []string {
	out := make([]string, 0, len(run.Shortfall))
	for _, s := range run.Shortfall {
		out = append(out, s.Entry)
	}

	return out
}

func TestClaimMatrix_SelfAndSameNodePairsAreNotShortfalls(t *testing.T) {
	// One server with two addresses is two endpoints. The self pair and the
	// two cross-endpoint pairs are the same node, so no phase reads them and
	// they must not read as lost coverage.
	m := NewConnectivityMatrix()
	a1 := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	a2 := serverEP("server-1", "vpc-2", "default", "10.0.2.1")
	m.AllEndpoints = []*Endpoint{a1, a2}

	run := claimed(m).summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Empty(t, run.Claims, "nothing is owned by a phase")
	require.Equal(t, 2, run.NotProbedByDesign["selfPair"])
	require.Equal(t, 2, run.NotProbedByDesign["sameNode"])
}

func TestClaimMatrix_DefaultEntrySupersededByProtoPort(t *testing.T) {
	// A pair with proto-port entries has its default entry read by nobody: the
	// proto-port phase covers it more precisely.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}
	m.Add(ConnectivityExpectation{Pair: EndpointPair{Source: src, Destination: dst}, Verdict: VerdictAllow})
	m.Add(ConnectivityExpectation{
		Pair:      EndpointPair{Source: src, Destination: dst},
		Verdict:   VerdictDeny,
		ProtoPort: ProtoPort{Protocol: "tcp", Port: 5201},
	})

	a := claimed(m)
	// only the proto-port probe runs, and it asserts
	a.record(ProbeRecord{
		Kind:   ProbeKindTCP,
		Target: portProbeTarget("server-1", netip.MustParseAddr("10.0.2.2"), "tcp", 5201),
	}.asserted())
	// the reverse pair has no entries at all, so it is a plain server-to-server deny
	a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget("server-2", netip.MustParseAddr("10.0.1.1"))}.asserted())

	run := a.summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Equal(t, 1, run.NotProbedByDesign["supersededByProtoPort"])
	require.Equal(t, 1, run.Claims[matrixPhaseProtoPort.String()])
	require.Equal(t, 1, run.Claims[matrixPhaseServerServer.String()])
	require.Equal(t, 2, run.Asserted())
}

func TestClaimMatrix_SourceDestinationFilterIsNotAShortfall(t *testing.T) {
	// --source/--destination is a second gate Validate does not model: an entry
	// can be owned by a phase and legitimately never probed.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	is1 := func(name string) bool { return name == "server-1" }
	is2 := func(name string) bool { return name == "server-2" }

	// --source server-1 --destination server-2: only the forward direction is
	// in scope, and only it is expected to assert.
	a := newProbeAudit(connectivityPathMatrix)
	a.claimMatrix(m, is1, is2)
	a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget("server-1", netip.MustParseAddr("10.0.2.2"))}.asserted())

	run := a.summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Equal(t, 1, run.Filtered, "the reverse direction is out of scope")

	// Neither direction survives both filters, so nothing asserts and nothing
	// is missing.
	a = newProbeAudit(connectivityPathMatrix)
	a.claimMatrix(m, is2, is2)
	run = a.summarize("0s", true)

	require.Empty(t, run.Shortfall, "an excluded pair asserts nothing and that is expected")
	require.Zero(t, run.Asserted())
	require.Equal(t, 2, run.Filtered)
}

func TestClaimMatrix_PingsDisabledIsAShortfallWithItsReason(t *testing.T) {
	// The case the disabled-probe guard exists for: nothing is stored, so the
	// entries are synthesized by Lookup, and checkPing no-ops at count 0.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	a := claimed(m)
	ctx := withProbeAudit(context.Background(), a)
	require.Nil(t, checkPing(ctx, 0, nil, "server-1", "server-2", nil, netip.MustParseAddr("10.0.2.2"), nil, Reachability{}))
	require.Nil(t, checkPing(ctx, 0, nil, "server-2", "server-1", nil, netip.MustParseAddr("10.0.1.1"), nil, Reachability{}))

	run := a.summarize("0s", true)

	require.Len(t, run.Shortfall, 2)
	require.Equal(t, probeSkipPingsDisabled, run.Shortfall[0].Why)
	require.Zero(t, run.Asserted())
	require.Equal(t, 2, run.Probes[string(ProbeKindPing)].Skipped)
}

func TestClaimMatrix_IPerfOnDenyPairIsNotClaimed(t *testing.T) {
	// checkIPerf only measures throughput on an allow path, so a deny entry
	// expects a ping and nothing else.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	a := claimed(m)
	for _, pair := range [][2]string{{"server-1", "10.0.2.2"}, {"server-2", "10.0.1.1"}} {
		a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget(pair[0], netip.MustParseAddr(pair[1]))}.asserted())
	}

	run := a.summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Equal(t, 2, run.Claims[matrixPhaseServerServer.String()], "one ping claim per direction, no iperf claim")
}

func TestSummarize_BidirIPerfAssertsBothDirections(t *testing.T) {
	// One bidir session measures both halves, so it satisfies both directions'
	// claims from a single execution.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-1", "default", "10.0.1.2")
	m.AllEndpoints = []*Endpoint{src, dst}
	for _, p := range []EndpointPair{{Source: src, Destination: dst}, {Source: dst, Destination: src}} {
		m.Add(ConnectivityExpectation{Pair: p, Verdict: VerdictAllow, Reason: ReachabilityReasonIntraVPC})
	}

	a := claimed(m)
	a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget("server-1", netip.MustParseAddr("10.0.1.2"))}.asserted())
	a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget("server-2", netip.MustParseAddr("10.0.1.1"))}.asserted())
	a.record(ProbeRecord{Kind: ProbeKindIPerf, Target: iperfProbeTarget("server-1", "server-2")}.bidir())

	run := a.summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Equal(t, 4, run.Claims[matrixPhaseServerServer.String()], "ping and iperf per direction")
	require.Equal(t, 1, run.Probes[string(ProbeKindIPerf)].Asserted, "one session is one assertion")
}

func TestClaimMatrix_SiblingExternalsShareOneCurl(t *testing.T) {
	// The curl probe is untargeted and runs once per source, so an allow to one
	// external pins the outcome for its unevaluable siblings.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	extA, extB := externalEP("ext-a"), externalEP("ext-b")
	m.AllEndpoints = []*Endpoint{src, extA, extB}
	m.Add(ConnectivityExpectation{Pair: EndpointPair{Source: src, Destination: extA}, Verdict: VerdictAllow})
	m.Add(ConnectivityExpectation{Pair: EndpointPair{Source: src, Destination: extB}, Verdict: VerdictUnknown})

	a := claimed(m)
	a.record(ProbeRecord{Kind: ProbeKindCurl, Target: curlProbeTarget("server-1", externalCurlTarget)}.asserted())

	run := a.summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Equal(t, 1, run.Claims[matrixPhaseCurl.String()])
	require.Equal(t, 1, run.NotProbedByDesign["siblingExternalAllow"])
}

func TestClaimMatrix_PortForwardClaimsTheTranslatedTarget(t *testing.T) {
	// The probe dials the NAT pool address and port, not the server's own.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}
	m.Add(ConnectivityExpectation{
		Pair:    EndpointPair{Source: src, Destination: dst},
		Verdict: VerdictAllow,
		NAT: &TranslatedAddress{
			DestinationIP:   netip.MustParseAddr("100.64.0.5"),
			DestinationPort: 8080,
		},
	})

	a := claimed(m)
	// dialing the real address instead would leave the claim unmet
	a.record(ProbeRecord{
		Kind:   ProbeKindPortForward,
		Target: portProbeTarget("server-1", netip.MustParseAddr("100.64.0.5"), "tcp", 8080),
	}.asserted())
	a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget("server-2", netip.MustParseAddr("10.0.1.1"))}.asserted())

	run := a.summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Equal(t, 1, run.Claims[matrixPhasePortForward.String()])
	require.Empty(t, run.Unclaimed)
}

func TestSummarize_UnprobedProtoPortEntryIsAShortfall(t *testing.T) {
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}
	for _, pp := range []ProtoPort{{Protocol: "tcp", Port: 5201}, {Protocol: "udp", Port: 5201}} {
		m.Add(ConnectivityExpectation{
			Pair:      EndpointPair{Source: src, Destination: dst},
			Verdict:   VerdictDeny,
			ProtoPort: pp,
		})
	}

	a := claimed(m)
	// tcp probed, udp silently not
	a.record(ProbeRecord{
		Kind:   ProbeKindTCP,
		Target: portProbeTarget("server-1", netip.MustParseAddr("10.0.2.2"), "tcp", 5201),
	}.asserted())
	a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget("server-2", netip.MustParseAddr("10.0.1.1"))}.asserted())

	run := a.summarize("0s", true)

	require.Len(t, run.Shortfall, 1)
	require.Equal(t, matrixPhaseProtoPort.String(), run.Shortfall[0].Phase)
	require.Contains(t, run.Shortfall[0].Entry, "[udp/5201]")
	require.Equal(t, "no probe recorded", run.Shortfall[0].Why)
}

func TestSummarize_InconclusiveProbeAssertsNothing(t *testing.T) {
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	a := claimed(m)
	a.record(ProbeRecord{
		Kind:   ProbeKindPing,
		Target: pingProbeTarget("server-1", netip.MustParseAddr("10.0.2.2")),
	}.inconclusive("ping did not execute: exit 127"))
	a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget("server-2", netip.MustParseAddr("10.0.1.1"))}.asserted())

	run := a.summarize("0s", true)

	require.Len(t, run.Shortfall, 1)
	require.Equal(t, "ping did not execute: exit 127", run.Shortfall[0].Why)
	require.Equal(t, 1, run.Probes[string(ProbeKindPing)].Asserted)
	require.Equal(t, 1, run.Probes[string(ProbeKindPing)].Inconclusive)
}

func TestSummarize_UnclaimedAssertionIsReported(t *testing.T) {
	// A probe no claim asked for means the claim builder has drifted from the
	// phases, which is a defect in the audit rather than in the run.
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	a := claimed(m)
	for _, pair := range [][2]string{{"server-1", "10.0.2.2"}, {"server-2", "10.0.1.1"}} {
		a.record(ProbeRecord{Kind: ProbeKindPing, Target: pingProbeTarget(pair[0], netip.MustParseAddr(pair[1]))}.asserted())
	}
	a.record(ProbeRecord{
		Kind:   ProbeKindUDP,
		Target: portProbeTarget("server-1", netip.MustParseAddr("10.0.2.2"), "udp", 9999),
	}.asserted())

	run := a.summarize("0s", true)

	require.Empty(t, run.Shortfall)
	require.Equal(t, []string{"server-1 → 10.0.2.2 [udp/9999]"}, run.Unclaimed)
}

func TestRecordProbe_NoRecorderInContextIsANoOp(t *testing.T) {
	// Every caller that has not opted in must behave exactly as before.
	ctx := context.Background()
	require.Nil(t, probeAuditFrom(ctx))
	require.Nil(t, probeAuditSinkFrom(ctx))
	require.NotPanics(t, func() {
		recordProbe(ctx, ProbeRecord{Kind: ProbeKindPing}.asserted())
		require.Nil(t, checkPing(ctx, 0, nil, "server-1", "server-2", nil, netip.MustParseAddr("10.0.2.2"), nil, Reachability{}))
	})

	var nilAudit *probeAudit
	require.NotPanics(t, func() {
		nilAudit.claimMatrix(NewConnectivityMatrix(), allServers, allServers)
		nilAudit.record(ProbeRecord{})
		nilAudit.claim(probeClaim{})
		nilAudit.recordNotProbed("selfPair")
	})
	require.Equal(t, ProbeAuditRun{}, nilAudit.summarize("0s", true))
}

func TestLogArgs_KeysAreUniqueAndDoNotShadowErrorCounts(t *testing.T) {
	// The failure log lines prepend a per-kind *error* count under the bare kind
	// name ("ping", "iperf", "curl"), so an assertion count must not reuse those
	// keys: one slog record with a duplicate key silently drops one of them.
	run := ProbeAuditRun{
		Path:      connectivityPathMatrix,
		Completed: true,
		Probes: map[string]ProbeCounts{
			string(ProbeKindPing):        {Asserted: 20},
			string(ProbeKindIPerf):       {Skipped: 18},
			string(ProbeKindCurl):        {Asserted: 5},
			string(ProbeKindTCP):         {Asserted: 2},
			string(ProbeKindUDP):         {Asserted: 2},
			string(ProbeKindPortForward): {Asserted: 1},
		},
		Claims: map[string]int{matrixPhaseServerServer.String(): 18},
	}

	args := append([]any{"ping", 2, "iperf", 4, "curl", 0}, run.LogArgs()...)
	seen := map[string]struct{}{}
	for i := 0; i < len(args); i += 2 {
		key, ok := args[i].(string)
		require.True(t, ok, "key at %d must be a string", i)
		_, dup := seen[key]
		require.False(t, dup, "duplicate log key %q", key)
		seen[key] = struct{}{}
	}

	require.Contains(t, seen, "assertedPing")
	require.Contains(t, seen, "assertedPortforward")
	require.Contains(t, seen, "ping", "the pre-existing error count keeps its key")
}

func TestSink_DirectProbesAreAttributedToTheTest(t *testing.T) {
	// Some tests call checkPing themselves rather than going through a
	// connectivity run. Those assertions still happened and must be counted.
	sink := &ProbeAuditSink{}
	ctx := WithProbeAuditSink(context.Background(), sink)
	require.Nil(t, probeAuditFrom(ctx), "no connectivity run is in scope")

	recordProbe(ctx, ProbeRecord{
		Kind:   ProbeKindPing,
		Target: pingProbeTarget("server-1", netip.MustParseAddr("10.0.2.2")),
	}.asserted())

	runs := sink.Runs()
	require.Len(t, runs, 1)
	require.Equal(t, connectivityPathDirect, runs[0].Path)
	require.Equal(t, 1, runs[0].Probes[string(ProbeKindPing)].Asserted)
	require.Empty(t, runs[0].Shortfall, "a direct probe has no expectation set to fall short of")
}

func TestSink_DirectRunFollowsTheConnectivityRuns(t *testing.T) {
	sink := &ProbeAuditSink{}
	sink.add(ProbeAuditRun{Path: connectivityPathMatrix, Completed: true})
	ctx := WithProbeAuditSink(context.Background(), sink)
	recordProbe(ctx, ProbeRecord{Kind: ProbeKindPing}.asserted())

	runs := sink.Runs()
	require.Len(t, runs, 2)
	require.Equal(t, connectivityPathMatrix, runs[0].Path)
	require.Equal(t, connectivityPathDirect, runs[1].Path)
}

func TestRecordProbe_RunRecorderWinsOverTheSink(t *testing.T) {
	sink := &ProbeAuditSink{}
	audit := newProbeAudit(connectivityPathMatrix)
	ctx := withProbeAudit(WithProbeAuditSink(context.Background(), sink), audit)
	recordProbe(ctx, ProbeRecord{Kind: ProbeKindPing}.asserted())

	require.Empty(t, sink.Runs(), "nothing should land in the ad-hoc run")
	require.Equal(t, 1, audit.summarize("0s", true).Probes[string(ProbeKindPing)].Asserted)
}

func TestProbeAuditFilePath(t *testing.T) {
	require.Equal(t, "release-test-connectivity-audit.json", probeAuditFilePath("release-test.xml"))
	require.Equal(t, "/tmp/a.b/res-connectivity-audit.json", probeAuditFilePath("/tmp/a.b/res.xml"))
	require.Equal(t, "results-connectivity-audit.json", probeAuditFilePath("results"))
}
