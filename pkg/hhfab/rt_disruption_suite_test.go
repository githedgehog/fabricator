// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/xml"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lostRun is n consecutive lost echoes of the probe, one interval apart, the first at start
func lostRun(probe string, start time.Time, n int) []LostPing {
	var out []LostPing
	for i := range n {
		out = append(out, LostPing{Probe: probe, Seq: i + 1, At: start.Add(time.Duration(i) * trafficPingInterval)})
	}

	return out
}

func TestLongLossWindowsShortWindowPasses(t *testing.T) {
	base := time.Unix(1782458000, 0)

	// a leaf leaving costs two pings
	require.Empty(t, longLossWindows(lostRun("a -> b", base, 2)))
	// and five in a row are still the second
	require.Empty(t, longLossWindows(lostRun("a -> b", base, 5)))
}

func TestLongLossWindowsLongWindowBeforeSettledFails(t *testing.T) {
	base := time.Unix(1782458000, 0)
	settled := base.Add(60 * time.Second)

	// 45 s without replies that ends 15 s before the leaf applied its config
	lost := lostRun("a -> b", base, 225)
	require.Empty(t, LostPings([]ProbeTimeline{{Probe: "a -> b", Samples: lostSamples(lost)}}, settled.Add(disruptionRecovery), settled.Add(time.Minute)),
		"the loss after apply check doesn't see it")

	long := longLossWindows(lost)
	require.Len(t, long, 1)
	require.Equal(t, 225, long[0].Pings)
	require.Equal(t, 45*time.Second, longestLossWindow(long))
}

func TestLongLossWindowsTwoShortWindowsPass(t *testing.T) {
	base := time.Unix(1782458000, 0)

	lost := append(lostRun("a -> b", base, 3), lostRun("a -> b", base.Add(10*time.Second), 3)...)
	require.Empty(t, longLossWindows(lost))
}

func TestLongLossWindowsBoundsEachProbe(t *testing.T) {
	base := time.Unix(1782458000, 0)

	lost := append(lostRun("a -> b", base, 3), lostRun("c -> d", base, 8)...)
	long := longLossWindows(lost)
	require.Len(t, long, 1)
	require.Equal(t, "c -> d", long[0].Probe)
}

func lostSamples(lost []LostPing) []pingSample {
	var out []pingSample
	for _, l := range lost {
		out = append(out, pingSample{Seq: l.Seq, At: l.At})
	}

	return out
}

func TestSuiteTeardownFailureFailsTheSuite(t *testing.T) {
	ts := &JUnitTestSuite{Name: "S", Tests: 1, TestCases: []JUnitTestCase{{Name: "a"}}}
	ts.Teardown = func(context.Context, *VPCPeeringTestCtx) error { return errors.New("wiping VPCs failed") } //nolint:goerr113

	runSuiteTeardown(context.Background(), &VPCPeeringTestCtx{}, ts)

	require.Equal(t, 1, ts.Failures)
	require.Equal(t, 2, ts.Tests)
	require.Len(t, ts.TestCases, 2)
	require.Equal(t, "S Cleanup", ts.TestCases[1].Name)
	require.Contains(t, ts.TestCases[1].Failure.Message, "wiping VPCs failed")

	ok := &JUnitTestSuite{Name: "S", Tests: 1, TestCases: []JUnitTestCase{{Name: "a"}}}
	ok.Teardown = func(context.Context, *VPCPeeringTestCtx) error { return nil }
	runSuiteTeardown(context.Background(), &VPCPeeringTestCtx{}, ok)
	require.Zero(t, ok.Failures)
	require.Len(t, ok.TestCases, 1)
}

func TestDisruptionTestsWithoutCleanSetup(t *testing.T) {
	ctx := context.Background()
	ops := []JUnitTestCase{}
	for _, tc := range makeDisruptionTestSuite().TestCases[1:] {
		ops = append(ops, tc)
	}
	require.Len(t, ops, 5)

	// the baseline didn't hold: no verdict, an inconclusive result instead of a failure
	testCtx := &VPCPeeringTestCtx{disruption: &disruptionState{err: errors.New("lost pings in the baseline")}} //nolint:goerr113
	for _, tc := range ops {
		skip, reverts, err := tc.F(ctx, testCtx, nil)
		require.False(t, skip, tc.Name)
		require.Empty(t, reverts, tc.Name)
		require.ErrorIs(t, err, errInconclusive, tc.Name)
	}

	// the fabric can't run the suite: every test skips with the reason
	testCtx = &VPCPeeringTestCtx{disruption: &disruptionState{skip: errDisruptionNoPair}}
	for _, tc := range ops {
		skip, _, err := tc.F(ctx, testCtx, nil)
		require.True(t, skip, tc.Name)
		require.ErrorIs(t, err, errDisruptionNoPair, tc.Name)
	}

	// nothing was built: the teardown has nothing to do
	require.NoError(t, (&VPCPeeringTestCtx{}).teardownDisruption(ctx))
}

func TestDisruptionTestsKeepTheVPCsBetweenThem(t *testing.T) {
	// the disruption suite runs with wipeBetweenTests off, and the runner only rebuilds after a failed revert
	testCtx := &VPCPeeringTestCtx{wipeBetweenTests: false}
	require.False(t, testCtx.needsSetupBetweenTests(true, false))
	require.True(t, testCtx.needsSetupBetweenTests(true, true))
	require.True(t, (&VPCPeeringTestCtx{wipeBetweenTests: true}).needsSetupBetweenTests(true, false))
}

func TestRoundReportSpecChangesAloneDontStop(t *testing.T) {
	rep := &roundReport{total: 12}
	rep.ran = 3
	rep.specs = []string{"touch-1-leaf-01: leaf-02 3", "touch-2-leaf-02: leaf-01 3"}
	rep.first = "touch-1-leaf-01: leaf-02: spec.catalog.irbVLANs.dt-f2-1: 3004 -> 3003"

	require.True(t, rep.failed())
	require.False(t, rep.stop())

	got := rep.summary()
	require.Contains(t, got, "Agent specs changed in 2 of 3 rounds (touch-1-leaf-01: leaf-02 3; touch-2-leaf-02: leaf-01 3)")
	require.Contains(t, got, "no ping lost in any of the 3 rounds")
	require.Contains(t, got, "stopped after 3 of 12 rounds")
}

func TestRoundReportLossStopsAndIsListedApartFromSpecChanges(t *testing.T) {
	rep := &roundReport{total: 3, ran: 2}
	rep.specs = []string{"restart-fabric-ctrl-1: leaf-01 3"}
	rep.first = "restart-fabric-ctrl-1: leaf-01: spec.catalog.irbVLANs.dt-1a: 3003 -> 3005"
	rep.losses = []string{"restart-fabric-ctrl-2: 4 pings lost: a -> b"}

	require.True(t, rep.stop())
	got := rep.summary()
	require.Contains(t, got, "Agent specs changed in 1 of 2 rounds")
	require.Contains(t, got, "pings lost in 1 of 2 rounds: restart-fabric-ctrl-2: 4 pings lost: a -> b")
	require.NotContains(t, got, "no ping lost")
	require.Contains(t, got, "stopped after 2 of 3 rounds")
}

func TestRoundReportCleanRunHasNoFinding(t *testing.T) {
	rep := &roundReport{total: 3, ran: 3}

	require.False(t, rep.failed())
	require.False(t, rep.stop())
}

func TestSummarizeSpecDiffsCountsPerAgent(t *testing.T) {
	got := summarizeSpecDiffs([]string{
		"leaf-02: spec.catalog.irbVLANs.dt-f2-1: 3003 -> 3004",
		"leaf-01: spec.catalog.irbVLANs.dt-1a: 3004 -> 3003",
		"leaf-02: spec.catalog.irbVLANs.dt-f2-2: 3004 -> 3005",
	})

	require.Equal(t, "leaf-01 1, leaf-02 2", got)
}

func TestSummarizeProbeLossPerProbe(t *testing.T) {
	base := time.Unix(1782458000, 0)
	lost := append(lostRun("a -> b", base, 3), lostRun("c -> d", base.Add(time.Second), 2)...)
	class := map[string]string{"a -> b": probeHard, "c -> d": probeHard}

	got := summarizeProbeLoss(lost, class)

	require.Equal(t, "a -> b (hard): 3 pings lost, "+base.Format("15:04:05.000")+" to "+base.Add(2*trafficPingInterval).Format("15:04:05.000")+"\n"+
		"c -> d (hard): 2 pings lost, "+base.Add(time.Second).Format("15:04:05.000")+" to "+base.Add(time.Second+trafficPingInterval).Format("15:04:05.000")+"\n", got)
	require.Empty(t, summarizeProbeLoss(nil, class))
}

func TestJUnitReportOmitsEmptyAttributes(t *testing.T) {
	results := []JUnitTestSuite{{Name: "S", TestCases: []JUnitTestCase{
		{Name: "a", Failure: &Failure{Message: "m"}},
		{Name: "b", Error: &Error{Message: "m"}},
		{Name: "c", ClassName: "Own"},
	}}}

	require.NoError(t, recapAndReport(results, ReleaseTestOpts{}))
	out, err := xml.Marshal(JUnitReport{Suites: results})
	require.NoError(t, err)

	require.Contains(t, string(out), `<testcase classname="S" name="a"`)
	require.Contains(t, string(out), `<testcase classname="Own" name="c"`)
	require.NotContains(t, string(out), `classname=""`)
	require.NotContains(t, string(out), `type=""`)
}
