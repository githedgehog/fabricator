// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	class := map[string]string{"strict": probeStrict, "hard": probeHard, "soft": probeSoft}

	for _, tc := range []struct {
		name string
		lost []lostPing
		fail string // what the one failure says, empty for none
	}{
		{name: "strict pair loses a ping", lost: lostRun("strict", base.Add(10*time.Second), 1), fail: "pings lost on pairs that don't cross leaf-02"},
		{name: "hard pair down while the leaf is away", lost: lostRun("hard", base.Add(2*time.Second), 250)},
		{name: "hard pair loses after the leaf applied", lost: lostRun("hard", base.Add(70*time.Second), 1), fail: "after leaf-02 applied its config"},
		{name: "soft pair fails over", lost: lostRun("soft", base.Add(2*time.Second), 2)},
		{name: "soft pair without replies while the leaf rejoins", lost: lostRun("soft", base.Add(30*time.Second), 100), fail: "loss on ESLAG pairs while leaf-02 rejoined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := tc.lost[0].Probe
			v := judgeLeafCycle([]probeTimeline{{Probe: probe, Samples: lostSamples(tc.lost)}}, class, "leaf-02", at)
			if tc.fail == "" {
				require.Empty(t, v.failures)

				return
			}
			require.Len(t, v.failures, 1)
			require.Contains(t, v.failures[0], tc.fail)
		})
	}
}

func lostSamples(lost []lostPing) []pingSample {
	out := make([]pingSample, 0, len(lost))
	for _, l := range lost {
		out = append(out, pingSample{Seq: l.Seq, At: l.At})
	}

	return out
}
