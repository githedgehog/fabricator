// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A full collection: etcd's exposition (with the noise and labelled series it
// really carries) followed by the node lines the sample command appends.
// etcd_debugging_server_alarms is deliberately absent, as it is while no alarm
// is armed.
const fullSampleOutput = `# HELP etcd_mvcc_db_total_size_in_bytes Total size of the underlying database.
# TYPE etcd_mvcc_db_total_size_in_bytes gauge
etcd_mvcc_db_total_size_in_bytes 2.3914676224e+10
etcd_mvcc_db_total_size_in_use_in_bytes 5.376e+08
etcd_debugging_mvcc_current_revision 651543
etcd_debugging_mvcc_compact_revision 649453
etcd_server_quota_backend_bytes 2.5769803776e+10
etcd_debugging_mvcc_keys_total 165250
etcd_mvcc_db_open_read_transactions 3
etcd_debugging_mvcc_watcher_total 95
etcd_debugging_mvcc_slow_watcher_total 2
etcd_network_client_grpc_sent_bytes_total 5.09946143e+08
etcd_mvcc_put_total 346
etcd_disk_backend_commit_duration_seconds_sum 0.81
etcd_disk_backend_commit_duration_seconds_count 127
etcd_disk_wal_fsync_duration_seconds_sum 1.5
etcd_disk_wal_fsync_duration_seconds_count 100
etcd_server_feature_enabled{name="StopGRPCServiceOnDefrag",stage="BETA"} 1
etcd_server_has_leader 1
go_goroutines 512
bench_mem_used_bytes 44023414784
bench_mem_available_bytes 21231927296
bench_rss_k3s_bytes 56482004992
bench_rss_fabric_ctrl_bytes 1742003200
bench_rss_fabric_boot_bytes 4850352128
`

func TestParseSampleFull(t *testing.T) {
	t.Parallel()

	at := time.Now()

	sample, err := parseSample(at, fullSampleOutput)
	require.NoError(t, err)

	require.Equal(t, at, sample.At)
	require.True(t, sample.HasEtcd())
	require.True(t, sample.HasMem())

	require.Equal(t, int64(23914676224), sample.DBSize())
	require.Equal(t, int64(537600000), sample.DBInUse())
	require.Equal(t, int64(651543), sample.CurrentRev())
	require.Equal(t, int64(649453), sample.CompactRev())
	require.Equal(t, int64(2090), sample.Lag())

	require.Equal(t, int64(25769803776), sample.Quota())
	require.Equal(t, int64(165250), sample.Keys())
	require.Equal(t, int64(3), sample.OpenReads())
	require.Equal(t, int64(95), sample.Watchers())
	require.Equal(t, int64(2), sample.SlowWatchers())

	require.Equal(t, int64(44023414784), sample.MemUsed())
	require.Equal(t, int64(4850352128), sample.RSSFabricBoot())

	// Absent optional metric stays absent rather than reading as zero.
	require.Zero(t, sample.Alarms())

	// sum/count become a mean: 0.81s over 127 commits.
	require.InDelta(t, 6.38, sample.CommitMean().Seconds()*1000, 0.01)
}

func TestParseSampleIgnoresLabelledSeries(t *testing.T) {
	t.Parallel()

	// etcd_server_feature_enabled{...} must not be mistaken for a declared
	// metric by prefix; exact-name matching is what prevents that.
	sample, err := parseSample(time.Now(), fullSampleOutput)
	require.NoError(t, err)
	require.NotContains(t, sample.Values, "etcd_server_feature_enabled")
}

func TestParseSampleMemoryOnly(t *testing.T) {
	t.Parallel()

	// k3s is down so curl returned nothing, which is exactly when memory is
	// most worth recording. The memory half must still be kept.
	raw := `bench_mem_used_bytes 44023414784
bench_mem_available_bytes 21231927296
bench_rss_k3s_bytes 56482004992
bench_rss_fabric_ctrl_bytes 1742003200
bench_rss_fabric_boot_bytes 4850352128
`

	sample, err := parseSample(time.Now(), raw)
	require.NoError(t, err)

	require.False(t, sample.HasEtcd())
	require.True(t, sample.HasMem())
	require.Equal(t, int64(44023414784), sample.MemUsed())
}

func TestParseSamplePartialEtcdIsDropped(t *testing.T) {
	t.Parallel()

	// A truncated scrape must not leave compact_rev at zero, which would make
	// Lag enormous. The etcd half is dropped; the memory half survives.
	raw := `etcd_mvcc_db_total_size_in_bytes 2.3914676224e+10
etcd_debugging_mvcc_current_revision 651543
bench_mem_used_bytes 100
bench_mem_available_bytes 200
bench_rss_k3s_bytes 300
bench_rss_fabric_ctrl_bytes 400
bench_rss_fabric_boot_bytes 500
`

	sample, err := parseSample(time.Now(), raw)
	require.NoError(t, err)

	require.False(t, sample.HasEtcd())
	require.Zero(t, sample.DBSize())
	require.Zero(t, sample.Lag())
	require.True(t, sample.HasMem())
}

func TestParseSampleNothingUsable(t *testing.T) {
	t.Parallel()

	_, err := parseSample(time.Now(), "curl: (7) Failed to connect\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no complete metric group")
}

// mk builds a sample from column values.
func mk(at time.Time, vals map[string]float64) Sample {
	out := Sample{At: at, Values: map[string]float64{}}
	for col, val := range vals {
		out.Values[col] = val
	}

	return out
}

// series builds samples 30s apart. Revisions advance steadily; compaction
// catches up to just behind the current revision at the listed indices, which
// is how a real series looks - compaction always trails.
//
// inUse gives live bytes per sample; nil means half the file is live, so free
// pages exist and "were they reused" is a meaningful question.
func series(sizes, inUse []int64, compactAt map[int]bool, extra map[string]float64) []Sample {
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	out := make([]Sample, 0, len(sizes))

	compact := int64(1000)

	for idx, size := range sizes {
		current := 1000 + int64(idx)*100

		if compactAt[idx] {
			compact = current - 50
		}

		live := size / 2
		if inUse != nil {
			live = inUse[idx]
		}

		vals := map[string]float64{
			"db_size":     float64(size),
			"db_in_use":   float64(live),
			"compact_rev": float64(compact),
			"current_rev": float64(current),
		}

		for col, val := range extra {
			vals[col] = val
		}

		out = append(out, mk(base.Add(time.Duration(idx)*30*time.Second), vals))
	}

	return out
}

// reading renders a series and returns the "reading" verdict lines.
func reading(t *testing.T, samples []Sample) string {
	t.Helper()

	var buf strings.Builder

	healthSeries(&buf, samples)

	out := []string{}
	keep := false

	for _, line := range strings.Split(buf.String(), "\n") {
		switch {
		case strings.Contains(line, "reading"):
			keep = true
		case keep && strings.HasPrefix(line, "                       "):
			// continuation line of the verdict
		default:
			keep = false
		}

		if keep {
			out = append(out, strings.TrimSpace(line))
		}
	}

	return strings.Join(out, " ")
}

func TestReadingRefusesWithOneCompaction(t *testing.T) {
	t.Parallel()

	// One compaction cannot distinguish anything; saying so is the right answer.
	samples := series([]int64{100, 200, 300, 400}, nil, map[int]bool{1: true}, nil)

	require.Contains(t, reading(t, samples), "too few compactions")
}

func TestReadingIngestOutpacingCompaction(t *testing.T) {
	t.Parallel()

	// The file is entirely live data: there are no free pages, so growth is
	// accumulation and reuse never enters into it. This is the 800KB run.
	sizes := []int64{100, 200, 300, 400, 500, 600, 700, 800}
	samples := series(sizes, sizes, map[int]bool{2: true, 5: true}, nil)

	stats := analyzeSeries(samples)
	require.Zero(t, stats.FreePeak)
	require.Equal(t, 0, stats.ReuseFailures)

	got := reading(t, samples)
	require.Contains(t, got, "not keeping")
	require.NotContains(t, got, "not being reused")
}

func TestReadingFreePagesNotReused(t *testing.T) {
	t.Parallel()

	// Most of the file is free the whole way and growth is small, so the free
	// space could easily have absorbed it - yet the file grew anyway. That is
	// the real reuse-failure signature, as distinct from ingest outrunning
	// compaction.
	sizes := []int64{10000, 10100, 10200, 10300, 10400, 10500, 10600, 10700}
	live := []int64{1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000}
	samples := series(sizes, live, map[int]bool{2: true, 5: true}, nil)

	stats := analyzeSeries(samples)
	require.Positive(t, stats.FreePeak)
	require.Equal(t, 2, stats.ReuseFailures)

	require.Contains(t, reading(t, samples), "not being reused")
}

func TestReadingHealthy(t *testing.T) {
	t.Parallel()

	// Free space exists and the file holds steady after each compaction, but it
	// did grow earlier in the window, so the retention reading applies.
	sizes := []int64{1000, 2000, 3000, 3000, 3000, 3000, 3000, 3000}
	samples := series(sizes, nil, map[int]bool{2: true, 5: true}, nil)

	stats := analyzeSeries(samples)
	require.Equal(t, 0, stats.ReuseFailures)

	got := reading(t, samples)
	require.Contains(t, got, "being reused")
	require.NotContains(t, got, "steady state")
}

func TestReadingSteadyState(t *testing.T) {
	t.Parallel()

	// The file never grows. Reporting "growth is the retention window" against
	// a measured growth of zero reads as a problem in the one case that is
	// unambiguously healthy.
	flat := []int64{5000, 5000, 5000, 5000, 5000, 5000}
	live := []int64{1000, 1000, 1000, 1000, 1000, 1000}
	samples := series(flat, live, map[int]bool{1: true, 3: true}, nil)

	stats := analyzeSeries(samples)
	require.Zero(t, stats.GrowthAfter)
	require.Equal(t, stats.SizeStart, stats.SizeEnd)

	got := reading(t, samples)
	require.Contains(t, got, "steady state")
	require.NotContains(t, got, "retention")
}

func TestCompactionIntervalNotReportedForOne(t *testing.T) {
	t.Parallel()

	// "every ~0s" was wrong: an interval needs two events.
	var buf strings.Builder

	healthSeries(&buf, series([]int64{100, 200, 300}, nil, map[int]bool{1: true}, nil))

	require.NotContains(t, buf.String(), "every ~0s")
	require.Contains(t, buf.String(), "too few to measure an interval")
}

func TestAnalyzeSeriesGrowsAfterCompaction(t *testing.T) {
	t.Parallel()

	// The file keeps growing through and past each compaction, which is the
	// signature of freed pages not being reused.
	samples := series([]int64{100, 200, 300, 400, 500, 600, 700, 800}, nil,
		map[int]bool{2: true, 5: true}, map[string]float64{"open_reads": 4})

	stats := analyzeSeries(samples)

	require.Equal(t, 8, stats.Samples)
	require.Equal(t, 8, stats.EtcdSamples)
	require.Equal(t, 2, stats.Compactions)
	require.Equal(t, int64(100), stats.SizeStart)
	require.Equal(t, int64(800), stats.SizeEnd)
	require.Equal(t, int64(800), stats.SizePeak)

	require.Equal(t, 2, stats.GrewAfter)
	require.Positive(t, stats.GrowthAfter)

	// The open read txns are what would explain it.
	require.Equal(t, int64(4), stats.OpenReadsMax)
}

func TestAnalyzeSeriesStableAfterCompaction(t *testing.T) {
	t.Parallel()

	// The file grows up to a compaction and then holds, which means the
	// retention window is what sets the peak and a shorter one would bound it.
	samples := series([]int64{100, 200, 300, 300, 300, 300, 300, 300}, nil,
		map[int]bool{2: true, 5: true}, nil)

	stats := analyzeSeries(samples)

	require.Equal(t, 2, stats.Compactions)
	require.Equal(t, 0, stats.GrewAfter)
	require.Zero(t, stats.GrowthAfter)
}

func TestAnalyzeSeriesQuotaAndCounters(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	// Counters only advance, so a run's total is last minus first.
	samples := []Sample{
		mk(base, map[string]float64{
			"db_size": 100, "db_in_use": 10, "compact_rev": 1, "current_rev": 2,
			"quota": 1000, "grpc_sent": 500, "puts": 10,
		}),
		mk(base.Add(time.Hour), map[string]float64{
			"db_size": 400, "db_in_use": 10, "compact_rev": 1, "current_rev": 2,
			"quota": 1000, "grpc_sent": 4100, "puts": 3610,
		}),
	}

	stats := analyzeSeries(samples)

	require.Equal(t, int64(1000), stats.Quota)
	require.InDelta(t, 40.0, stats.QuotaPct(), 0.001)
	require.Equal(t, int64(3600), stats.GRPCSent)
	require.Equal(t, int64(3600), stats.Puts)
	require.Equal(t, int64(1), stats.GRPCRate()) // 3600 bytes over 3600s
}

func TestAnalyzeSeriesLagAndInterval(t *testing.T) {
	t.Parallel()

	samples := series([]int64{100, 100, 100, 100, 100}, nil, map[int]bool{1: true, 3: true}, nil)

	stats := analyzeSeries(samples)

	require.Equal(t, 2, stats.Compactions)
	require.Equal(t, time.Minute, stats.MeanCompact)
	require.Positive(t, stats.LagMax)
	require.Equal(t, samples[len(samples)-1].Lag(), stats.LagEnd)
}

func TestAnalyzeSeriesMemoryPeaksAndBootGrowth(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	// fabric-boot climbs steadily over half an hour, which is the leak; the
	// other figures peak mid-run and come back down.
	samples := []Sample{
		mk(base, map[string]float64{
			"mem_used": 10, "mem_available": 90, "rss_k3s": 5, "rss_fabric_ctrl": 1, "rss_fabric_boot": 100,
		}),
		mk(base.Add(15*time.Minute), map[string]float64{
			"mem_used": 50, "mem_available": 50, "rss_k3s": 40, "rss_fabric_ctrl": 3, "rss_fabric_boot": 400,
		}),
		mk(base.Add(30*time.Minute), map[string]float64{
			"mem_used": 20, "mem_available": 80, "rss_k3s": 10, "rss_fabric_ctrl": 2, "rss_fabric_boot": 700,
		}),
	}

	stats := analyzeSeries(samples)

	require.Equal(t, 3, stats.MemSamples)
	require.Equal(t, 0, stats.EtcdSamples)
	require.Equal(t, int64(50), stats.MemUsedPeak)
	require.Equal(t, int64(50), stats.MemAvailMin)
	require.Equal(t, int64(40), stats.K3sPeak)

	require.Equal(t, int64(100), stats.BootStart)
	require.Equal(t, int64(700), stats.BootEnd)

	// 600 over half an hour extrapolates to 1200/h.
	require.Equal(t, int64(1200), stats.BootGrowthPerHour())
}

func TestAnalyzeSeriesEmpty(t *testing.T) {
	t.Parallel()

	stats := analyzeSeries(nil)
	require.Equal(t, 0, stats.Samples)
	require.Equal(t, 0, stats.Compactions)
	require.Zero(t, stats.BootGrowthPerHour())
	require.Zero(t, stats.QuotaPct())
}

func TestLoadSeriesMissingIsNotAnError(t *testing.T) {
	t.Parallel()

	// Health runs whether or not a sampled run has happened.
	samples, err := LoadSeries(filepath.Join(t.TempDir(), "absent.csv"))
	require.NoError(t, err)
	require.Empty(t, samples)
}

func TestSeriesRoundTrip(t *testing.T) {
	t.Parallel()

	want, err := parseSample(time.Now().Truncate(time.Second).UTC(), fullSampleOutput)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "series.csv")

	out, err := createSeries(path)
	require.NoError(t, err)
	require.NoError(t, out.Write(want.record()))
	require.NoError(t, out.Close())

	got, err := LoadSeries(path)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, want.At, got[0].At)
	require.Equal(t, want.Values, got[0].Values)

	// An absent optional metric round trips as absent, not as zero.
	require.NotContains(t, got[0].Values, "alarms")
}

func TestLoadSeriesMatchesColumnsByName(t *testing.T) {
	t.Parallel()

	// A file written by a different build has its own column order and a column
	// this build does not know. Matching on the header keeps it readable.
	raw := strings.Join([]string{
		"time,current_rev,db_size,compact_rev,db_in_use,something_new",
		"2026-09-29T12:00:00Z,500,4096,400,2048,7",
		"",
	}, "\n")

	path := filepath.Join(t.TempDir(), "old.csv")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

	samples, err := LoadSeries(path)
	require.NoError(t, err)
	require.Len(t, samples, 1)

	require.Equal(t, int64(4096), samples[0].DBSize())
	require.Equal(t, int64(100), samples[0].Lag())
	require.InDelta(t, 7.0, samples[0].Values["something_new"], 0.001)
}
