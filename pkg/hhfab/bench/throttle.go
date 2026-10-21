// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"cmp"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ThrottleFile is where `bench sample` records the CPU throttling of the pods
// in the fab namespace, one row per pod per sample, next to SeriesFile. Pods
// come and go during a run, so they get rows of their own rather than columns.
const ThrottleFile = "bench-throttle.csv"

// throttleCmd reports every fab pod that has a CPU limit, from its pod cgroup's
// cpu.stat: the enforcement periods it ran in, how many of them it was
// throttled in, and how long it was frozen for. Pods without a limit can't be
// throttled and are left out.
//
// Pods are found through the kubelet's log directories, named namespace_pod_uid,
// rather than the API, which is what goes away during the outages worth
// measuring. The pod cgroup is the one that throttles: its quota is the sum of
// its containers', and it also holds the pause container. Guaranteed pods sit
// directly under kubepods.slice, the others under their QoS class.
//
// It never fails: a failed command would lose the whole sample.
const throttleCmd = `sudo ls /var/log/pods 2>/dev/null | awk -F_ '$1=="fab"{print $2, $3}' | while read -r pod uid; do u=$(echo "$uid" | tr - _); for d in /sys/fs/cgroup/kubepods.slice/kubepods-pod$u.slice /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/kubepods-*-pod$u.slice; do [ -r "$d/cpu.stat" ] && awk -v p="$pod" 'FNR==NR{q=$1; per=$2; next} /^nr_periods /{n=$2} /^nr_throttled /{t=$2} /^throttled_usec /{u=$2} END{if (q != "max") printf "bench_throttle %s %g %.0f %.0f %.0f\n", p, q/per, n, t, u}' "$d/cpu.max" "$d/cpu.stat"; done; done; true`

// PodCPU is one pod's CPU bandwidth counters at a sample.
type PodCPU struct {
	Pod   string
	Limit float64 // cores

	// Counters for the pod's life. Periods only counts the enforcement periods
	// the pod had something to run in, so Throttled against it is the share of
	// the time it wanted CPU and was held back.
	Periods   int64
	Throttled int64
	Frozen    time.Duration
}

// parsePodCPU picks the throttleCmd lines out of a sample's output.
func parsePodCPU(raw string) []PodCPU {
	out := make([]PodCPU, 0, strings.Count(raw, "bench_throttle "))

	for line := range strings.Lines(raw) {
		fields := strings.Fields(line)
		if len(fields) != 6 || fields[0] != "bench_throttle" {
			continue
		}

		limit, errLimit := strconv.ParseFloat(fields[2], 64)
		periods, errPeriods := strconv.ParseInt(fields[3], 10, 64)
		throttled, errThrottled := strconv.ParseInt(fields[4], 10, 64)
		usec, errUsec := strconv.ParseInt(fields[5], 10, 64)
		if errors.Join(errLimit, errPeriods, errThrottled, errUsec) != nil {
			continue
		}

		out = append(out, PodCPU{
			Pod: fields[1], Limit: limit, Periods: periods, Throttled: throttled, Frozen: time.Duration(usec) * time.Microsecond,
		})
	}

	return out
}

var throttleHeader = []string{"time", "pod", "limit", "periods", "throttled", "throttled_seconds"}

// podCPUAt is a pod's counters as read at one sample.
type podCPUAt struct {
	At time.Time
	PodCPU
}

func (p podCPUAt) record() []string {
	return []string{
		p.At.UTC().Format(time.RFC3339),
		p.Pod,
		strconv.FormatFloat(p.Limit, 'f', -1, 64),
		strconv.FormatInt(p.Periods, 10),
		strconv.FormatInt(p.Throttled, 10),
		strconv.FormatFloat(p.Frozen.Seconds(), 'f', -1, 64),
	}
}

// loadThrottle reads ThrottleFile back, matching columns by header name like
// LoadSeries. A missing file is not an error: it only exists after a sampled
// run.
func loadThrottle(path string) ([]podCPUAt, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	col := map[string]int{}
	for idx, name := range header {
		col[name] = idx
	}

	field := func(row []string, name string) string {
		if idx, ok := col[name]; ok && idx < len(row) {
			return row[idx]
		}

		return ""
	}

	var out []podCPUAt

	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A run that took the machine down can leave the last line cut off;
			// everything before it still counts.
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				continue
			}

			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		at, errAt := time.Parse(time.RFC3339, field(row, "time"))
		limit, errLimit := strconv.ParseFloat(field(row, "limit"), 64)
		periods, errPeriods := strconv.ParseInt(field(row, "periods"), 10, 64)
		throttled, errThrottled := strconv.ParseInt(field(row, "throttled"), 10, 64)
		frozen, errFrozen := strconv.ParseFloat(field(row, "throttled_seconds"), 64)
		pod := field(row, "pod")
		if pod == "" || errors.Join(errAt, errLimit, errPeriods, errThrottled, errFrozen) != nil {
			continue
		}

		// cpu.stat counts in microseconds, which the seconds column holds
		// exactly but a float product may miss by a nanosecond.
		out = append(out, podCPUAt{At: at, PodCPU: PodCPU{
			Pod: pod, Limit: limit, Periods: periods, Throttled: throttled, Frozen: time.Duration(math.Round(frozen*1e6)) * time.Microsecond,
		}})
	}

	return out, nil
}

// workloadName is the Deployment, DaemonSet or Job a pod belongs to, from its
// name: the random suffix and, for a Deployment, the pod template hash come
// off. Both are drawn from the alphabet Kubernetes generates names with, which
// has no vowels, so a word in the name is never mistaken for one.
func workloadName(pod string) string {
	base, suffix, ok := cutLast(pod, "-")
	if !ok || len(suffix) != 5 || !generatedName(suffix) {
		return pod
	}

	if owner, hash, ok := cutLast(base, "-"); ok && len(hash) >= 6 && len(hash) <= 10 && generatedName(hash) {
		return owner
	}

	return base
}

func cutLast(s, sep string) (string, string, bool) {
	idx := strings.LastIndex(s, sep)
	if idx < 0 {
		return s, "", false
	}

	return s[:idx], s[idx+len(sep):], true
}

// generatedName reports whether s only has characters from the alphabet
// Kubernetes picks generated name suffixes and template hashes from.
func generatedName(s string) bool {
	return strings.Trim(s, "bcdfghjklmnpqrstvwxz2456789") == ""
}

// workloadThrottle is one workload's throttling over a run, its pods summed:
// a restarted pod's replacement counts towards the same workload.
type workloadThrottle struct {
	Workload  string
	Limit     float64
	Periods   int64
	Throttled int64
	Frozen    time.Duration

	// The largest share of throttled periods between two samples, which is
	// where a storm shows up after the run's share has averaged it away.
	Peak float64
}

// Share is the part of the periods the workload ran in that it was throttled
// in.
func (w workloadThrottle) Share() float64 {
	if w.Periods == 0 {
		return 0
	}

	return float64(w.Throttled) / float64(w.Periods)
}

// analyzeThrottle sums each pod's counters over the run from one sample to the
// next, the same way the node counters are. A pod that only shows up after the
// first sample started during the run, so all it counted happened in it.
func analyzeThrottle(rows []podCPUAt) []workloadThrottle {
	if len(rows) == 0 {
		return nil
	}

	rows = slices.Clone(rows)
	slices.SortStableFunc(rows, func(a, b podCPUAt) int { return a.At.Compare(b.At) })

	first := rows[0].At
	prev := map[string]PodCPU{}
	byName := map[string]*workloadThrottle{}

	for _, row := range rows {
		name := workloadName(row.Pod)

		stats := byName[name]
		if stats == nil {
			stats = &workloadThrottle{Workload: name}
			byName[name] = stats
		}

		stats.Limit = max(stats.Limit, row.Limit)

		last, seen := prev[row.Pod]
		prev[row.Pod] = row.PodCPU

		if !seen && !row.At.After(first) {
			continue
		}

		periods := grown(last.Periods, row.Periods)
		throttled := grown(last.Throttled, row.Throttled)

		stats.Periods += periods
		stats.Throttled += throttled
		stats.Frozen += time.Duration(grown(int64(last.Frozen), int64(row.Frozen)))

		if periods > 0 {
			stats.Peak = max(stats.Peak, float64(throttled)/float64(periods))
		}
	}

	out := make([]workloadThrottle, 0, len(byName))
	for _, stats := range byName {
		out = append(out, *stats)
	}

	slices.SortFunc(out, func(a, b workloadThrottle) int {
		return cmp.Or(cmp.Compare(b.Frozen, a.Frozen), cmp.Compare(a.Workload, b.Workload))
	})

	return out
}

// grown is how much a counter advanced; one that went down was reset, so all
// it holds now is new.
func grown(prev, cur int64) int64 {
	if cur >= prev {
		return cur - prev
	}

	return cur
}

// healthThrottle prints the fab workloads' CPU throttling over a sampled run,
// the most frozen first.
func healthThrottle(w io.Writer, rows []podCPUAt) {
	stats := analyzeThrottle(rows)
	if len(stats) == 0 {
		return
	}

	span := rows[len(rows)-1].At.Sub(rows[0].At).Truncate(time.Second)
	fmt.Fprintf(w, "\nCPU THROTTLING (%s, fab pods with a CPU limit, over %s)\n", ThrottleFile, span)

	for _, stats := range stats {
		fmt.Fprintf(w, "  %-20s limit %g cpu, ", stats.Workload, stats.Limit)

		if stats.Throttled == 0 {
			fmt.Fprintf(w, "never throttled\n")

			continue
		}

		fmt.Fprintf(w, "throttled in %.1f%% of periods (peak %.1f%%), frozen %s\n",
			stats.Share()*100, stats.Peak*100, stats.Frozen.Truncate(time.Millisecond))
	}
}
