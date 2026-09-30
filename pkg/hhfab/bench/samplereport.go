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
	LagMax      int64
	LagEnd      int64
	Compactions int
	MeanCompact time.Duration
	// GrewAfter counts compactions after which the file still grew, and
	// GrowthAfter totals that growth. Free pages should be plentiful right
	// after a compaction, so growth there means they are not being reused -
	// fragmentation, or pages pinned by long-running reads - rather than the
	// retention window simply being too wide.
	GrewAfter   int
	GrowthAfter int64

	MemSamples  int
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

func analyzeSeries(samples []Sample) seriesStats {
	stats := seriesStats{Samples: len(samples)}
	if len(samples) == 0 {
		return stats
	}

	stats.Span = samples[len(samples)-1].At.Sub(samples[0].At)

	var (
		compactAt []time.Time
		prevEtcd  *Sample
		firstMem  *Sample
		lastMem   *Sample
	)

	for idx := range samples {
		sample := samples[idx]

		if sample.HasEtcd() {
			stats.EtcdSamples++

			if stats.SizeStart == 0 {
				stats.SizeStart = sample.DBSize
			}

			stats.SizeEnd = sample.DBSize
			stats.LagEnd = sample.Lag()
			stats.SizePeak = max(stats.SizePeak, sample.DBSize)
			stats.InUsePeak = max(stats.InUsePeak, sample.DBInUse)
			stats.LagMax = max(stats.LagMax, sample.Lag())

			if prevEtcd != nil && sample.CompactRev > prevEtcd.CompactRev {
				compactAt = append(compactAt, sample.At)
				stats.Compactions++

				if growth := growthAfter(samples, idx); growth > 0 {
					stats.GrewAfter++
					stats.GrowthAfter += growth
				}
			}

			prevEtcd = &samples[idx]
		}

		if sample.HasMem() {
			stats.MemSamples++
			stats.MemUsedPeak = max(stats.MemUsedPeak, sample.MemUsed)
			stats.K3sPeak = max(stats.K3sPeak, sample.RSSK3s)
			stats.CtrlPeak = max(stats.CtrlPeak, sample.RSSFabricCtrl)
			stats.BootPeak = max(stats.BootPeak, sample.RSSFabricBoot)

			if stats.MemAvailMin == 0 || sample.MemAvail < stats.MemAvailMin {
				stats.MemAvailMin = sample.MemAvail
			}

			if firstMem == nil {
				firstMem = &samples[idx]
			}

			lastMem = &samples[idx]
		}
	}

	if firstMem != nil && lastMem != nil {
		stats.BootStart = firstMem.RSSFabricBoot
		stats.BootEnd = lastMem.RSSFabricBoot
		stats.BootSpan = lastMem.At.Sub(firstMem.At)
	}

	if len(compactAt) > 1 {
		stats.MeanCompact = compactAt[len(compactAt)-1].Sub(compactAt[0]) / time.Duration(len(compactAt)-1)
	}

	return stats
}

// growthAfter reports how much the file grew in the window following the
// compaction observed at idx.
func growthAfter(samples []Sample, idx int) int64 {
	base := samples[idx]
	deadline := base.At.Add(postCompactWindow)
	peak := base.DBSize

	for _, sample := range samples[idx+1:] {
		if sample.At.After(deadline) {
			break
		}

		if sample.HasEtcd() {
			peak = max(peak, sample.DBSize)
		}
	}

	return peak - base.DBSize
}

// healthSeries prints what a sampled run showed, if there is one.
func healthSeries(w io.Writer, samples []Sample) {
	if len(samples) == 0 {
		return
	}

	stats := analyzeSeries(samples)

	fmt.Fprintf(w, "\nSAMPLED OVER TIME (%s, %d samples over %s)\n",
		SeriesFile, stats.Samples, stats.Span.Truncate(time.Second))

	if stats.EtcdSamples > 0 {
		reportEtcdSeries(w, stats)
	}

	if stats.MemSamples > 0 {
		reportMemSeries(w, stats)
	}

	// Gaps are themselves a result: the sampler keeps running across outages,
	// so a group missing from some samples means that subsystem was unreachable.
	if stats.EtcdSamples < stats.Samples {
		fmt.Fprintf(w, "  %-20s %d of %d samples had no etcd metrics (endpoint down)\n",
			"etcd gaps", stats.Samples-stats.EtcdSamples, stats.Samples)
	}
}

func reportEtcdSeries(w io.Writer, stats seriesStats) {
	fmt.Fprintf(w, "  %-20s %s -> %s, peak %s\n", "etcd allocated",
		humanBytes(int(stats.SizeStart)), humanBytes(int(stats.SizeEnd)), humanBytes(int(stats.SizePeak)))
	fmt.Fprintf(w, "  %-20s peak %s\n", "etcd live data", humanBytes(int(stats.InUsePeak)))
	fmt.Fprintf(w, "  %-20s max %d, ended at %d\n", "uncompacted revs", stats.LagMax, stats.LagEnd)

	if stats.Compactions == 0 {
		fmt.Fprintf(w, "  %-20s none observed in this window\n", "compactions")
		fmt.Fprintf(w, "  %-20s inconclusive, no compaction seen\n", "reading")

		return
	}

	fmt.Fprintf(w, "  %-20s %d, every ~%s\n", "compactions",
		stats.Compactions, stats.MeanCompact.Truncate(time.Second))
	fmt.Fprintf(w, "  %-20s %d of %d grew the file, +%s total\n", "after compaction",
		stats.GrewAfter, stats.Compactions, humanBytes(int(stats.GrowthAfter)))

	// The whole reason for sampling: separate "retention window too wide" from
	// "freed pages are not being reused".
	if stats.GrewAfter*2 > stats.Compactions {
		fmt.Fprintf(w, "  %-20s file grows after most compactions - freed pages are not\n", "reading")
		fmt.Fprintf(w, "  %-20s being reused (fragmentation, or pages pinned by long reads)\n", "")
	} else {
		fmt.Fprintf(w, "  %-20s file is stable after compaction - growth is the retention\n", "reading")
		fmt.Fprintf(w, "  %-20s window, so a shorter interval should bound it\n", "")
	}
}

func reportMemSeries(w io.Writer, stats seriesStats) {
	// System used is the honest figure: k3s's RSS counts the mmap'd etcd
	// backend, so it routinely exceeds total system usage.
	fmt.Fprintf(w, "  %-20s peak %s, min available %s\n", "memory used",
		humanBytes(int(stats.MemUsedPeak)), humanBytes(int(stats.MemAvailMin)))
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
