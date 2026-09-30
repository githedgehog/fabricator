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
	CommitMeanMax   time.Duration
	FsyncMeanMax    time.Duration
	// GRPCSent is the byte counter's total advance, which measures watch and
	// read traffic rather than inferring it from object size.
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
		compactAt           []time.Time
		prevEtcd            *Sample
		firstMem, lastMem   *Sample
		firstSent, lastSent int64
		firstPuts, lastPuts int64
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

			if sample.GRPCSent() > 0 {
				if firstSent == 0 {
					firstSent = sample.GRPCSent()
				}

				lastSent = sample.GRPCSent()
			}

			if sample.Puts() > 0 {
				if firstPuts == 0 {
					firstPuts = sample.Puts()
				}

				lastPuts = sample.Puts()
			}

			if prevEtcd != nil && sample.CompactRev() > prevEtcd.CompactRev() {
				compactAt = append(compactAt, sample.At)
				stats.Compactions++

				if growth := growthAfter(samples, idx); growth > 0 {
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

			if stats.MemAvailMin == 0 || sample.MemAvail() < stats.MemAvailMin {
				stats.MemAvailMin = sample.MemAvail()
			}

			if firstMem == nil {
				firstMem = &samples[idx]
			}

			lastMem = &samples[idx]
		}
	}

	// Counters only ever advance, so the run's total is last minus first.
	stats.GRPCSent = lastSent - firstSent
	stats.Puts = lastPuts - firstPuts

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
	stats.CommitMeanMax = max(stats.CommitMeanMax, sample.CommitMean())
	stats.FsyncMeanMax = max(stats.FsyncMeanMax, sample.FsyncMean())
}

// growthAfter reports how much the file grew in the window following the
// compaction observed at idx.
func growthAfter(samples []Sample, idx int) int64 {
	base := samples[idx]
	deadline := base.At.Add(postCompactWindow)
	peak := base.DBSize()

	for _, sample := range samples[idx+1:] {
		if sample.At.After(deadline) {
			break
		}

		if sample.HasEtcd() {
			peak = max(peak, sample.DBSize())
		}
	}

	return peak - base.DBSize()
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
		fmt.Fprintf(w, "  %-20s commit %s, wal fsync %s\n", "disk mean (worst)",
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

	fmt.Fprintf(w, "  %-20s %d of %d grew the file, +%s total\n", "after compaction",
		stats.GrewAfter, stats.Compactions, humanBytes(int(stats.GrowthAfter)))
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
	switch {
	case stats.Compactions < 2:
		fmt.Fprintf(w, "  %-20s too few compactions to read; sample a longer run\n", "reading")

	case stats.FreePeak < stats.SizePeak/freePagesFloor:
		fmt.Fprintf(w, "  %-20s file is nearly all live data - compaction is not keeping\n", "reading")
		fmt.Fprintf(w, "  %-20s up with ingest, so retention interval is the lever\n", "")

	case stats.ReuseFailures*2 > stats.Compactions:
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
