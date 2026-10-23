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
// in the fab namespace, a row per limited cgroup per sample, next to
// SeriesFile. Pods come and go during a run, so they get rows of their own
// rather than columns.
const ThrottleFile = "bench-throttle.csv"

// throttleCmd reports the CPU bandwidth counters of every fab pod that has a
// CPU limit: a line for the pod's cgroup and one for each container's that has
// a limit of its own, from their cpu.stat. A cgroup's counters only count the
// throttling its own limit caused, and a container's limit is enforced apart
// from the pod's, so either may be the one that throttles. Pause containers
// have no limit and are left out, and so are pods with none at any level,
// which can't be throttled. It also says how many fab pods it found and for
// how many it found a cgroup, so a collection that couldn't see them isn't
// taken for none being limited.
//
// Pods are found through the kubelet's log directories, named namespace_pod_uid,
// rather than the API, which is what goes away during the outages worth
// measuring. Guaranteed pods sit directly under kubepods.slice, the others
// under their QoS class.
//
// It never fails: a failed command would lose the whole sample.
const throttleCmd = `p=$(sudo ls /var/log/pods 2>/dev/null | awk -F_ '$1=="fab"{print $2, $3}'); echo "bench_throttle_pods $(printf '%s\n' "$p" | grep -c .)"; printf '%s\n' "$p" | while read -r pod uid; do [ -n "$uid" ] || continue; u=$(echo "$uid" | tr - _); for d in /sys/fs/cgroup/kubepods.slice/kubepods-pod$u.slice /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/kubepods-*-pod$u.slice; do [ -d "$d" ] || continue; echo bench_throttle_found; for c in "$d" "$d"/*.scope; do [ -r "$c/cpu.stat" ] || continue; case "$c" in *.scope) g=$(basename "$c" .scope | sed 's/.*-//' | cut -c1-12);; *) g=pod;; esac; awk -v p="$pod" -v id="$uid" -v g="$g" 'FNR==NR{q=$1; per=$2; next} /^nr_periods /{n=$2} /^nr_throttled /{t=$2} /^throttled_usec /{u=$2} END{if (q != "max") printf "bench_throttle %s %s %s %g %.0f %.0f %.0f\n", p, id, g, q/per, n, t, u}' "$c/cpu.max" "$c/cpu.stat"; done; done; done | awk '$1=="bench_throttle_found"{n++; next} {print} END{print "bench_throttle_running", n+0}'; true` //nolint:dupword // nested shell loops

// cgroupPod marks a pod's own cgroup; a container's is its short ID.
const cgroupPod = "pod"

// PodCPU is the CPU bandwidth counters of one of a pod's limited cgroups at a
// sample: the pod's own, or one of its containers'.
type PodCPU struct {
	Pod    string
	UID    string
	Cgroup string  // cgroupPod, or the container's short ID
	Limit  float64 // cores

	// Counters for the cgroup's life. Periods only counts the enforcement
	// periods it had something to run in, so Throttled against it is the
	// share of the time it wanted CPU and was held back. ThrottledTime is how
	// long its run queues were held back, one per CPU, added up: with a limit
	// of more than one CPU it can outrun the wall clock.
	Periods       int64
	Throttled     int64
	ThrottledTime time.Duration
}

// parsePodCPU picks the throttleCmd lines out of a sample's output.
func parsePodCPU(raw string) []PodCPU {
	out := make([]PodCPU, 0, strings.Count(raw, "bench_throttle "))

	for line := range strings.Lines(raw) {
		fields := strings.Fields(line)
		if len(fields) != 8 || fields[0] != "bench_throttle" {
			continue
		}

		limit, errLimit := strconv.ParseFloat(fields[4], 64)
		periods, errPeriods := strconv.ParseInt(fields[5], 10, 64)
		throttled, errThrottled := strconv.ParseInt(fields[6], 10, 64)
		usec, errUsec := strconv.ParseInt(fields[7], 10, 64)
		if errors.Join(errLimit, errPeriods, errThrottled, errUsec) != nil {
			continue
		}

		out = append(out, PodCPU{
			Pod: fields[1], UID: fields[2], Cgroup: fields[3], Limit: limit,
			Periods: periods, Throttled: throttled, ThrottledTime: time.Duration(usec) * time.Microsecond,
		})
	}

	return out
}

var throttleHeader = []string{"time", "pod", "uid", "cgroup", "limit", "periods", "throttled", "throttled_seconds"}

// podCPUAt is a cgroup's counters as read at one sample.
type podCPUAt struct {
	At time.Time
	PodCPU
}

func (p podCPUAt) record() []string {
	return []string{
		p.At.UTC().Format(time.RFC3339),
		p.Pod,
		p.UID,
		p.Cgroup,
		strconv.FormatFloat(p.Limit, 'f', -1, 64),
		strconv.FormatInt(p.Periods, 10),
		strconv.FormatInt(p.Throttled, 10),
		strconv.FormatFloat(p.ThrottledTime.Seconds(), 'f', -1, 64),
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
			if _, ok := errors.AsType[*csv.ParseError](err); ok {
				continue
			}

			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		at, errAt := time.Parse(time.RFC3339, field(row, "time"))
		limit, errLimit := strconv.ParseFloat(field(row, "limit"), 64)
		periods, errPeriods := strconv.ParseInt(field(row, "periods"), 10, 64)
		throttled, errThrottled := strconv.ParseInt(field(row, "throttled"), 10, 64)
		seconds, errSeconds := strconv.ParseFloat(field(row, "throttled_seconds"), 64)
		pod := field(row, "pod")
		if pod == "" || errors.Join(errAt, errLimit, errPeriods, errThrottled, errSeconds) != nil {
			continue
		}

		// cpu.stat counts in microseconds, which the seconds column holds
		// exactly but a float product may miss by a nanosecond. A file from
		// before cgroups were told apart only had the pod's.
		out = append(out, podCPUAt{
			At:  at,
			Pod: pod, UID: field(row, "uid"), Cgroup: cmp.Or(field(row, "cgroup"), cgroupPod), Limit: limit,
			Periods: periods, Throttled: throttled, ThrottledTime: time.Duration(math.Round(seconds*1e6)) * time.Microsecond,
		})
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
	before, after, ok := strings.CutLast(s, sep)
	if !ok {
		return s, "", false
	}

	return before, after, true
}

// generatedName reports whether s only has characters from the alphabet
// Kubernetes picks generated name suffixes and template hashes from.
func generatedName(s string) bool {
	return strings.Trim(s, "bcdfghjklmnpqrstvwxz2456789") == ""
}

// workloadThrottle is one workload's throttling over a run, its pods summed:
// a restarted pod's replacement counts towards the same workload.
type workloadThrottle struct {
	Workload      string
	Limit         float64
	Intervals     int
	Periods       int64
	Throttled     int64
	ThrottledTime time.Duration

	// The largest share of throttled periods between two samples, which is
	// where a storm shows up after the run's share has averaged it away.
	Peak float64
}

// throttlePeakMinPeriods is how many periods an interval needs for its share to
// count towards the peak: one throttled period of the only one a nearly idle
// pod ran in would read as a 100% peak.
const throttlePeakMinPeriods = 10

// Share is the part of the periods the workload ran in that it was throttled
// in. Where a pod's cgroup and a container's are throttled in the same period
// it is counted twice, so the share is capped at all of them.
func (w workloadThrottle) Share() float64 {
	if w.Periods == 0 {
		return 0
	}

	return min(1, float64(w.Throttled)/float64(w.Periods))
}

// podID tells pods apart: by UID, so a pod recreated under the same name is a
// new one, or by name in a file written before the UID was recorded.
func podID(p PodCPU) string {
	return cmp.Or(p.UID, p.Pod)
}

// analyzeThrottle works out each cgroup's counters from one sample to the next,
// the same way the node counters are, so a container restarted within its pod
// is a new cgroup counting from zero rather than its pod's counters dropping. A
// cgroup that only shows up after the first sample started during the run, so
// all it counted happened in it.
//
// A pod's throttled periods and time are its cgroups' added up. The periods it
// ran in are its own cgroup's when it has a limit, which every container runs
// within; without one, its limited containers' added up.
func analyzeThrottle(rows []podCPUAt) []workloadThrottle {
	if len(rows) == 0 {
		return nil
	}

	rows = slices.Clone(rows)
	slices.SortStableFunc(rows, func(a, b podCPUAt) int { return a.At.Compare(b.At) })

	first := rows[0].At

	limitedPod := map[string]bool{}
	for _, row := range rows {
		if row.Cgroup == cgroupPod {
			limitedPod[podID(row.PodCPU)] = true
		}
	}

	// A pod's share of each interval, before it goes into its workload's.
	type interval struct {
		workload         string
		podLimit, ctrSum float64
		periods, thrott  int64
		throttledTime    time.Duration
		counted          bool
	}

	type intervalKey struct {
		pod string
		at  time.Time
	}

	prev := map[string]PodCPU{}
	intervals := map[intervalKey]*interval{}
	var order []intervalKey

	for _, row := range rows {
		pod := podID(row.PodCPU)

		key := intervalKey{pod, row.At}
		iv := intervals[key]
		if iv == nil {
			iv = &interval{workload: workloadName(row.Pod)}
			intervals[key] = iv
			order = append(order, key)
		}

		if row.Cgroup == cgroupPod {
			iv.podLimit = row.Limit
		} else {
			iv.ctrSum += row.Limit
		}

		cgroup := pod + "/" + row.Cgroup
		last, seen := prev[cgroup]
		prev[cgroup] = row.PodCPU

		if !seen && !row.At.After(first) {
			continue
		}

		iv.counted = true
		iv.thrott += grown(last.Throttled, row.Throttled)
		iv.throttledTime += time.Duration(grown(int64(last.ThrottledTime), int64(row.ThrottledTime)))

		if row.Cgroup == cgroupPod || !limitedPod[pod] {
			iv.periods += grown(last.Periods, row.Periods)
		}
	}

	byName := map[string]*workloadThrottle{}
	peaked := map[string]bool{}

	for _, key := range order {
		iv := intervals[key]

		stats := byName[iv.workload]
		if stats == nil {
			stats = &workloadThrottle{Workload: iv.workload}
			byName[iv.workload] = stats
		}

		stats.Limit = max(stats.Limit, cmp.Or(iv.podLimit, iv.ctrSum))

		if !iv.counted {
			continue
		}

		stats.Intervals++
		stats.Periods += iv.periods
		stats.Throttled += iv.thrott
		stats.ThrottledTime += iv.throttledTime

		if iv.periods >= throttlePeakMinPeriods {
			stats.Peak = max(stats.Peak, min(1, float64(iv.thrott)/float64(iv.periods)))
			peaked[iv.workload] = true
		}
	}

	out := make([]workloadThrottle, 0, len(byName))
	for name, stats := range byName {
		// No interval was busy enough to judge on its own: the run's share is
		// the best there is.
		if !peaked[name] {
			stats.Peak = stats.Share()
		}

		out = append(out, *stats)
	}

	slices.SortFunc(out, func(a, b workloadThrottle) int {
		return cmp.Or(cmp.Compare(b.ThrottledTime, a.ThrottledTime), cmp.Compare(a.Workload, b.Workload))
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
// the most throttled first. samples are the series sampled with it: they say
// how many fab pods the collection found and saw the cgroups of, so a run
// where it saw none says so instead of leaving the section out.
func healthThrottle(w io.Writer, rows []podCPUAt, samples []Sample) {
	stats := analyzeThrottle(rows)

	if len(stats) == 0 {
		collected, pods, running := false, int64(0), int64(0)
		for _, sample := range samples {
			if sample.has("throttle_pods") {
				collected = true
				pods = max(pods, sample.num("throttle_pods"))
				running = max(running, sample.num("throttle_running"))
			}
		}

		switch {
		case !collected:
			// Sampled before throttling was, or no sampled run at all.
		case pods == 0:
			fmt.Fprintf(w, "\nCPU THROTTLING (%s): not collected, no fab pods found on the control node\n", ThrottleFile)
		case running == 0:
			fmt.Fprintf(w, "\nCPU THROTTLING (%s): not collected, none of the %d fab pods' cgroups found\n", ThrottleFile, pods)
		default:
			fmt.Fprintf(w, "\nCPU THROTTLING (%s): none of the %d running fab pods has a CPU limit\n", ThrottleFile, running)
		}

		return
	}

	span := rows[len(rows)-1].At.Sub(rows[0].At).Truncate(time.Second)
	fmt.Fprintf(w, "\nCPU THROTTLING (%s, fab pods with a CPU limit, over %s)\n", ThrottleFile, span)

	for _, stats := range stats {
		fmt.Fprintf(w, "  %-20s limit %g cpu, ", stats.Workload, stats.Limit)

		switch {
		case stats.Intervals == 0:
			fmt.Fprintf(w, "no data, seen in a single sample\n")
		case stats.Throttled == 0:
			fmt.Fprintf(w, "never throttled\n")
		default:
			fmt.Fprintf(w, "throttled in %.1f%% of periods (peak %.1f%%), %s held back across its CPUs\n",
				stats.Share()*100, stats.Peak*100, stats.ThrottledTime.Truncate(time.Millisecond))
		}
	}
}
