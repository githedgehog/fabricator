// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	appsapi "k8s.io/api/apps/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// lostRun is n consecutive lost echoes of the probe, one interval apart, the first at start
func lostRun(probe string, start time.Time, n int) []lostPing {
	out := make([]lostPing, 0, n)
	for i := range n {
		out = append(out, lostPing{Probe: probe, Seq: i + 1, At: start.Add(time.Duration(i) * trafficPingInterval)})
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
	require.Empty(t, lostPings([]probeTimeline{{Probe: "a -> b", Samples: lostSamples(lost)}}, settled.Add(disruptionRecovery), settled.Add(time.Minute)),
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

func TestJudgeLeafCycle(t *testing.T) {
	base := time.Unix(1782458000, 0)
	at := leafCycleTimes{from: base, requested: base.Add(time.Second), settled: base.Add(time.Minute), end: base.Add(2 * time.Minute)}
	class := map[string]probeClass{"strict": probeStrict, "hard": probeHard, "soft": probeSoft}

	for _, tc := range []struct {
		name       string
		lost       []lostPing
		fail       string // what the one failure says, empty for none
		wantJudged int
	}{
		{name: "strict pair loses a ping", lost: lostRun("strict", base.Add(10*time.Second), 1), fail: "pings lost on pairs that don't cross leaf-02", wantJudged: 1},
		{name: "hard pair down while the leaf is away", lost: lostRun("hard", base.Add(2*time.Second), 250)},
		{name: "hard pair loses before the request", lost: lostRun("hard", base.Add(500*time.Millisecond), 1), fail: "pings lost on pairs that cross leaf-02 before it was taken away", wantJudged: 1},
		{name: "hard pair loses after the leaf applied", lost: lostRun("hard", base.Add(70*time.Second), 1), fail: "after leaf-02 applied its config", wantJudged: 1},
		{name: "soft pair fails over", lost: lostRun("soft", base.Add(2*time.Second), 2), wantJudged: 2},
		{name: "soft pair without replies while the leaf rejoins", lost: lostRun("soft", base.Add(30*time.Second), 100), fail: "loss on ESLAG pairs while leaf-02 rejoined", wantJudged: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := tc.lost[0].Probe
			v, err := judgeLeafCycle([]probeTimeline{{Probe: probe, Samples: lostSamples(tc.lost)}}, class, "leaf-02", at)
			require.NoError(t, err)
			if tc.fail == "" {
				require.Empty(t, v.failures)
			} else {
				require.Len(t, v.failures, 1)
				require.Contains(t, v.failures[0], tc.fail)
			}
			require.Len(t, v.judged, tc.wantJudged)
			require.Len(t, v.expected, len(tc.lost)-tc.wantJudged)
		})
	}

	_, err := judgeLeafCycle([]probeTimeline{{Probe: "unclassified"}}, class, "leaf-02", at)
	require.ErrorIs(t, err, errUnknownProbeClass)
}

func lostSamples(lost []lostPing) []pingSample {
	out := make([]pingSample, 0, len(lost))
	for _, l := range lost {
		out = append(out, pingSample{Seq: l.Seq, At: l.At})
	}

	return out
}

func TestDisruptionCleanupFailureFailsTheSuite(t *testing.T) {
	// a client that knows no fabric types: removing the touch label fails
	env := &disruptionEnv{kube: fake.NewClientBuilder().Build(), sc: &disruptionScenario{}, touched: map[string]bool{"leaf-01": true}}
	sh := &disruptionShared{st: &disruptionState{env: env}}
	results := &JUnitTestSuite{Name: "S", Tests: 1, TestCases: []JUnitTestCase{{Name: "a"}}}

	sh.cleanup(context.Background(), results)

	require.Equal(t, 1, results.Failures)
	require.Equal(t, 2, results.Tests)
	require.Len(t, results.TestCases, 2)
	require.Equal(t, "S Cleanup", results.TestCases[1].Name)
	require.Contains(t, results.TestCases[1].Failure.Message, "getting switch leaf-01")
	require.Nil(t, sh.st, "cleanup runs once")

	// nothing was built: the cleanup has nothing to do
	results = &JUnitTestSuite{Name: "S", Tests: 1, TestCases: []JUnitTestCase{{Name: "a"}}}
	(&disruptionShared{}).cleanup(context.Background(), results)
	require.Zero(t, results.Failures)
	require.Len(t, results.TestCases, 1)
}

func TestDisruptionTestsWithoutCleanSetup(t *testing.T) {
	ctx := context.Background()

	// the setup can't discover the fabric: every test fails with the reason, the first one to
	// run builds the state once
	suite, cleanup := makeDisruptionTestSuite()
	testCtx := &VPCPeeringTestCtx{kube: fake.NewClientBuilder().Build(), extName: "ext", setupOpts: SetupVPCsOpts{VPCMode: vpcapi.VPCModeL2VNI}}
	var first error
	for _, tc := range suite.TestCases[1:] {
		skip, reverts, err := tc.F(ctx, testCtx, nil)
		require.False(t, skip, tc.Name)
		require.Empty(t, reverts, tc.Name)
		require.ErrorContains(t, err, "setup and baseline did not succeed", tc.Name)
		if first == nil {
			first = errors.Unwrap(err)
		}
		require.ErrorIs(t, err, first, tc.Name)
	}
	_, _, err := suite.TestCases[0].F(ctx, testCtx, nil)
	require.ErrorIs(t, err, first)
	// nothing was built: the cleanup has nothing to do
	results := &JUnitTestSuite{Name: suite.Name}
	cleanup(ctx, results)
	require.Zero(t, results.Failures)

	// the fabric can't run the suite: every test skips with the reason
	suite, _ = makeDisruptionTestSuite()
	testCtx = &VPCPeeringTestCtx{setupOpts: SetupVPCsOpts{VPCMode: vpcapi.VPCModeL3VNI}}
	for _, tc := range suite.TestCases {
		skip, _, err := tc.F(ctx, testCtx, nil)
		require.True(t, skip, tc.Name)
		require.ErrorContains(t, err, "ESLAG servers only take part in l2vni", tc.Name)
	}
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
	got := summarizeSpecDiffs([]specDiff{
		{kind: specChanged, agent: "leaf-02", path: "catalog.irbVLANs.dt-f2-1", old: "3003", new: "3004"},
		{kind: specChanged, agent: "leaf-01", path: "catalog.irbVLANs.dt-1a", old: "3004", new: "3003"},
		{kind: specChanged, agent: "leaf-02", path: "catalog.irbVLANs.dt-f2-2", old: "3004", new: "3005"},
	})

	require.Equal(t, "leaf-01 1, leaf-02 2", got)
}

func TestSummarizeProbeLossPerProbe(t *testing.T) {
	base := time.Unix(1782458000, 0)
	lost := append(lostRun("a -> b", base, 3), lostRun("c -> d", base.Add(time.Second), 2)...)
	class := map[string]probeClass{"a -> b": probeHard, "c -> d": probeHard}

	got := summarizeProbeLoss(lost, class)

	require.Equal(t, "a -> b (hard): 3 pings lost, "+base.Format("15:04:05.000")+" to "+base.Add(2*trafficPingInterval).Format("15:04:05.000")+"\n"+
		"c -> d (hard): 2 pings lost, "+base.Add(time.Second).Format("15:04:05.000")+" to "+base.Add(time.Second+trafficPingInterval).Format("15:04:05.000")+"\n", got)
	require.Empty(t, summarizeProbeLoss(nil, class))
}

func TestDisruptionWaits(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, agentapi.AddToScheme(scheme))
	require.NoError(t, appsapi.AddToScheme(scheme))
	one := int32(1)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&agentapi.Agent{Name: "leaf-01", Namespace: kmetav1.NamespaceDefault},
		&appsapi.Deployment{
			Name:      "ctrl",
			Namespace: "fab",
			Spec:      appsapi.DeploymentSpec{Replicas: &one},
			Status:    appsapi.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
		},
	).Build()
	env := &disruptionEnv{kube: kube}
	cycle := func(back bool) leafCycle {
		return leafCycle{name: "reboot", max: 50 * time.Millisecond, back: func(_, _ *agentapi.Agent) bool { return back }}
	}

	start := time.Now()
	at, err := env.waitAgentBack(t.Context(), "leaf-01", cycle(true), nil)
	require.NoError(t, err)
	require.False(t, at.Before(start))

	_, err = env.waitAgentBack(t.Context(), "leaf-01", cycle(false), nil)
	require.EqualError(t, err, "waiting for leaf-01 to be back from the reboot (took more than 50ms): context deadline exceeded")

	_, err = env.waitAgentBack(t.Context(), "leaf-02", cycle(true), nil)
	require.ErrorContains(t, err, "getting agent leaf-02 during its reboot: ")

	require.NoError(t, waitRollout(t.Context(), kube, kclient.ObjectKey{Name: "ctrl", Namespace: "fab"}))
	require.ErrorContains(t, waitRollout(t.Context(), kube, kclient.ObjectKey{Name: "other", Namespace: "fab"}), "getting deployment fab/other: ")
}
