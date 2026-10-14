// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
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
