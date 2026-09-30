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
free -m | awk '/^Mem:/{printf "bench_mem_total_bytes %d\nbench_mem_used_bytes %d\nbench_mem_available_bytes %d\n", $2*1048576, $3*1048576, $7*1048576}'
ps -eo rss=,comm= | awk '{r[$2]+=$1} END{printf "bench_rss_k3s_bytes %d\nbench_rss_fabric_ctrl_bytes %d\nbench_rss_fabric_boot_bytes %d\n", r["k3s-server"]*1024, r["fabric"]*1024, r["fabric-boot"]*1024}'`

// The two groups fail independently: a collapsed k3s takes etcd's metrics
// endpoint with it at exactly the moment memory is most worth recording.
const (
	groupEtcd = "etcd"
	groupMem  = "mem"
)

// metric is one column of the series.
//
// Required metrics gate whether their group is usable: a partial etcd scrape
// that left compact_rev at zero would make Lag enormous, and a wrong derived
// value is worse than an absent one. Optional metrics are recorded when present
// and left at zero otherwise - etcd_debugging_server_alarms, for instance, is
// simply not emitted while no alarm is armed, which is the healthy case.
type metric struct {
	col      string
	name     string
	group    string
	required bool
}

// seriesMetrics is the ordered set of columns. Adding an observation is one
// line here; nothing else needs to change.
var seriesMetrics = []metric{
	// What the quota is actually checked against, and what is really live.
	{"db_size", "etcd_mvcc_db_total_size_in_bytes", groupEtcd, true},
	{"db_in_use", "etcd_mvcc_db_total_size_in_use_in_bytes", groupEtcd, true},
	{"compact_rev", "etcd_debugging_mvcc_compact_revision", groupEtcd, true},
	{"current_rev", "etcd_debugging_mvcc_current_revision", groupEtcd, true},

	// Read from the server rather than assumed, so a percentage of quota is
	// never computed against a number someone remembered wrong.
	{"quota", "etcd_server_quota_backend_bytes", groupEtcd, false},
	{"alarms", "etcd_debugging_server_alarms", groupEtcd, false},

	// Live keys, which separates "more objects" from "more revisions".
	{"keys", "etcd_debugging_mvcc_keys_total", groupEtcd, false},

	// Open read transactions pin pages against reuse: bbolt cannot hand a freed
	// page back while a read txn can still see it. If the file keeps growing
	// after compaction while this sits above zero, that is the reason.
	{"open_reads", "etcd_mvcc_db_open_read_transactions", groupEtcd, false},

	// Watch health. A consumer that cannot keep up shows up as a slow watcher
	// before it gets dropped, which is the fabric-boot reconnect storm.
	{"watchers", "etcd_debugging_mvcc_watcher_total", groupEtcd, false},
	{"slow_watchers", "etcd_debugging_mvcc_slow_watcher_total", groupEtcd, false},
	{"pending_events", "etcd_debugging_mvcc_pending_events_total", groupEtcd, false},

	// Backpressure and stability.
	{"proposals_pending", "etcd_server_proposals_pending", groupEtcd, false},
	{"slow_applies", "etcd_server_slow_apply_total", groupEtcd, false},
	{"leader_changes", "etcd_server_leader_changes_seen_total", groupEtcd, false},

	// Operation and byte counters. Differenced across samples these give real
	// rates, rather than write volume inferred from agent count times object
	// size.
	{"puts", "etcd_mvcc_put_total", groupEtcd, false},
	{"ranges", "etcd_mvcc_range_total", groupEtcd, false},
	{"grpc_sent", "etcd_network_client_grpc_sent_bytes_total", groupEtcd, false},
	{"grpc_recv", "etcd_network_client_grpc_received_bytes_total", groupEtcd, false},

	// Histogram sums and counts, which are plain series; their ratio is the
	// mean, which is all we need to see disk latency move.
	{"commit_sum", "etcd_disk_backend_commit_duration_seconds_sum", groupEtcd, false},
	{"commit_count", "etcd_disk_backend_commit_duration_seconds_count", groupEtcd, false},
	{"fsync_sum", "etcd_disk_wal_fsync_duration_seconds_sum", groupEtcd, false},
	{"fsync_count", "etcd_disk_wal_fsync_duration_seconds_count", groupEtcd, false},
	{"compact_pause_sum", "etcd_debugging_mvcc_db_compaction_pause_duration_milliseconds_sum", groupEtcd, false},

	// The node. MemUsed is the honest figure rather than rss_k3s: k3s's RSS
	// counts the mmap'd etcd backend, so it routinely exceeds system usage.
	// Total is recorded so a peak is always readable against the box it ran on,
	// which changes between runs when the VM is resized.
	{"mem_total", "bench_mem_total_bytes", groupMem, false},
	{"mem_used", "bench_mem_used_bytes", groupMem, true},
	{"mem_available", "bench_mem_available_bytes", groupMem, true},
	{"rss_k3s", "bench_rss_k3s_bytes", groupMem, true},
	{"rss_fabric_ctrl", "bench_rss_fabric_ctrl_bytes", groupMem, true},
	{"rss_fabric_boot", "bench_rss_fabric_boot_bytes", groupMem, true},
}

var (
	metricsByName = func() map[string]metric {
		out := make(map[string]metric, len(seriesMetrics))
		for _, m := range seriesMetrics {
			out[m.name] = m
		}

		return out
	}()

	requiredPerGroup = func() map[string]int {
		out := map[string]int{}

		for _, m := range seriesMetrics {
			if m.required {
				out[m.group]++
			}
		}

		return out
	}()

	seriesHeader = func() []string {
		out := make([]string, 0, len(seriesMetrics)+1)
		out = append(out, "time")

		for _, m := range seriesMetrics {
			out = append(out, m.col)
		}

		return out
	}()
)

// Sample is one observation of the control node, keyed by column name.
type Sample struct {
	At     time.Time
	Values map[string]float64
}

func (s Sample) num(col string) int64   { return int64(s.Values[col]) }
func (s Sample) val(col string) float64 { return s.Values[col] }

func (s Sample) has(col string) bool {
	_, ok := s.Values[col]

	return ok
}

// Accessors for the fields the report reads, so the reporting code is not
// stringly typed throughout.
func (s Sample) DBSize() int64        { return s.num("db_size") }
func (s Sample) DBInUse() int64       { return s.num("db_in_use") }
func (s Sample) CompactRev() int64    { return s.num("compact_rev") }
func (s Sample) CurrentRev() int64    { return s.num("current_rev") }
func (s Sample) Quota() int64         { return s.num("quota") }
func (s Sample) Keys() int64          { return s.num("keys") }
func (s Sample) OpenReads() int64     { return s.num("open_reads") }
func (s Sample) Watchers() int64      { return s.num("watchers") }
func (s Sample) SlowWatchers() int64  { return s.num("slow_watchers") }
func (s Sample) Alarms() int64        { return s.num("alarms") }
func (s Sample) GRPCSent() int64      { return s.num("grpc_sent") }
func (s Sample) Puts() int64          { return s.num("puts") }
func (s Sample) MemUsed() int64       { return s.num("mem_used") }
func (s Sample) MemAvail() int64      { return s.num("mem_available") }
func (s Sample) MemTotal() int64      { return s.num("mem_total") }
func (s Sample) RSSK3s() int64        { return s.num("rss_k3s") }
func (s Sample) RSSFabricCtrl() int64 { return s.num("rss_fabric_ctrl") }
func (s Sample) RSSFabricBoot() int64 { return s.num("rss_fabric_boot") }

// CommitMean and FsyncMean turn a histogram's sum and count into the mean.
func (s Sample) CommitMean() time.Duration {
	return meanDuration(s.val("commit_sum"), s.val("commit_count"))
}
func (s Sample) FsyncMean() time.Duration {
	return meanDuration(s.val("fsync_sum"), s.val("fsync_count"))
}

func meanDuration(sum, count float64) time.Duration {
	if count <= 0 {
		return 0
	}

	return time.Duration(sum / count * float64(time.Second))
}

// Lag is how many revisions exist that compaction has not yet reclaimed. A lag
// that grows over a run means compaction is falling behind; a lag that stays
// flat while DBSize grows means the file is growing for some other reason,
// which is the interesting case.
func (s Sample) Lag() int64 {
	return s.CurrentRev() - s.CompactRev()
}

// HasEtcd and HasMem say whether that half of the sample was collected.
func (s Sample) HasEtcd() bool { return s.has("db_size") }
func (s Sample) HasMem() bool  { return s.has("mem_used") }

// parseSample pulls the declared metrics out of a `name value` exposition.
//
// A group is kept only if all of its required metrics arrived; optional ones
// are recorded when present. Labelled series are skipped, since every metric we
// declare is label-free and `name{...}` will not match an exact name.
func parseSample(at time.Time, raw string) (Sample, error) {
	found := map[string]float64{}
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

		m, wanted := metricsByName[name]
		if !wanted {
			continue
		}

		num, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			continue
		}

		found[m.col] = num

		if m.required {
			seen[m.group]++
		}
	}

	sample := Sample{At: at, Values: map[string]float64{}}
	kept := 0

	for group, need := range requiredPerGroup {
		if seen[group] < need {
			continue
		}

		kept++

		for _, m := range seriesMetrics {
			if m.group != group {
				continue
			}

			if val, ok := found[m.col]; ok {
				sample.Values[m.col] = val
			}
		}
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

// record renders a sample in column order. A metric that was absent is written
// empty rather than zero, so "not collected" and "zero" stay distinguishable.
func (s Sample) record() []string {
	out := make([]string, 0, len(seriesHeader))
	out = append(out, s.At.UTC().Format(time.RFC3339))

	for _, m := range seriesMetrics {
		val, ok := s.Values[m.col]
		if !ok {
			out = append(out, "")

			continue
		}

		out = append(out, strconv.FormatFloat(val, 'f', -1, 64))
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

// LoadSeries reads a series back, matching columns by header name so a file
// written by an older or newer build still loads. A missing file is not an
// error: health runs whether or not a sampled run has happened.
func LoadSeries(path string) ([]Sample, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening series %s: %w", path, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading series %s: %w", path, err)
	}

	if len(rows) == 0 {
		return nil, nil
	}

	header := rows[0]
	samples := make([]Sample, 0, len(rows)-1)

	for _, row := range rows[1:] {
		if len(row) == 0 {
			continue
		}

		at, err := time.Parse(time.RFC3339, row[0])
		if err != nil {
			continue
		}

		sample := Sample{At: at, Values: map[string]float64{}}

		for pos := 1; pos < len(row) && pos < len(header); pos++ {
			if row[pos] == "" {
				continue
			}

			val, err := strconv.ParseFloat(row[pos], 64)
			if err != nil {
				continue
			}

			sample.Values[header[pos]] = val
		}

		samples = append(samples, sample)
	}

	return samples, nil
}
