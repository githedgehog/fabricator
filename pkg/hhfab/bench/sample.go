// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// SeriesFile is where a sampling run writes, relative to the work dir. Health
// reads it from the same place so the two need no flag to agree.
const SeriesFile = "bench-sample.csv"

// DefaultSampleInterval is frequent enough to place etcd growth against
// compaction events and to catch a memory peak during a collapse, while being
// nothing next to the load being measured: one ssh round trip per interval.
const DefaultSampleInterval = 15 * time.Second

// sampleCmd collects everything in a single round trip. Output is uniform
// `name value` lines so one parser handles etcd and the node alike.
//
// etcd's metrics listener needs no certificates, unlike the client port. RSS is
// summed per command name because fabric-ctrl reports as "fabric", and values
// are converted to bytes at the source so nothing downstream has to remember a
// unit.
const sampleCmd = `curl -s --max-time 10 http://127.0.0.1:2381/metrics
free -m | awk '/^Mem:/{printf "bench_mem_used_bytes %d\nbench_mem_available_bytes %d\n", $3*1048576, $7*1048576}'
ps -eo rss=,comm= | awk '{r[$2]+=$1} END{printf "bench_rss_k3s_bytes %d\nbench_rss_fabric_ctrl_bytes %d\nbench_rss_fabric_boot_bytes %d\n", r["k3s-server"]*1024, r["fabric"]*1024, r["fabric-boot"]*1024}'`

// Sample is one observation of the control node.
//
// For etcd the distinction that matters is DBSize vs DBInUse. Compaction frees
// pages inside the file but never shrinks it; only defrag returns space to the
// filesystem. NOSPACE is checked against DBSize, so the quota is consumed by a
// high-water mark of peak demand rather than by steady-state usage, and a run
// can sit at 96% free pages while still marching toward the limit.
//
// For memory, MemUsed is the honest figure rather than RSSK3s: k3s's RSS counts
// the mmap'd etcd backend, so it routinely exceeds total system usage.
type Sample struct {
	At         time.Time
	DBSize     int64
	DBInUse    int64
	CompactRev int64
	CurrentRev int64

	MemUsed       int64
	MemAvail      int64
	RSSK3s        int64
	RSSFabricCtrl int64
	RSSFabricBoot int64
}

// Lag is how many revisions exist that compaction has not yet reclaimed. A lag
// that grows over a run means compaction is falling behind; a lag that stays
// flat while DBSize grows means the file is growing for some other reason,
// which is the interesting case.
func (s Sample) Lag() int64 {
	return s.CurrentRev - s.CompactRev
}

// HasEtcd and HasMem say whether that half of the sample was collected. They
// are recorded independently because the two fail separately: a collapsed k3s
// takes etcd's metrics endpoint with it at exactly the moment memory is most
// worth recording.
func (s Sample) HasEtcd() bool { return s.DBSize > 0 }
func (s Sample) HasMem() bool  { return s.MemUsed > 0 }

// The two groups fail independently: a collapsed k3s takes etcd's metrics
// endpoint with it at exactly the moment memory is most worth recording.
const (
	groupEtcd = "etcd"
	groupMem  = "mem"
)

const (
	metricDBSize     = "etcd_mvcc_db_total_size_in_bytes"
	metricDBInUse    = "etcd_mvcc_db_total_size_in_use_in_bytes"
	metricCompactRev = "etcd_debugging_mvcc_compact_revision"
	metricCurrentRev = "etcd_debugging_mvcc_current_revision"

	metricMemUsed  = "bench_mem_used_bytes"
	metricMemAvail = "bench_mem_available_bytes"
	metricRSSK3s   = "bench_rss_k3s_bytes"
	metricRSSCtrl  = "bench_rss_fabric_ctrl_bytes"
	metricRSSBoot  = "bench_rss_fabric_boot_bytes"
)

var sampleGroups = map[string]string{
	metricDBSize:     groupEtcd,
	metricDBInUse:    groupEtcd,
	metricCompactRev: groupEtcd,
	metricCurrentRev: groupEtcd,

	metricMemUsed:  groupMem,
	metricMemAvail: groupMem,
	metricRSSK3s:   groupMem,
	metricRSSCtrl:  groupMem,
	metricRSSBoot:  groupMem,
}

// groupSizes is how many metrics each group needs to be considered complete.
var groupSizes = func() map[string]int {
	out := map[string]int{}
	for _, group := range sampleGroups {
		out[group]++
	}

	return out
}()

// parseSample pulls the fields out of a `name value` exposition.
//
// A group is kept only if it arrived complete. A partial etcd scrape would
// otherwise leave CompactRev at zero and make Lag enormous, which is worse than
// recording nothing: derived values would be wrong rather than absent.
func parseSample(at time.Time, raw string) (Sample, error) {
	values := map[string]int64{}
	seen := map[string]int{}

	for line := range strings.Lines(raw) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		group, wanted := sampleGroups[name]
		if !wanted {
			continue
		}

		num, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			continue
		}

		values[name] = int64(num)
		seen[group]++
	}

	sample := Sample{At: at}
	kept := 0

	if seen[groupEtcd] >= groupSizes[groupEtcd] {
		kept++
		sample.DBSize = values[metricDBSize]
		sample.DBInUse = values[metricDBInUse]
		sample.CompactRev = values[metricCompactRev]
		sample.CurrentRev = values[metricCurrentRev]
	}

	if seen[groupMem] >= groupSizes[groupMem] {
		kept++
		sample.MemUsed = values[metricMemUsed]
		sample.MemAvail = values[metricMemAvail]
		sample.RSSK3s = values[metricRSSK3s]
		sample.RSSFabricCtrl = values[metricRSSCtrl]
		sample.RSSFabricBoot = values[metricRSSBoot]
	}

	if kept == 0 {
		return Sample{}, fmt.Errorf("no complete metric group in sample output") //nolint:err113
	}

	return sample, nil
}

// takeSample collects one observation from the control node.
func takeSample(ctx context.Context, run Runner) (Sample, error) {
	out, err := run(ctx, sampleCmd)
	if err != nil {
		return Sample{}, fmt.Errorf("collecting sample: %w", err)
	}

	return parseSample(time.Now(), out)
}

var seriesHeader = []string{
	"time", "db_size", "db_in_use", "compact_rev", "current_rev",
	"mem_used", "mem_available", "rss_k3s", "rss_fabric_ctrl", "rss_fabric_boot",
}

func (s Sample) record() []string {
	nums := []int64{
		s.DBSize, s.DBInUse, s.CompactRev, s.CurrentRev,
		s.MemUsed, s.MemAvail, s.RSSK3s, s.RSSFabricCtrl, s.RSSFabricBoot,
	}

	out := make([]string, 0, len(seriesHeader))
	out = append(out, s.At.UTC().Format(time.RFC3339))

	for _, num := range nums {
		out = append(out, strconv.FormatInt(num, 10))
	}

	return out
}

// seriesWriter is an open series file with its header already written.
type seriesWriter struct {
	file *os.File
	out  *csv.Writer
}

// createSeries truncates path and writes the header.
func createSeries(path string) (*seriesWriter, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating series %s: %w", path, err)
	}

	out := csv.NewWriter(file)

	if err := out.Write(seriesHeader); err != nil {
		file.Close()

		return nil, fmt.Errorf("writing series header: %w", err)
	}

	out.Flush()

	return &seriesWriter{file: file, out: out}, nil
}

func (s *seriesWriter) Write(rec []string) error {
	if err := s.out.Write(rec); err != nil {
		return fmt.Errorf("writing sample: %w", err)
	}

	return nil
}

// Flush pushes buffered records to disk. It is called after every sample so the
// series survives a run that ends by taking the machine down with it.
func (s *seriesWriter) Flush() {
	s.out.Flush()
}

func (s *seriesWriter) Close() error {
	s.out.Flush()

	if err := s.file.Close(); err != nil {
		return fmt.Errorf("closing series: %w", err)
	}

	return nil
}

// SampleTo appends samples to path until ctx is done.
//
// Failures are skipped rather than fatal: the point is to keep observing across
// the outages the benchmark is trying to cause, and a gap in the series is
// itself a measurement.
func SampleTo(ctx context.Context, run Runner, interval time.Duration, path string) error {
	if interval <= 0 {
		return nil
	}

	series, err := createSeries(path)
	if err != nil {
		return err
	}
	defer series.Close()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			sample, err := takeSample(ctx, run)
			if err != nil {
				continue
			}

			if err := series.Write(sample.record()); err != nil {
				return err
			}

			series.Flush()
		}
	}
}

// LoadSeries reads a series back. A missing file is not an error: health runs
// whether or not a sampled run has happened.
func LoadSeries(path string) ([]Sample, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening series %s: %w", path, err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading series %s: %w", path, err)
	}

	samples := make([]Sample, 0, len(rows))

	for idx, row := range rows {
		if idx == 0 || len(row) != len(seriesHeader) {
			continue
		}

		at, err := time.Parse(time.RFC3339, row[0])
		if err != nil {
			continue
		}

		nums := make([]int64, len(seriesHeader)-1)
		bad := false

		for pos := range nums {
			nums[pos], err = strconv.ParseInt(row[pos+1], 10, 64)
			if err != nil {
				bad = true

				break
			}
		}

		if bad {
			continue
		}

		samples = append(samples, Sample{
			At:     at,
			DBSize: nums[0], DBInUse: nums[1], CompactRev: nums[2], CurrentRev: nums[3],
			MemUsed: nums[4], MemAvail: nums[5],
			RSSK3s: nums[6], RSSFabricCtrl: nums[7], RSSFabricBoot: nums[8],
		})
	}

	return samples, nil
}
