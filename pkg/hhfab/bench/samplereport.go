// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"fmt"
	"io"
	"time"
)

// postCompactWindow is how long after a compaction the file is watched to see
// whether it still grows.
const postCompactWindow = time.Minute

// seriesStats is what a sampled run says once it has been read back.
type seriesStats struct {
	Samples int
	Span    time.Duration

	EtcdSamples int
	SizeStart   int64
	SizeEnd     int64
	SizePeak    int64
	InUsePeak   int64
	Quota       int64
	KeysPeak    int64
	LagMax      int64
	LagEnd      int64
	Compactions int
	// Observed counts the compactions followed by a full postCompactWindow of
	// samples, which are the only ones the growth reading may draw on.
	Observed    int
	MeanCompact time.Duration
	// GrewAfter counts compactions after which the file still grew, and
	// GrowthAfter totals that growth. Free pages should be plentiful right
	// after a compaction, so growth there means they are not being reused.
	GrewAfter   int
	GrowthAfter int64
	// ReuseFailures counts only the compactions that grew the file *while
	// enough free space existed to absorb that growth*. Growth on its own
	// proves nothing: under steady ingest the file grows after every
	// compaction whether or not pages are reusable.
	ReuseFailures int
	// FreePeak is the most free space ever seen inside the file. When it stays
	// near zero the file is all live data, so compaction is simply not keeping
	// up with ingest and reuse never enters into it.
	FreePeak int64
	// OpenReadsMax is the evidence for why they are not being reused: bbolt
	// cannot hand back a page a live read transaction can still see.
	OpenReadsMax    int64
	WatchersMax     int64
	SlowWatchersMax int64
	AlarmsMax       int64
	// CommitMeanMax and FsyncMeanMax are the worst mean latency over any one
	// sampling interval, not the etcd-lifetime mean.
	CommitMeanMax time.Duration
	FsyncMeanMax  time.Duration
	// GRPCSent is how many bytes etcd sent to clients over the run, summed
	// across counter resets. It measures watch and read traffic rather than
	// inferring it from object size.
	GRPCSent int64
	Puts     int64

	MemSamples  int
	MemTotal    int64
	MemUsedPeak int64
	MemAvailMin int64
	K3sPeak     int64
	CtrlPeak    int64
	BootStart   int64
	BootEnd     int64
	BootPeak    int64
	BootSpan    time.Duration
}

// BootGrowthPerHour extrapolates fabric-boot's growth over the sampled span. A
// process with a bounded working set flattens; one that leaks does not.
func (s seriesStats) BootGrowthPerHour() int64 {
	if s.BootSpan <= 0 {
		return 0
	}

	return int64(float64(s.BootEnd-s.BootStart) / s.BootSpan.Hours())
}

// QuotaPct is the peak allocated size against the quota etcd itself reported.
func (s seriesStats) QuotaPct() float64 {
	if s.Quota <= 0 {
		return 0
	}

	return float64(s.SizePeak) / float64(s.Quota) * 100
}

// GRPCRate is the average bytes/s etcd sent to clients over the span.
func (s seriesStats) GRPCRate() int64 {
	if s.Span <= 0 {
		return 0
	}

	return int64(float64(s.GRPCSent) / s.Span.Seconds())
}

func analyzeSeries(samples []Sample) seriesStats {
	stats := seriesStats{Samples: len(samples)}
	if len(samples) == 0 {
		return stats
	}

	stats.Span = samples[len(samples)-1].At.Sub(samples[0].At)

	var (
		compactAt         []time.Time
		prevEtcd          *Sample
		firstMem, lastMem *Sample
	)

	for idx := range samples {
		sample := samples[idx]

		if sample.HasEtcd() {
			stats.EtcdSamples++
			accumulateEtcd(&stats, sample)

			if stats.SizeStart == 0 {
				stats.SizeStart = sample.DBSize()
			}

			stats.SizeEnd = sample.DBSize()
			stats.LagEnd = sample.Lag()

			if prevEtcd != nil {
				accumulateInterval(&stats, *prevEtcd, sample)
			}

			if prevEtcd != nil && sample.CompactRev() > prevEtcd.CompactRev() {
				compactAt = append(compactAt, sample.At)
				stats.Compactions++

				// A compaction too close to the end of the series has no full
				// follow-up window. Counting it as "did not grow" would be a
				// conclusion drawn from no evidence, and it would push the
				// reading towards "reused" or "steady state".
				growth, complete := growthAfter(samples, idx)
				if complete {
					stats.Observed++
				}

				if complete && growth > 0 {
					stats.GrewAfter++
					stats.GrowthAfter += growth

					// Only evidence of reuse failure if the free space sitting
					// in the file could have taken that growth instead.
					if sample.FreePages() > growth {
						stats.ReuseFailures++
					}
				}
			}

			prevEtcd = &samples[idx]
		}

		if sample.HasMem() {
			stats.MemSamples++
			stats.MemTotal = max(stats.MemTotal, sample.MemTotal())
			stats.MemUsedPeak = max(stats.MemUsedPeak, sample.MemUsed())
			stats.K3sPeak = max(stats.K3sPeak, sample.RSSK3s())
			stats.CtrlPeak = max(stats.CtrlPeak, sample.RSSFabricCtrl())
			stats.BootPeak = max(stats.BootPeak, sample.RSSFabricBoot())

			// The first observation seeds the minimum by count, not by treating
			// zero as unset: a node that really hit zero available is exactly
			// the reading that must not be overwritten.
			if stats.MemSamples == 1 || sample.MemAvail() < stats.MemAvailMin {
				stats.MemAvailMin = sample.MemAvail()
			}

			if firstMem == nil {
				firstMem = &samples[idx]
			}

			lastMem = &samples[idx]
		}
	}

	if firstMem != nil && lastMem != nil {
		stats.BootStart = firstMem.RSSFabricBoot()
		stats.BootEnd = lastMem.RSSFabricBoot()
		stats.BootSpan = lastMem.At.Sub(firstMem.At)
	}

	if len(compactAt) > 1 {
		stats.MeanCompact = compactAt[len(compactAt)-1].Sub(compactAt[0]) / time.Duration(len(compactAt)-1)
	}

	return stats
}

func accumulateEtcd(stats *seriesStats, sample Sample) {
	stats.SizePeak = max(stats.SizePeak, sample.DBSize())
	stats.InUsePeak = max(stats.InUsePeak, sample.DBInUse())
	stats.LagMax = max(stats.LagMax, sample.Lag())
	stats.Quota = max(stats.Quota, sample.Quota())
	stats.KeysPeak = max(stats.KeysPeak, sample.Keys())
	stats.FreePeak = max(stats.FreePeak, sample.FreePages())
	stats.OpenReadsMax = max(stats.OpenReadsMax, sample.OpenReads())
	stats.WatchersMax = max(stats.WatchersMax, sample.Watchers())
	stats.SlowWatchersMax = max(stats.SlowWatchersMax, sample.SlowWatchers())
	stats.AlarmsMax = max(stats.AlarmsMax, sample.Alarms())
}

// accumulateInterval folds in what happened between two consecutive etcd
// samples.
//
// Everything here is derived from Prometheus counters, which only advance for
// the life of the process - and resetting that process is exactly what the
// benchmark sets out to cause. So totals are summed from per-interval deltas
// rather than taken as last minus first, which after a restart comes out low
// or negative. Latency means likewise come from the interval's own sum and
// count: the ratio of the cumulative values is a lifetime average, in which
// hours of quiet history hide a slowdown during the run.
func accumulateInterval(stats *seriesStats, prev, cur Sample) {
	stats.GRPCSent += int64(counterDelta(prev, cur, "grpc_sent"))
	stats.Puts += int64(counterDelta(prev, cur, "puts"))

	stats.CommitMeanMax = max(stats.CommitMeanMax, intervalMean(prev, cur, "commit_sum", "commit_count"))
	stats.FsyncMeanMax = max(stats.FsyncMeanMax, intervalMean(prev, cur, "fsync_sum", "fsync_count"))
}

// counterDelta is how much a counter advanced between two samples. A counter
// that went down was reset by a restart, so everything it now holds happened
// since; a counter missing from either sample contributes nothing.
func counterDelta(prev, cur Sample, col string) float64 {
	if !prev.has(col) || !cur.has(col) {
		return 0
	}

	if delta := cur.val(col) - prev.val(col); delta >= 0 {
		return delta
	}

	return cur.val(col)
}

// intervalMean is a histogram's mean over one sampling interval, in seconds of
// sum per observation.
func intervalMean(prev, cur Sample, sumCol, countCol string) time.Duration {
	count := counterDelta(prev, cur, countCol)
	if count <= 0 {
		return 0
	}

	return time.Duration(counterDelta(prev, cur, sumCol) / count * float64(time.Second))
}

// growthAfter reports how much the file grew in the window following the
// compaction observed at idx, and whether the series actually covers that whole
// window.
//
// A window is complete only if etcd answered every sample inside it and at
// least one sample reached the deadline. A sample without etcd metrics in the
// middle of the window is a hole in the evidence, not a sample to skip: if the
// growth happened while etcd was unreachable, skipping it and then counting the
// window as observed would record zero growth for a window nobody saw, and
// outages are exactly what the benchmark provokes.
func growthAfter(samples []Sample, idx int) (int64, bool) {
	base := samples[idx]
	deadline := base.At.Add(postCompactWindow)
	peak := base.DBSize()

	for _, sample := range samples[idx+1:] {
		if sample.At.After(deadline) {
			// The first sample beyond the window. Every sample inside it had
			// etcd, so the window counts as observed as long as etcd was still
			// answering when it closed. This sample is outside the window, so it
			// does not contribute to the growth.
			return peak - base.DBSize(), sample.HasEtcd()
		}

		if !sample.HasEtcd() {
			return 0, false
		}

		peak = max(peak, sample.DBSize())

		if !sample.At.Before(deadline) {
			return peak - base.DBSize(), true
		}
	}

	// The series ended before the window did.
	return peak - base.DBSize(), false
}

// healthSeries prints what a sampled run showed, if there is one.
//
// etcd and the node are printed as separate sections rather than one mixed
// list, so no line's subject has to be guessed at. They also have separate
// sample counts: the two are collected independently and etcd's endpoint goes
// away during exactly the outages worth measuring.
func healthSeries(w io.Writer, samples []Sample) {
	if len(samples) == 0 {
		return
	}

	stats := analyzeSeries(samples)
	span := stats.Span.Truncate(time.Second)

	if stats.EtcdSamples > 0 {
		fmt.Fprintf(w, "\nETCD OVER TIME (%s, %d of %d samples over %s)\n",
			SeriesFile, stats.EtcdSamples, stats.Samples, span)
		reportEtcdSeries(w, stats)

		// Gaps are themselves a result: the sampler keeps running across
		// outages, so missing samples mean etcd was unreachable then.
		if stats.EtcdSamples < stats.Samples {
			fmt.Fprintf(w, "  %-20s %d samples had no etcd metrics (endpoint down)\n",
				"gaps", stats.Samples-stats.EtcdSamples)
		}
	}

	if stats.MemSamples > 0 {
		fmt.Fprintf(w, "\nNODE OVER TIME (%s, %d of %d samples over %s)\n",
			SeriesFile, stats.MemSamples, stats.Samples, span)
		reportMemSeries(w, stats)

		if stats.MemSamples < stats.Samples {
			fmt.Fprintf(w, "  %-20s %d samples had no node metrics (control node unreachable)\n",
				"gaps", stats.Samples-stats.MemSamples)
		}
	}

	// Every collection failed: without this the series would print nothing at
	// all, which reads as "no sampled run" rather than "the node was down".
	if stats.EtcdSamples == 0 && stats.MemSamples == 0 {
		fmt.Fprintf(w, "\nSAMPLED OVER TIME (%s): %d samples over %s, none collected any metrics\n",
			SeriesFile, stats.Samples, span)
	}
}

func reportEtcdSeries(w io.Writer, stats seriesStats) {
	fmt.Fprintf(w, "  %-20s %s -> %s, peak %s", "allocated",
		humanBytes(int(stats.SizeStart)), humanBytes(int(stats.SizeEnd)), humanBytes(int(stats.SizePeak)))

	// The quota comes from etcd rather than from anyone's memory of the flag.
	if stats.Quota > 0 {
		fmt.Fprintf(w, " (%.1f%% of %s quota)", stats.QuotaPct(), humanBytes(int(stats.Quota)))
	}

	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  %-20s peak %s\n", "live data", humanBytes(int(stats.InUsePeak)))

	if stats.KeysPeak > 0 {
		fmt.Fprintf(w, "  %-20s peak %d\n", "keys", stats.KeysPeak)
	}

	fmt.Fprintf(w, "  %-20s max %d, ended at %d\n", "uncompacted revs", stats.LagMax, stats.LagEnd)

	if stats.AlarmsMax > 0 {
		fmt.Fprintf(w, "  %-20s ALARM RAISED during the run (NOSPACE or CORRUPT)\n", "alarms")
	}

	reportCompaction(w, stats)

	if stats.WatchersMax > 0 {
		fmt.Fprintf(w, "  %-20s peak %d, slow peak %d\n", "watchers",
			stats.WatchersMax, stats.SlowWatchersMax)
	}

	if stats.GRPCSent > 0 {
		fmt.Fprintf(w, "  %-20s %s total, %s/s average\n", "sent to clients",
			humanBytes(int(stats.GRPCSent)), humanBytes(int(stats.GRPCRate())))
	}

	if stats.CommitMeanMax > 0 {
		fmt.Fprintf(w, "  %-20s commit %s, wal fsync %s\n", "disk (worst mean)",
			stats.CommitMeanMax.Truncate(time.Microsecond), stats.FsyncMeanMax.Truncate(time.Microsecond))
	}
}

func reportCompaction(w io.Writer, stats seriesStats) {
	if stats.Compactions == 0 {
		fmt.Fprintf(w, "  %-20s none observed in this window\n", "compactions")
		fmt.Fprintf(w, "  %-20s inconclusive, no compaction seen\n", "reading")

		return
	}

	// An interval needs two events to measure; with one it is not "every 0s".
	if stats.Compactions > 1 {
		fmt.Fprintf(w, "  %-20s %d, every ~%s\n", "compactions",
			stats.Compactions, stats.MeanCompact.Truncate(time.Second))
	} else {
		fmt.Fprintf(w, "  %-20s 1 (too few to measure an interval)\n", "compactions")
	}

	fmt.Fprintf(w, "  %-20s %d of %d grew the file, +%s total", "after compaction",
		stats.GrewAfter, stats.Observed, humanBytes(int(stats.GrowthAfter)))

	if pending := stats.Compactions - stats.Observed; pending > 0 {
		fmt.Fprintf(w, " (%d too recent to judge)", pending)
	}

	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  %-20s peak %s\n", "free pages", humanBytes(int(stats.FreePeak)))

	if stats.OpenReadsMax > 0 {
		fmt.Fprintf(w, "  %-20s peak %d\n", "open read txns", stats.OpenReadsMax)
	}

	reportReading(w, stats)
}

// freePagesFloor is how much free space the file needs before "were freed pages
// reused" is even a meaningful question.
const freePagesFloor = 20

// reportReading says what the series means, and refuses to say it when the
// evidence does not support a conclusion.
//
// Growth after a compaction is not on its own evidence that pages cannot be
// reused: under steady ingest the file grows after every compaction regardless.
// It only counts when there was free space that could have absorbed it.
func reportReading(w io.Writer, stats seriesStats) {
	// Everything below reasons from compactions whose follow-up window was
	// fully observed, so that is the count that has to be large enough.
	switch {
	case stats.Observed < 2:
		fmt.Fprintf(w, "  %-20s too few compactions to read; sample a longer run\n", "reading")

	case stats.SizeEnd <= stats.SizeStart && stats.GrowthAfter == 0:
		// Nothing to explain: the file did not grow at all. Saying growth is
		// the retention window when the measured growth is zero is worse than
		// saying nothing, and this is the healthy case so it is the one most
		// likely to be read as a problem.
		fmt.Fprintf(w, "  %-20s steady state - the file did not grow, compaction frees\n", "reading")
		fmt.Fprintf(w, "  %-20s as fast as writes arrive and free pages absorb them\n", "")

	case stats.FreePeak < stats.SizePeak/freePagesFloor:
		fmt.Fprintf(w, "  %-20s file is nearly all live data - compaction is not keeping\n", "reading")
		fmt.Fprintf(w, "  %-20s up with ingest, so retention interval is the lever\n", "")

	case stats.ReuseFailures*2 > stats.Observed:
		fmt.Fprintf(w, "  %-20s file grew while free pages were available - they are not\n", "reading")

		if stats.OpenReadsMax > 1 {
			fmt.Fprintf(w, "  %-20s being reused, with read txns open to pin them\n", "")
		} else {
			fmt.Fprintf(w, "  %-20s being reused, and not because of open read txns\n", "")
		}

	default:
		fmt.Fprintf(w, "  %-20s free pages are being reused - growth is the retention\n", "reading")
		fmt.Fprintf(w, "  %-20s window, so a shorter interval should bound it\n", "")
	}
}

func reportMemSeries(w io.Writer, stats seriesStats) {
	// System used is the honest figure: k3s's RSS counts the mmap'd etcd
	// backend, so it routinely exceeds total system usage.
	fmt.Fprintf(w, "  %-20s peak %s, min available %s", "memory used",
		humanBytes(int(stats.MemUsedPeak)), humanBytes(int(stats.MemAvailMin)))

	// Against the size of the box, which changes between runs when the VM is
	// resized and otherwise has to be remembered.
	if stats.MemTotal > 0 {
		fmt.Fprintf(w, " (of %s)", humanBytes(int(stats.MemTotal)))
	}

	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  %-20s peak %s\n", "k3s-server rss", humanBytes(int(stats.K3sPeak)))
	fmt.Fprintf(w, "  %-20s peak %s\n", "fabric-ctrl rss", humanBytes(int(stats.CtrlPeak)))

	growth := stats.BootGrowthPerHour()

	fmt.Fprintf(w, "  %-20s %s -> %s, peak %s", "fabric-boot rss",
		humanBytes(int(stats.BootStart)), humanBytes(int(stats.BootEnd)), humanBytes(int(stats.BootPeak)))

	if growth > 0 {
		fmt.Fprintf(w, " (+%s/h)", humanBytes(int(growth)))
	}

	fmt.Fprintf(w, "\n")
}
