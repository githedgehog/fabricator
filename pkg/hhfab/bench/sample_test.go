// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A full collection: etcd's exposition (with the noise it really carries)
// followed by the node lines the sample command appends.
const fullSampleOutput = `# HELP etcd_mvcc_db_total_size_in_bytes Total size of the underlying database.
# TYPE etcd_mvcc_db_total_size_in_bytes gauge
etcd_mvcc_db_total_size_in_bytes 2.3914676224e+10
etcd_mvcc_db_total_size_in_use_in_bytes 5.376e+08
etcd_debugging_mvcc_current_revision 651543
etcd_debugging_mvcc_compact_revision 649453
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

	require.Equal(t, int64(23914676224), sample.DBSize)
	require.Equal(t, int64(537600000), sample.DBInUse)
	require.Equal(t, int64(651543), sample.CurrentRev)
	require.Equal(t, int64(649453), sample.CompactRev)
	require.Equal(t, int64(2090), sample.Lag())

	require.Equal(t, int64(44023414784), sample.MemUsed)
	require.Equal(t, int64(21231927296), sample.MemAvail)
	require.Equal(t, int64(56482004992), sample.RSSK3s)
	require.Equal(t, int64(1742003200), sample.RSSFabricCtrl)
	require.Equal(t, int64(4850352128), sample.RSSFabricBoot)
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
	require.Equal(t, int64(44023414784), sample.MemUsed)
}

func TestParseSamplePartialEtcdIsDropped(t *testing.T) {
	t.Parallel()

	// A truncated scrape must not leave CompactRev at zero, which would make
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
	require.Zero(t, sample.DBSize)
	require.Zero(t, sample.CurrentRev)
	require.Zero(t, sample.Lag())
	require.True(t, sample.HasMem())
}

func TestParseSampleNothingUsable(t *testing.T) {
	t.Parallel()

	_, err := parseSample(time.Now(), "curl: (7) Failed to connect\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no complete metric group")
}

// series builds samples 30s apart. Revisions advance steadily; compaction
// catches up to just behind the current revision at the listed indices, which
// is how a real series looks - compaction always trails.
func series(sizes []int64, compactAt map[int]bool) []Sample {
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	out := make([]Sample, 0, len(sizes))

	compact := int64(1000)

	for idx, size := range sizes {
		current := 1000 + int64(idx)*100

		if compactAt[idx] {
			compact = current - 50
		}

		out = append(out, Sample{
			At:         base.Add(time.Duration(idx) * 30 * time.Second),
			DBSize:     size,
			DBInUse:    1000,
			CompactRev: compact,
			CurrentRev: current,
		})
	}

	return out
}

func TestAnalyzeSeriesGrowsAfterCompaction(t *testing.T) {
	t.Parallel()

	// The file keeps growing through and past each compaction, which is the
	// signature of freed pages not being reused.
	samples := series([]int64{100, 200, 300, 400, 500, 600, 700, 800}, map[int]bool{2: true, 5: true})

	stats := analyzeSeries(samples)

	require.Equal(t, 8, stats.Samples)
	require.Equal(t, 8, stats.EtcdSamples)
	require.Equal(t, 2, stats.Compactions)
	require.Equal(t, int64(100), stats.SizeStart)
	require.Equal(t, int64(800), stats.SizeEnd)
	require.Equal(t, int64(800), stats.SizePeak)

	require.Equal(t, 2, stats.GrewAfter)
	require.Positive(t, stats.GrowthAfter)
}

func TestAnalyzeSeriesStableAfterCompaction(t *testing.T) {
	t.Parallel()

	// The file grows up to a compaction and then holds, which means the
	// retention window is what sets the peak and a shorter one would bound it.
	samples := series([]int64{100, 200, 300, 300, 300, 300, 300, 300}, map[int]bool{2: true, 5: true})

	stats := analyzeSeries(samples)

	require.Equal(t, 2, stats.Compactions)
	require.Equal(t, 0, stats.GrewAfter)
	require.Zero(t, stats.GrowthAfter)
}

func TestAnalyzeSeriesLagAndInterval(t *testing.T) {
	t.Parallel()

	samples := series([]int64{100, 100, 100, 100, 100}, map[int]bool{1: true, 3: true})

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
		{At: base, MemUsed: 10, MemAvail: 90, RSSK3s: 5, RSSFabricCtrl: 1, RSSFabricBoot: 100},
		{At: base.Add(15 * time.Minute), MemUsed: 50, MemAvail: 50, RSSK3s: 40, RSSFabricCtrl: 3, RSSFabricBoot: 400},
		{At: base.Add(30 * time.Minute), MemUsed: 20, MemAvail: 80, RSSK3s: 10, RSSFabricCtrl: 2, RSSFabricBoot: 700},
	}

	stats := analyzeSeries(samples)

	require.Equal(t, 3, stats.MemSamples)
	require.Equal(t, 0, stats.EtcdSamples)
	require.Equal(t, int64(50), stats.MemUsedPeak)
	require.Equal(t, int64(50), stats.MemAvailMin)
	require.Equal(t, int64(40), stats.K3sPeak)
	require.Equal(t, int64(3), stats.CtrlPeak)

	require.Equal(t, int64(100), stats.BootStart)
	require.Equal(t, int64(700), stats.BootEnd)
	require.Equal(t, int64(700), stats.BootPeak)

	// 600 over half an hour extrapolates to 1200/h.
	require.Equal(t, int64(1200), stats.BootGrowthPerHour())
}

func TestAnalyzeSeriesEmpty(t *testing.T) {
	t.Parallel()

	stats := analyzeSeries(nil)
	require.Equal(t, 0, stats.Samples)
	require.Equal(t, 0, stats.Compactions)
	require.Zero(t, stats.BootGrowthPerHour())
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

	// Write via the same record shape SampleTo uses.
	file, err := createSeries(path)
	require.NoError(t, err)
	require.NoError(t, file.Write(want.record()))
	file.Flush()

	got, err := LoadSeries(path)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, want, got[0])
}
