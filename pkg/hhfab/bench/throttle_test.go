// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// What throttleCmd printed on a VLAB control node, between the other lines of
// a sample: zot was throttled both by its pod's limit and its container's,
// which count apart.
const throttleOutput = `bench_cpu_fabric_dhcpd_seconds 588.02
bench_throttle_pods 25
bench_throttle fabric-boot-875684dc8-858ph d32ea02a-ebac-4b66-91aa-e7c66c3bea87 pod 1 2186 2 7178
bench_throttle fabric-boot-875684dc8-858ph d32ea02a-ebac-4b66-91aa-e7c66c3bea87 1c74c3b38bb7 1 2197 0 0
bench_throttle fabric-dhcpd-6d6d46cb57-mbd4v 4d91ca8a-5d02-46c9-80fe-0d43ff2e67de pod 2 27341 0 0
bench_throttle fabric-dhcpd-6d6d46cb57-mbd4v 4d91ca8a-5d02-46c9-80fe-0d43ff2e67de dc66f8460ab2 2 27389 0 0
bench_throttle zot-57949f7b78-wnkj5 5cc44721-6a2c-4821-afb3-359325f2d4ef pod 1 4249685 626 48143614
bench_throttle zot-57949f7b78-wnkj5 5cc44721-6a2c-4821-afb3-359325f2d4ef 8611c61a55b4 1 4243615 602 18247166
bench_throttle broken uid pod 1 x 0 0
bench_throttle_running 12
bench_io_read_fabric_boot_bytes 3557130240
`

func TestParsePodCPU(t *testing.T) {
	t.Parallel()

	rows := parsePodCPU(throttleOutput)

	require.Len(t, rows, 6, "one per limited cgroup, malformed lines skipped")
	require.Equal(t, PodCPU{
		Pod: "zot-57949f7b78-wnkj5", UID: "5cc44721-6a2c-4821-afb3-359325f2d4ef", Cgroup: "8611c61a55b4", Limit: 1,
		Periods: 4243615, Throttled: 602, ThrottledTime: 18247166 * time.Microsecond,
	}, rows[5])
	require.Equal(t, cgroupPod, rows[4].Cgroup)

	// The rest of the sample doesn't mind the extra lines, and keeps how many
	// fab pods there were and how many it saw the cgroups of.
	sample, err := parseSample(time.Now(), fullSampleOutput+throttleOutput)
	require.NoError(t, err)
	require.True(t, sample.HasEtcd())
	require.Equal(t, int64(25), sample.num("throttle_pods"))
	require.Equal(t, int64(12), sample.num("throttle_running"))
}

func TestWorkloadName(t *testing.T) {
	t.Parallel()

	for pod, want := range map[string]string{
		"fabric-dhcpd-68bcdfb48c-ctkzz":            "fabric-dhcpd",
		"fabric-boot-ff568cc97-q7mzb":              "fabric-boot",
		"cert-manager-cainjector-6bb684d9b5-k9lff": "cert-manager-cainjector",
		"reloader-reloader-7b66c646d8-xwwxz":       "reloader-reloader",
		"zot-57949f7b78-wnkj5":                     "zot",
		"alloy-ctrl-vrqtj":                         "alloy-ctrl", // DaemonSet: no template hash
		"helm-install-fabric-dhcpd-lzckf":          "helm-install-fabric-dhcpd",
		"zot-0":                                    "zot-0", // not a generated suffix
		"standalone":                               "standalone",
	} {
		require.Equal(t, want, workloadName(pod), pod)
	}
}

// A pod's throttling is its cgroup's and its containers' added up against the
// periods its cgroup ran in. A container restarted within the pod is a new
// cgroup counting from zero, a replacement pod counts from zero even under the
// same name, and a pod seen only once has no data rather than no throttling.
func TestAnalyzeThrottle(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	at := func(step int, pod, uid, cgroup string, periods, throttled int64, held time.Duration) podCPUAt {
		return podCPUAt{
			At:  base.Add(time.Duration(step) * 15 * time.Second),
			Pod: pod, UID: uid, Cgroup: cgroup, Limit: 1, Periods: periods, Throttled: throttled, ThrottledTime: held,
		}
	}

	const dhcpd, db = "fabric-dhcpd-68bcdfb48c-ctkzz", "db-0"

	rows := []podCPUAt{
		at(0, dhcpd, "a", cgroupPod, 1000, 10, time.Second), // before the run: not counted
		at(0, dhcpd, "a", "c1", 1000, 5, time.Second),
		at(0, db, "d", cgroupPod, 500, 0, 0),
		at(0, "zot-57949f7b78-wnkj5", "z", cgroupPod, 500, 9, 0), // never seen again: no data

		// 100 periods, 20 throttled by the pod's limit and 5 by the container's.
		at(1, dhcpd, "a", cgroupPod, 1100, 30, 3*time.Second),
		at(1, dhcpd, "a", "c1", 1100, 10, 2*time.Second),
		at(1, db, "d", cgroupPod, 501, 1, 0), // 1 of 1: too idle for the peak

		// The container restarted: a new one, counting from zero, while the
		// pod's cgroup goes on.
		at(2, dhcpd, "a", cgroupPod, 1150, 31, 3*time.Second),
		at(2, dhcpd, "a", "c2", 40, 4, 500*time.Millisecond),
		at(2, db, "e", cgroupPod, 20, 2, 0), // recreated under the same name: counts from zero
	}

	stats := analyzeThrottle(rows)
	require.Len(t, stats, 3)

	got := stats[0]
	require.Equal(t, "fabric-dhcpd", got.Workload, "the most throttled comes first")
	require.Equal(t, int64(100+50), got.Periods)
	require.Equal(t, int64(20+5+1+4), got.Throttled)
	require.Equal(t, 2*time.Second+time.Second+500*time.Millisecond, got.ThrottledTime)
	require.InDelta(t, 25.0/100, got.Peak, 1e-9)

	byName := map[string]workloadThrottle{}
	for _, s := range stats {
		byName[s.Workload] = s
	}

	require.Equal(t, int64(1+20), byName["db-0"].Periods)
	require.Equal(t, int64(1+2), byName["db-0"].Throttled)
	require.InDelta(t, 0.1, byName["db-0"].Peak, 1e-9, "only the 20-period interval counts")
	require.Zero(t, byName["zot"].Intervals)

	var buf strings.Builder
	healthThrottle(&buf, rows, nil)
	require.Contains(t, buf.String(), "CPU THROTTLING (bench-throttle.csv, fab pods with a CPU limit, over 30s)")
	require.Contains(t, buf.String(), "fabric-dhcpd         limit 1 cpu, throttled in 20.0% of periods (peak 25.0%), 3.5s held back across its CPUs")
	require.Contains(t, buf.String(), "zot                  limit 1 cpu, no data, seen in a single sample")
}

// Without a pod limit, as when one of its containers has none, a pod's limit
// and periods are its limited containers' added up; and a pod too idle for any
// interval to judge on its own gets the run's share as its peak.
func TestAnalyzeThrottleContainerLimitsOnly(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	row := func(step int, cgroup string, limit float64, periods, throttled int64) podCPUAt {
		return podCPUAt{
			At:  base.Add(time.Duration(step) * 15 * time.Second),
			Pod: "mixed-6bb684d9b5-k9lff", UID: "m", Cgroup: cgroup, Limit: limit, Periods: periods, Throttled: throttled,
		}
	}

	stats := analyzeThrottle([]podCPUAt{
		row(0, "c1", 0.5, 0, 0), row(0, "c2", 0.25, 0, 0),
		row(1, "c1", 0.5, 4, 1), row(1, "c2", 0.25, 4, 1),
	})

	require.Len(t, stats, 1)
	require.InDelta(t, 0.75, stats[0].Limit, 1e-9)
	require.Equal(t, int64(8), stats[0].Periods)
	require.Equal(t, int64(2), stats[0].Throttled)
	require.InDelta(t, 0.25, stats[0].Peak, 1e-9, "no interval of 10 periods: the run's share")
}

// No rows says nothing by itself: the sampled series tells finding no fab pods,
// or none of their cgroups, apart from finding none with a limit, and a run
// sampled before throttling was has no section at all.
func TestHealthThrottleWithoutRows(t *testing.T) {
	t.Parallel()

	sampled := func(pods, running float64) []Sample {
		return []Sample{{At: time.Now(), Values: map[string]float64{"throttle_pods": pods, "throttle_running": running}}}
	}

	for name, tc := range map[string]struct {
		samples []Sample
		want    string
	}{
		"not sampled":      {nil, ""},
		"older series":     {[]Sample{{At: time.Now(), Values: map[string]float64{}}}, ""},
		"no pods found":    {sampled(0, 0), "not collected, no fab pods found on the control node"},
		"no cgroups found": {sampled(25, 0), "not collected, none of the 25 fab pods' cgroups found"},
		"none limited":     {sampled(25, 12), "none of the 12 running fab pods has a CPU limit"},
	} {
		var buf strings.Builder
		healthThrottle(&buf, nil, tc.samples)

		if tc.want == "" {
			require.Empty(t, buf.String(), name)
		} else {
			require.Contains(t, buf.String(), tc.want, name)
		}
	}
}

// Rows round trip through the file; a line cut off by a run that took the
// machine down is skipped and the rest still loads.
func TestThrottleRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ThrottleFile)
	at := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)

	out, err := createCSV(path, throttleHeader)
	require.NoError(t, err)

	want := []podCPUAt{} //nolint:prealloc
	for _, row := range parsePodCPU(throttleOutput) {
		want = append(want, podCPUAt{At: at, PodCPU: row})
		require.NoError(t, out.Write(podCPUAt{At: at, PodCPU: row}.record()))
	}
	require.NoError(t, out.Close())

	got, err := loadThrottle(path)
	require.NoError(t, err)
	require.Equal(t, want, got)

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString("2026-10-21T20:00:15Z,zot-57949f7b78-wnkj5,5cc4,pod,1,41\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	got, err = loadThrottle(path)
	require.NoError(t, err)
	require.Len(t, got, len(want))

	missing, err := loadThrottle(filepath.Join(t.TempDir(), "absent.csv"))
	require.NoError(t, err)
	require.Empty(t, missing)
}

// Each sample's cgroups go to the throttle file with the sample's time; a
// failed collection is a gap row in the series and nothing in the throttle
// file.
func TestSampleLoopWritesThrottle(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		series, err := createSeries(filepath.Join(dir, SeriesFile))
		require.NoError(t, err)
		throttle, err := createCSV(filepath.Join(dir, ThrottleFile), throttleHeader)
		require.NoError(t, err)

		calls := 0
		run := func(context.Context, string) (string, error) {
			calls++
			if calls == 2 {
				return "", errors.New("ssh: connection refused") //nolint:err113
			}

			return fullSampleOutput + throttleOutput, nil
		}

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
		defer cancel()

		require.NoError(t, sampleLoop(ctx, run, 15*time.Second, series, throttle, sampleCmd))
		require.NoError(t, series.Close())
		require.NoError(t, throttle.Close())

		samples, err := LoadSeries(filepath.Join(dir, SeriesFile))
		require.NoError(t, err)
		require.Len(t, samples, 3)
		require.False(t, samples[1].HasMem(), "the failed collection is a gap")
		require.Equal(t, int64(12), samples[0].num("throttle_running"))

		rows, err := loadThrottle(filepath.Join(dir, ThrottleFile))
		require.NoError(t, err)
		require.Len(t, rows, 12, "six cgroups in each of the two collected samples")
		require.Equal(t, samples[0].At.Truncate(time.Second), rows[0].At)
		require.Equal(t, samples[2].At.Truncate(time.Second), rows[6].At)
	})
}
