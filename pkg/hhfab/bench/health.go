// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	coreapi "k8s.io/api/core/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Runner executes a command on the control node and returns its stdout.
type Runner func(ctx context.Context, cmd string) (string, error)

// HeartbeatPeriod mirrors the switch agent's heartbeat interval. It is
// redeclared rather than imported because depguard forbids importing
// go.githedgehog.com/fabric/pkg/agent.
const HeartbeatPeriod = 15 * time.Second

// heartbeatStale is how long without a heartbeat before an agent is considered
// stale. It matches what WaitReady and inspect use.
const heartbeatStale = time.Minute

// compactionWindow is k3s's default etcd compaction interval. Every heartbeat
// writes a full Agent object and each revision is retained until compaction, so
// the live history is roughly one window's worth of writes.
const compactionWindow = 5 * time.Minute

// HealthOpts configures what Health collects.
type HealthOpts struct {
	// Stats pulls object counts, Agent sizes and fabric convergence from the
	// API. That is the expensive half: listing Agents fetches the full status
	// of every one, which at a padded 600KB is hundreds of megabytes and is
	// itself a load on the cluster being measured. Turn it off to read etcd and
	// the node without perturbing a run in flight.
	Stats bool

	// Series is the path to samples left by `bench sample`, if any. Empty skips
	// the section; a missing file is not an error.
	Series string
}

// Health prints what the control plane currently looks like: how much is in it,
// whether the fabric is keeping up, and how etcd and the node are doing.
//
// It deliberately does not diagnose. The benchmark creates load until something
// breaks; this is the view you investigate with.
//
// kube may be nil when opts.Stats is false, since nothing then touches the API.
func Health(ctx context.Context, kube kclient.Client, run Runner, w io.Writer, opts HealthOpts) error {
	if opts.Stats {
		if err := healthObjects(ctx, kube, w); err != nil {
			return err
		}

		if err := healthFabric(ctx, kube, w); err != nil {
			return err
		}
	}

	// A sampled run leaves a series behind. It is read from the local work dir,
	// so it is available even when the control node is not.
	if opts.Series != "" {
		samples, err := LoadSeries(opts.Series)
		if err != nil {
			return err
		}

		healthSeries(w, samples)
	}

	// The node sections need the control node, which is not always reachable
	// (no VLAB, ssh down). Report that rather than failing the whole command.
	if run == nil {
		fmt.Fprintf(w, "\netcd / node: skipped, no control node access\n")

		return nil
	}

	healthEtcd(ctx, run, w)
	healthNode(ctx, run, w)

	return nil
}

// healthObjects counts what is in the cluster and measures Agent size, which is
// the parameter that drives etcd write volume.
func healthObjects(ctx context.Context, kube kclient.Client, w io.Writer) error {
	fmt.Fprintf(w, "OBJECTS\n")

	counts := []struct {
		kind string
		list kclient.ObjectList
	}{
		{"Switch", &wiringapi.SwitchList{}},
		{"Server", &wiringapi.ServerList{}},
		{"Connection", &wiringapi.ConnectionList{}},
		{"VPC", &vpcapi.VPCList{}},
		{"VPCAttachment", &vpcapi.VPCAttachmentList{}},
		{"VPCPeering", &vpcapi.VPCPeeringList{}},
		{"VLANNamespace", &wiringapi.VLANNamespaceList{}},
		{"IPv4Namespace", &vpcapi.IPv4NamespaceList{}},
	}

	for _, c := range counts {
		if err := kube.List(ctx, c.list); err != nil {
			return fmt.Errorf("listing %s: %w", c.kind, err)
		}

		total, bench := 0, 0
		for _, obj := range apiutil.KubeListItems(c.list) {
			total++
			if _, ok := obj.GetLabels()[LabelFabric]; ok {
				bench++
			}
		}

		fmt.Fprintf(w, "  %-16s %6d  (%d from bench)\n", c.kind, total, bench)
	}

	// Listing every Agent with its full status is exactly what inspect does and
	// what makes it expensive; here it is the point, since object size is the
	// etcd lever.
	agents := &agentapi.AgentList{}
	start := time.Now()
	if err := kube.List(ctx, agents); err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}
	listTook := time.Since(start)

	fmt.Fprintf(w, "  %-16s %6d  (listed in %s)\n", "Agent", len(agents.Items), listTook.Truncate(time.Millisecond))

	if len(agents.Items) == 0 {
		return nil
	}

	sizes := make([]int, 0, len(agents.Items))
	total := 0
	largestName, largest := "", 0

	for idx := range agents.Items {
		raw, err := json.Marshal(&agents.Items[idx])
		if err != nil {
			return fmt.Errorf("marshalling agent %s: %w", agents.Items[idx].Name, err)
		}

		sizes = append(sizes, len(raw))
		total += len(raw)

		if len(raw) > largest {
			largest, largestName = len(raw), agents.Items[idx].Name
		}
	}

	sort.Ints(sizes)

	fmt.Fprintf(w, "\nAGENT SIZE\n")
	fmt.Fprintf(w, "  median           %s\n", humanBytes(sizes[len(sizes)/2]))
	fmt.Fprintf(w, "  largest          %s  (%s)\n", humanBytes(largest), largestName)
	fmt.Fprintf(w, "  all agents       %s\n", humanBytes(total))

	// Each heartbeat rewrites the whole object and every revision is kept until
	// compaction, so this is roughly the live history etcd carries.
	perWindow := total * int(compactionWindow/(HeartbeatPeriod))
	fmt.Fprintf(w, "  etcd history/%s  %s at one heartbeat per %s\n",
		compactionWindow, humanBytes(perWindow), HeartbeatPeriod)

	return nil
}

// healthFabric reports whether the agents are keeping up with their specs.
func healthFabric(ctx context.Context, kube kclient.Client, w io.Writer) error {
	fmt.Fprintf(w, "\nFABRIC\n")

	agents := &agentapi.AgentList{}
	if err := kube.List(ctx, agents); err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}

	var noHeartbeat, stale, notApplied int
	for idx := range agents.Items {
		agent := &agents.Items[idx]

		switch {
		case agent.Status.LastHeartbeat.IsZero():
			noHeartbeat++
		case time.Since(agent.Status.LastHeartbeat.Time) > heartbeatStale:
			stale++
		}

		if agent.Generation != agent.Status.LastAppliedGen {
			notApplied++
		}
	}

	fmt.Fprintf(w, "  %-20s %d\n", "agents", len(agents.Items))
	fmt.Fprintf(w, "  %-20s %d\n", "never heartbeated", noHeartbeat)
	fmt.Fprintf(w, "  %-20s %d\n", fmt.Sprintf("stale (>%s)", heartbeatStale), stale)
	fmt.Fprintf(w, "  %-20s %d\n", "spec not applied", notApplied)

	pods := &coreapi.PodList{}
	if err := kube.List(ctx, pods, kclient.InNamespace(fabapi.FabNamespace)); err != nil {
		return fmt.Errorf("listing pods: %w", err)
	}

	type podIssue struct {
		name     string
		restarts int32
		reason   string
	}

	issues := []podIssue{}
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.Name, "helm-install-") {
			continue
		}

		statuses := slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses)
		for _, cs := range statuses {
			if cs.RestartCount == 0 {
				continue
			}

			reason := ""
			if cs.LastTerminationState.Terminated != nil {
				reason = cs.LastTerminationState.Terminated.Reason
			}

			issues = append(issues, podIssue{name: pod.Name + "/" + cs.Name, restarts: cs.RestartCount, reason: reason})
		}
	}

	if len(issues) == 0 {
		fmt.Fprintf(w, "  %-20s none\n", "pod restarts")

		return nil
	}

	sort.Slice(issues, func(i, j int) bool { return issues[i].restarts > issues[j].restarts })
	for _, issue := range issues {
		fmt.Fprintf(w, "  %-20s %s x%d %s\n", "pod restarts", issue.name, issue.restarts, issue.reason)
	}

	return nil
}

// etcdMetrics are the series worth watching: how big the store is against its
// quota, whether an alarm has fired, and how slow the disk is getting.
var etcdMetrics = []struct {
	metric string
	label  string
	bytes  bool
}{
	{"etcd_mvcc_db_total_size_in_bytes", "db size", true},
	{"etcd_mvcc_db_total_size_in_use_in_bytes", "db in use", true},
	{"etcd_server_quota_backend_bytes", "quota", true},
	{"etcd_debugging_mvcc_current_revision", "revision", false},
	{"etcd_debugging_mvcc_compact_revision", "compacted at", false},
	// Leadership is not reported: the VLAB control plane is a single etcd
	// member, so "has leader" is always 1 and leader changes only ever count
	// restarts, which the node section shows more directly.
	{"etcd_server_slow_apply_total", "slow applies", false},
	{"etcd_server_slow_read_indexes_total", "slow reads", false},
}

// healthEtcd scrapes etcd's own metrics endpoint, which k3s exposes on
// localhost only, so it has to be read from the control node.
func healthEtcd(ctx context.Context, run Runner, w io.Writer) {
	fmt.Fprintf(w, "\nETCD\n")

	out, err := run(ctx, "curl -s --max-time 10 http://127.0.0.1:2381/metrics")
	if err != nil || strings.TrimSpace(out) == "" {
		fmt.Fprintf(w, "  unavailable on 127.0.0.1:2381 (%v)\n", err)

		return
	}

	values := parsePromMetrics(out)

	for _, m := range etcdMetrics {
		val, ok := values[m.metric]
		if !ok {
			continue
		}

		if m.bytes {
			fmt.Fprintf(w, "  %-16s %s\n", m.label, humanBytes(int(val)))
		} else {
			fmt.Fprintf(w, "  %-16s %.0f\n", m.label, val)
		}
	}

	// Allocated and live are very different numbers once a cluster has been
	// busy, and only the allocated one can trip NOSPACE: the quota is enforced
	// against the backend file, not against what is actually stored. Reporting
	// a single "usage" conflated the two and read as though the store were
	// filling up when most of the file was reclaimable free pages.
	size, haveSize := values["etcd_mvcc_db_total_size_in_bytes"]
	inUse, haveInUse := values["etcd_mvcc_db_total_size_in_use_in_bytes"]
	quota, haveQuota := values["etcd_server_quota_backend_bytes"]

	if haveQuota && quota > 0 {
		if haveSize {
			fmt.Fprintf(w, "  %-16s %.1f%% of quota (what NOSPACE is checked against)\n",
				"allocated", size/quota*100)
		}
		if haveInUse {
			fmt.Fprintf(w, "  %-16s %.1f%% of quota\n", "live data", inUse/quota*100)
		}
	}

	// The gap is fragmentation. etcd reuses these pages for new writes, so they
	// are not lost capacity - the file only grows once they run out. What a
	// defrag does is hand them back to the filesystem, which is what shrinks
	// the allocated size and so the number the quota is checked against.
	if haveSize && haveInUse && size > 0 {
		free := size - inUse
		fmt.Fprintf(w, "  %-16s %s, %.1f%% of the file (reusable by etcd; defrag returns it to the fs)\n",
			"free pages", humanBytes(int(free)), free/size*100)
	}

	// A fired alarm is what turns a full store into a read-only cluster, which
	// surfaces as write errors rather than anything obviously etcd-shaped. etcd
	// exposes no alarm metric, so reading them needs etcdctl. Report when it
	// cannot be read rather than omitting the line, since a silently absent
	// alarm status is the worst kind of missing.
	bin := findEtcdctl(ctx, run)
	if bin == "" {
		fmt.Fprintf(w, "  %-16s unknown (no etcdctl on the control node)\n", "alarms")

		return
	}

	alarms, err := etcdctl(ctx, run, bin, "alarm list")
	switch {
	case err != nil:
		fmt.Fprintf(w, "  %-16s unknown (%v)\n", "alarms", err)
	case alarms == "":
		fmt.Fprintf(w, "  %-16s none\n", "alarms")
	default:
		fmt.Fprintf(w, "  %-16s %s\n", "alarms", alarms)
	}
}

// healthNode reports whether the control VM itself is the thing running out.
func healthNode(ctx context.Context, run Runner, w io.Writer) {
	fmt.Fprintf(w, "\nNODE\n")

	if out, err := run(ctx, "uptime"); err == nil {
		fmt.Fprintf(w, "  load            %s\n", strings.TrimSpace(out))
	}

	if out, err := run(ctx, "free -m | awk '/^Mem:/{printf \"%d MB used of %d MB, %d MB available\", $3, $2, $7}'"); err == nil {
		fmt.Fprintf(w, "  memory          %s\n", strings.TrimSpace(out))
	}

	if out, err := run(ctx, "df -h /var/lib/rancher 2>/dev/null | awk 'NR==2{printf \"%s used of %s (%s)\", $3, $2, $5}'"); err == nil {
		fmt.Fprintf(w, "  disk            %s\n", strings.TrimSpace(out))
	}

	// Per-process resident memory for the things that grow with object count.
	cmd := "ps -eo rss=,comm= --sort=-rss | head -12 | awk '{printf \"%s %s\\n\", $2, $1}'"
	if out, err := run(ctx, cmd); err == nil {
		fmt.Fprintf(w, "  top processes (RSS)\n")
		for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			kb, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}

			fmt.Fprintf(w, "    %-20s %s\n", fields[0], humanBytes(kb*1024))
		}
	}
}

// parsePromMetrics pulls the value of each unlabelled sample out of Prometheus
// text format. Only simple gauges and counters are needed here.
func parsePromMetrics(body string) map[string]float64 {
	out := map[string]float64{}

	for line := range strings.SplitSeq(body, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}

		name, rest, found := strings.Cut(line, " ")
		if !found || strings.ContainsAny(name, "{") {
			continue
		}

		val, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			continue
		}

		out[name] = val
	}

	return out
}

// humanBytes formats a byte count in binary units, labelled as binary units.
// The distinction matters here: --status-size parses KB as 1000, so printing a
// 600000-byte object as "586.0 KB" reads as if the padding fell short when it
// hit its target exactly.
func humanBytes(n int) string {
	const unit = 1024

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	div, exp := int64(unit), 0
	for v := int64(n) / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
