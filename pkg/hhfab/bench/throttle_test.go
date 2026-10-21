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
// a sample.
const throttleOutput = `bench_cpu_fabric_dhcpd_seconds 588.02
bench_throttle fabric-boot-ff568cc97-q7mzb 1 93 1 26887
bench_throttle fabric-ctrl-7fb6d7df5-6zcxt 8 2917 0 0
bench_throttle fabricator-ctrl-7c6fc8d74f-c2s8b 0.5 1418269 2289 19299078
bench_throttle zot-57949f7b78-wnkj5 1 4193990 621 47826563
bench_throttle broken 1 x 0 0
bench_io_read_fabric_boot_bytes 3557130240
`

func TestParsePodCPU(t *testing.T) {
	t.Parallel()

	pods := parsePodCPU(throttleOutput)

	require.Len(t, pods, 4, "malformed lines are skipped")
	require.Equal(t, PodCPU{
		Pod: "fabricator-ctrl-7c6fc8d74f-c2s8b", Limit: 0.5, Periods: 1418269, Throttled: 2289, Frozen: 19299078 * time.Microsecond,
	}, pods[2])
	require.Equal(t, "fabric-boot-ff568cc97-q7mzb", pods[0].Pod)
	require.InDelta(t, 8.0, pods[1].Limit, 1e-9)

	// The rest of the sample doesn't mind the extra lines.
	sample, err := parseSample(time.Now(), fullSampleOutput+throttleOutput)
	require.NoError(t, err)
	require.True(t, sample.HasEtcd())
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

// A workload's pods add up, a replacement that starts mid-run counts from
// zero, and the busiest interval is kept apart from the run's share.
func TestAnalyzeThrottle(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	at := func(step int, pod string, periods, throttled int64, frozen time.Duration) podCPUAt {
		return podCPUAt{At: base.Add(time.Duration(step) * 15 * time.Second), PodCPU: PodCPU{
			Pod: pod, Limit: 1, Periods: periods, Throttled: throttled, Frozen: frozen,
		}}
	}

	const oldDHCPD, newDHCPD, boot = "fabric-dhcpd-68bcdfb48c-ctkzz", "fabric-dhcpd-b765d958-lv7cg", "fabric-boot-ff568cc97-q7mzb"

	rows := []podCPUAt{
		at(0, oldDHCPD, 1000, 10, time.Second), // before the run: not counted
		at(0, boot, 100, 0, 0),
		at(1, oldDHCPD, 1100, 30, 3*time.Second), // 20 of 100
		at(1, boot, 200, 0, 0),
		at(2, newDHCPD, 50, 5, 500*time.Millisecond), // started during the run: 5 of 50
		at(2, boot, 300, 0, 0),
	}

	stats := analyzeThrottle(rows)
	require.Len(t, stats, 2)

	dhcpd := stats[0]
	require.Equal(t, "fabric-dhcpd", dhcpd.Workload, "the most frozen comes first")
	require.Equal(t, int64(150), dhcpd.Periods)
	require.Equal(t, int64(25), dhcpd.Throttled)
	require.Equal(t, 2500*time.Millisecond, dhcpd.Frozen)
	require.InDelta(t, 25.0/150, dhcpd.Share(), 1e-9)
	require.InDelta(t, 0.2, dhcpd.Peak, 1e-9)

	require.Equal(t, "fabric-boot", stats[1].Workload)
	require.Zero(t, stats[1].Throttled)

	var buf strings.Builder
	healthThrottle(&buf, rows)
	require.Contains(t, buf.String(), "CPU THROTTLING (bench-throttle.csv, fab pods with a CPU limit, over 30s)")
	require.Contains(t, buf.String(), "fabric-dhcpd         limit 1 cpu, throttled in 16.7% of periods (peak 20.0%), frozen 2.5s")
	require.Contains(t, buf.String(), "fabric-boot          limit 1 cpu, never throttled")

	buf.Reset()
	healthThrottle(&buf, nil)
	require.Empty(t, buf.String(), "no sampled run, no section")
}

// Rows round trip through the file; a line cut off by a run that took the
// machine down is skipped and the rest still loads.
func TestThrottleRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ThrottleFile)
	at := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)

	out, err := createCSV(path, throttleHeader)
	require.NoError(t, err)

	want := []podCPUAt{}
	for _, pod := range parsePodCPU(throttleOutput) {
		want = append(want, podCPUAt{At: at, PodCPU: pod})
		require.NoError(t, out.Write(podCPUAt{At: at, PodCPU: pod}.record()))
	}
	require.NoError(t, out.Close())

	got, err := loadThrottle(path)
	require.NoError(t, err)
	require.Equal(t, want, got)

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString("2026-10-21T20:00:15Z,zot-57949f7b78-wnkj5,1,41\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	got, err = loadThrottle(path)
	require.NoError(t, err)
	require.Len(t, got, len(want))

	missing, err := loadThrottle(filepath.Join(t.TempDir(), "absent.csv"))
	require.NoError(t, err)
	require.Empty(t, missing)
}

// Each sample's pods go to the throttle file with the sample's time; a failed
// collection is a gap row in the series and nothing in the throttle file.
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

		rows, err := loadThrottle(filepath.Join(dir, ThrottleFile))
		require.NoError(t, err)
		require.Len(t, rows, 8, "four pods in each of the two collected samples")
		require.Equal(t, samples[0].At.Truncate(time.Second), rows[0].At)
		require.Equal(t, samples[2].At.Truncate(time.Second), rows[4].At)
	})
}
