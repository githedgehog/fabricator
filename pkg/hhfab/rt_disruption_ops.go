// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	appsapi "k8s.io/api/apps/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// summarizeProbeLoss says per probe how many pings were lost and between when, one line each, in
// the order the probes first lost one. It is empty without loss.
func summarizeProbeLoss(lost []lostPing, class map[string]string) string {
	type span struct {
		count       int
		first, last time.Time
	}
	spans := map[string]*span{}
	var order []string
	for _, l := range lost {
		s, ok := spans[l.Probe]
		if !ok {
			s = &span{}
			spans[l.Probe] = s
			order = append(order, l.Probe)
		}
		s.count++
		if l.At.IsZero() {
			continue
		}
		if s.first.IsZero() || l.At.Before(s.first) {
			s.first = l.At
		}
		if l.At.After(s.last) {
			s.last = l.At
		}
	}

	var b strings.Builder
	for _, probe := range order {
		s := spans[probe]
		fmt.Fprintf(&b, "%s (%s): %d pings lost", probe, class[probe], s.count)
		if !s.first.IsZero() {
			fmt.Fprintf(&b, ", %s to %s", s.first.Format("15:04:05.000"), s.last.Format("15:04:05.000"))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func summarizeLoss(lost []lostPing) string {
	return summarizeWindows(groupLoss(lost, 3*trafficPingInterval))
}

// checkResult is what one operation is judged on.
type checkResult struct {
	op       string
	started  time.Time
	failures []string
	files    map[string]string
}

func (env *disruptionEnv) specsAfterSettle(ctx context.Context) (agentSpecs, []agentapi.Agent, error) {
	agents, err := waitAgentsSettled(ctx, env.kube, disruptionSettleQuiet, disruptionSettleMax)
	if err != nil {
		return nil, nil, err
	}
	specs, err := snapshotAgentSpecs(ctx, env.kube)
	if err != nil {
		return nil, nil, err
	}

	return specs, agents, nil
}

// noteOnce puts notes in the test result, each one once however many operations repeat it.
func (env *disruptionEnv) noteOnce(notes []string) {
	for _, n := range notes {
		if !env.noted[n] {
			env.noted[n] = true
			env.testCtx.addNote(n)
		}
	}
}

// writeEvidence saves files in the evidence directory, each one's name starting with prefix. Empty
// files are left out: a file that says nothing only gets in the way.
func (env *disruptionEnv) writeEvidence(prefix string, files map[string]string) {
	if err := os.MkdirAll(env.evidenceDir, 0o755); err != nil {
		slog.Warn("Creating evidence directory failed", "dir", env.evidenceDir, "err", err)

		return
	}
	for name, content := range files {
		if content == "" {
			continue
		}
		if prefix != "" {
			name = sanitizeNameForFolder(prefix) + "-" + name
		}
		file := filepath.Join(env.evidenceDir, name)
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil { //nolint:gosec
			slog.Warn("Writing evidence failed", "file", file, "err", err)
		}
	}
}

// fail records the evidence of a failed operation and returns the error to report.
func (env *disruptionEnv) fail(res *checkResult) error {
	env.writeEvidence(res.op, res.files)

	return fmt.Errorf("%s: %s (evidence in %s)", res.op, strings.Join(res.failures, "; "), env.evidenceDir) //nolint:err113
}

// roundReport collects what the rounds of one operation found. A round whose only finding is a
// change in Agent specs is recorded and the next round runs, so that a change that repeats shows
// whether it ever costs traffic. Lost pings and Agents that did not apply end the operation.
type roundReport struct {
	total   int // rounds the operation has
	ran     int
	specs   []string // per round with changed Agent specs: the round and which Agents
	losses  []string // per round with lost pings: the round and the loss windows
	pending []string // per round with Agents that did not apply: the round and which
	// first is the first round with changed Agent specs and what changed in it
	first string
}

func (r *roundReport) failed() bool {
	return len(r.specs)+len(r.losses)+len(r.pending) > 0
}

// stop says whether the operation ends here: lost pings and Agents that did not apply do, spec changes alone don't.
func (r *roundReport) stop() bool {
	return len(r.losses)+len(r.pending) > 0
}

// summary says what the rounds found, spec changes and loss apart.
func (r *roundReport) summary() string {
	var parts []string
	if len(r.specs) > 0 {
		parts = append(parts, fmt.Sprintf("Agent specs changed in %d of %d rounds (%s); first: %s", len(r.specs), r.ran, strings.Join(r.specs, "; "), r.first))
	}
	if len(r.losses) > 0 {
		parts = append(parts, fmt.Sprintf("pings lost in %d of %d rounds: %s", len(r.losses), r.ran, strings.Join(r.losses, "; ")))
	} else {
		parts = append(parts, fmt.Sprintf("no ping lost in any of the %d rounds", r.ran))
	}
	if len(r.pending) > 0 {
		parts = append(parts, fmt.Sprintf("Agents not applied in %d of %d rounds: %s", len(r.pending), r.ran, strings.Join(r.pending, "; ")))
	}
	if r.ran < r.total {
		parts = append(parts, fmt.Sprintf("stopped after %d of %d rounds", r.ran, r.total))
	}

	return strings.Join(parts, "; ")
}

// finish returns the operation's error: what the rounds found, prefixed by err when a round
// could not run.
func (env *disruptionEnv) finish(rep *roundReport, err error) error {
	if !rep.failed() {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w; before that: %s (evidence in %s)", err, rep.summary(), env.evidenceDir)
	}

	return fmt.Errorf("%s (evidence in %s)", rep.summary(), env.evidenceDir) //nolint:err113
}

// summarizeSpecDiffs counts the changes per Agent: "leaf-01 3, leaf-02 3". Each change reads "<agent>: <path>: <old> -> <new>".
func summarizeSpecDiffs(diffs []string) string {
	counts := map[string]int{}
	for _, d := range diffs {
		agent, _, _ := strings.Cut(d, ": ")
		counts[agent]++
	}
	parts := make([]string, 0, len(counts))
	for _, agent := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", agent, counts[agent]))
	}

	return strings.Join(parts, ", ")
}

// runUnrelated runs one round of an operation that must not disturb anything: no lost ping, no
// Agent spec change other than the touch marker, and every Agent applied. Findings go to rep;
// the returned error only means the round could not run.
func (env *disruptionEnv) runUnrelated(ctx context.Context, rep *roundReport, op string, do func(context.Context) error) error {
	before, beforeAgents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	started := time.Now()
	if err := do(ctx); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	after, agents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if err := sleepCtx(ctx, disruptionTrailGrace); err != nil {
		return err
	}
	end := time.Now()

	tl, err := env.tw.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if stale := staleProbes(tl, end.Add(-disruptionTrailGrace)); len(stale) > 0 {
		return fmt.Errorf("%w: %s: probes stopped recording, the traffic can't be judged: %s", errInconclusive, op, strings.Join(stale, ", "))
	}
	lost := lostPings(tl, env.cursor, end)
	env.cursor = end

	rep.ran++
	files := map[string]string{}
	if diffs := diffAgentSpecs(before, after, ignoreBenchTouch); len(diffs) > 0 {
		rep.specs = append(rep.specs, fmt.Sprintf("%s: %s", op, summarizeSpecDiffs(diffs)))
		if rep.first == "" {
			rep.first = fmt.Sprintf("%s: %s", op, strings.Join(diffs, ", "))
		}
		files["spec-diff.txt"] = strings.Join(diffs, "\n") + "\n"
	}
	if len(lost) > 0 {
		rep.losses = append(rep.losses, fmt.Sprintf("%s: %d pings lost: %s", op, len(lost), summarizeLoss(lost)))
		files["lost-pings.txt"] = formatLostPings(lost)
	}
	pending, notes := agentsNotApplied(agents, agentGenerations(beforeAgents))
	env.noteOnce(notes)
	if len(pending) > 0 {
		rep.pending = append(rep.pending, fmt.Sprintf("%s: %s", op, strings.Join(pending, ", ")))
	}
	if len(files) > 0 || len(pending) > 0 {
		files["agent-timeline.txt"] = formatAgentRows(env.aw.Rows(started, end))
		env.writeEvidence(op, files)
	}

	return nil
}

// opCreateDeleteVPC creates and deletes a VPC that is attached nowhere, disruptionVPCCycles times.
// Nothing that carries traffic changes, so nothing may be lost and no Agent spec may change.
// Targets fabric#1649: the reconcile renumbers the IRB VLANs of the entries that only one leaf of
// the pair holds.
func (env *disruptionEnv) opCreateDeleteVPC(ctx context.Context) error {
	vpc, err := env.newVPC("dt-un")
	if err != nil {
		return err
	}
	// a round that stops the operation between the create and the delete must not leave the VPC behind
	defer func() {
		if err := kclient.IgnoreNotFound(env.kube.Delete(context.WithoutCancel(ctx), vpc.DeepCopy())); err != nil {
			slog.Warn("Removing the VPC of the create and delete rounds failed", "vpc", vpc.Name, "err", err)
		}
	}()

	rep := &roundReport{total: 2 * disruptionVPCCycles}
	for cycle := 1; cycle <= disruptionVPCCycles && !rep.stop(); cycle++ {
		if err := env.runUnrelated(ctx, rep, fmt.Sprintf("create-vpc-%d", cycle), func(ctx context.Context) error {
			_, err := CreateOrUpdateVpc(ctx, env.kube, vpc.DeepCopy())

			return err
		}); err != nil {
			return env.finish(rep, err)
		}
		if rep.stop() {
			break
		}
		if err := env.runUnrelated(ctx, rep, fmt.Sprintf("delete-vpc-%d", cycle), func(ctx context.Context) error {
			if err := env.kube.Delete(ctx, vpc.DeepCopy()); err != nil {
				return fmt.Errorf("deleting VPC: %w", err)
			}

			return nil
		}); err != nil {
			return env.finish(rep, err)
		}
	}

	return env.finish(rep, nil)
}

// opTouchLeaves puts the benchmark touch label on the leaves of the pair in turn,
// disruptionTouchRounds times. The label reaches no config, so nothing may be lost and no Agent
// spec may change but the touch marker. Targets fabric#1649: any reconcile of one leaf renumbers
// the IRB VLANs of the entries attached to that leaf alone.
func (env *disruptionEnv) opTouchLeaves(ctx context.Context) error {
	rep := &roundReport{total: disruptionTouchRounds}
	for round := 1; round <= disruptionTouchRounds && !rep.stop(); round++ {
		leaf := env.sc.Leaves[(round-1)%2]
		if err := env.runUnrelated(ctx, rep, fmt.Sprintf("touch-%d-%s", round, leaf), func(ctx context.Context) error {
			sw := &wiringapi.Switch{}
			if err := env.kube.Get(ctx, kclient.ObjectKey{Name: leaf, Namespace: kmetav1.NamespaceDefault}, sw); err != nil {
				return fmt.Errorf("getting switch %s: %w", leaf, err)
			}
			patch := kclient.MergeFrom(sw.DeepCopy())
			if sw.Labels == nil {
				sw.Labels = map[string]string{}
			}
			sw.Labels[disruptionTouchLabel] = strconv.FormatInt(time.Now().UnixNano(), 10)
			env.touched[leaf] = true
			if err := env.kube.Patch(ctx, sw, patch); err != nil {
				return fmt.Errorf("labelling switch %s: %w", leaf, err)
			}

			return nil
		}); err != nil {
			return env.finish(rep, fmt.Errorf("round %d of %d: %w", round, disruptionTouchRounds, err))
		}
	}

	return env.finish(rep, nil)
}

// opRestartController restarts fabric-ctrl the way kubectl rollout restart does,
// disruptionCtrlRestarts times. A new controller reconciles everything again and has to arrive at
// what was there. Targets fabric#1649: the first reconcile of a new controller renumbers the IRB
// VLANs of the entries attached to one leaf alone.
func (env *disruptionEnv) opRestartController(ctx context.Context) error {
	kubeconfig := filepath.Join(env.testCtx.vlabCfg.WorkDir, VLABDir, VLABKubeConfig)
	kc, err := kubeutil.NewClient(ctx, kubeconfig, appsapi.AddToScheme)
	if err != nil {
		return fmt.Errorf("creating kube client for deployments: %w", err)
	}
	key := kclient.ObjectKey{Namespace: comp.FabNamespace, Name: "fabric-ctrl"}

	rep := &roundReport{total: disruptionCtrlRestarts}
	for n := 1; n <= disruptionCtrlRestarts && !rep.stop(); n++ {
		if err := env.runUnrelated(ctx, rep, fmt.Sprintf("restart-fabric-ctrl-%d", n), func(ctx context.Context) error {
			dep := &appsapi.Deployment{}
			if err := kc.Get(ctx, key, dep); err != nil {
				return fmt.Errorf("getting deployment %s: %w", key, err)
			}
			patch := kclient.MergeFrom(dep.DeepCopy())
			if dep.Spec.Template.Annotations == nil {
				dep.Spec.Template.Annotations = map[string]string{}
			}
			dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339Nano)
			if err := kc.Patch(ctx, dep, patch); err != nil {
				return fmt.Errorf("restarting deployment %s: %w", key, err)
			}

			return waitRollout(ctx, kc, key)
		}); err != nil {
			return env.finish(rep, err)
		}
	}

	return env.finish(rep, nil)
}

func waitRollout(ctx context.Context, kc kclient.Client, key kclient.ObjectKey) error {
	var getErr error
	err := wait.PollUntilContextTimeout(ctx, disruptionPollInterval, disruptionRolloutMax, true, func(ctx context.Context) (bool, error) {
		dep := &appsapi.Deployment{}
		if err := kc.Get(ctx, key, dep); err != nil {
			getErr = fmt.Errorf("getting deployment %s: %w", key, err)

			return false, getErr
		}
		want := int32(1)
		if dep.Spec.Replicas != nil {
			want = *dep.Spec.Replicas
		}
		st := dep.Status

		return st.ObservedGeneration >= dep.Generation && st.UpdatedReplicas == want && st.Replicas == want && st.AvailableReplicas == want, nil
	})
	if getErr != nil {
		return getErr
	}
	if err != nil {
		return fmt.Errorf("waiting for rollout of %s: %w", key, err)
	}

	return nil
}

// leafCycle is an operation that takes the second leaf of the pair away through its Agent and
// waits for it to come back: a reinstall or a reboot. Only on hardware for now.
type leafCycle struct {
	name string // as in the notes and the logs
	// specField is the Agent spec field the request sets, it is the one spec change expected
	specField string
	// max is how long the leaf may take to come back
	max time.Duration
	// request asks the Agent for the cycle and returns what it logs about it; it fails when the
	// Agent can't do it
	request func(ag *agentapi.Agent) (string, error)
	// back says whether the Agent has come back, given the Agent as it was before the request
	back func(before, now *agentapi.Agent) bool
}

// opReinstall reinstalls the second leaf of the pair through its Agent, on hardware only. Targets
// fabric#1663, the Agent Applied condition that stays ApplyPending after a reinstall, and
// fabric#1488, the ESLAG dual-homed servers that lose traffic while the leaf comes back.
func (env *disruptionEnv) opReinstall(ctx context.Context) error {
	return env.opLeafCycle(ctx, leafCycle{
		name:      "reinstall",
		specField: "reinstall",
		max:       disruptionReinstallMax,
		request: func(ag *agentapi.Agent) (string, error) {
			if ag.Status.InstallID == "" {
				return "", fmt.Errorf("agent %s is not installed (missing .status.installID)", ag.Name) //nolint:err113
			}
			ag.Spec.Reinstall = ag.Status.InstallID

			return "installID " + ag.Status.InstallID, nil
		},
		// a new install ID and a heartbeat newer than the one before (both stamped by the switch,
		// so no comparison with the runner clock) and the current generation applied
		back: func(before, now *agentapi.Agent) bool {
			return now.Status.InstallID != "" && now.Status.InstallID != before.Status.InstallID &&
				now.Status.LastHeartbeat.Time.After(before.Status.LastHeartbeat.Time) && now.Status.LastAppliedGen == now.Generation
		},
	})
}

// opReboot reboots the second leaf of the pair through the Agent, which reboots when the spec holds
// the boot ID it is running on. Hardware only. Targets fabric#1488: the traffic of ESLAG
// dual-homed servers is unstable after the leaf comes back.
func (env *disruptionEnv) opReboot(ctx context.Context) error {
	return env.opLeafCycle(ctx, leafCycle{
		name:      "reboot",
		specField: "reboot",
		max:       disruptionRebootMax,
		request: func(ag *agentapi.Agent) (string, error) {
			if ag.Status.BootID == "" {
				return "", fmt.Errorf("agent %s doesn't report a boot ID (missing .status.bootID)", ag.Name) //nolint:err113
			}
			ag.Spec.Reboot = ag.Status.BootID

			return "bootID " + ag.Status.BootID, nil
		},
		back: func(before, now *agentapi.Agent) bool {
			return now.Status.BootID != "" && now.Status.BootID != before.Status.BootID &&
				!now.Status.LastHeartbeat.Time.Equal(before.Status.LastHeartbeat.Time) && now.Status.LastAppliedGen == now.Generation
		},
	})
}

// opLeafCycle takes the second leaf of the pair away through its Agent under traffic and waits for
// it to come back. The traffic is judged by judgeLeafCycle; on top of that no Agent spec may change
// but the requested field, and every Agent must have applied.
func (env *disruptionEnv) opLeafCycle(ctx context.Context, cycle leafCycle) error {
	target := env.sc.Leaves[1]

	for _, leaf := range env.sc.Leaves {
		sw := &wiringapi.Switch{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: leaf, Namespace: kmetav1.NamespaceDefault}, sw); err != nil {
			return fmt.Errorf("getting switch %s: %w", leaf, err)
		}
		if sw.Spec.Profile == meta.SwitchProfileVS || sw.Spec.Profile == meta.SwitchProfileVSCLSP {
			return fmt.Errorf("%w: %s is a virtual switch, the %s is only run on hardware", errDisruptionHardwareOnly, leaf, cycle.name)
		}
	}

	before, beforeAgents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", cycle.name, err)
	}

	agent := &agentapi.Agent{}
	if err := env.kube.Get(ctx, kclient.ObjectKey{Name: target, Namespace: kmetav1.NamespaceDefault}, agent); err != nil {
		return fmt.Errorf("getting agent %s: %w", target, err)
	}
	agentBefore := agent.DeepCopy()
	patch := kclient.MergeFrom(agent.DeepCopy())
	what, err := cycle.request(agent)
	if err != nil {
		return err
	}

	res := &checkResult{op: cycle.name + "-" + target, started: time.Now(), files: map[string]string{}}
	slog.Info("Taking switch away through the Agent", "operation", cycle.name, "switch", target, "state", what)
	if err := env.kube.Patch(ctx, agent, patch); err != nil {
		return fmt.Errorf("requesting %s of %s: %w", cycle.name, target, err)
	}

	settled, err := env.waitAgentBack(ctx, target, cycle, agentBefore)
	if err != nil {
		return err
	}
	after, agents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", cycle.name, err)
	}

	if err := sleepCtx(ctx, disruptionRecovery+disruptionWatchAfter+disruptionTrailGrace); err != nil {
		return err
	}
	end := time.Now()

	tl, err := env.tw.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", cycle.name, err)
	}
	if stale := staleProbes(tl, end.Add(-disruptionTrailGrace)); len(stale) > 0 {
		return fmt.Errorf("%w: %s: probes stopped recording, the traffic can't be judged: %s", errInconclusive, cycle.name, strings.Join(stale, ", "))
	}

	v := judgeLeafCycle(tl, env.probeClass, target, leafCycleTimes{from: env.cursor, requested: res.started, settled: settled, end: end})
	env.cursor = end
	res.failures = v.failures
	slog.Info("Loss while the leaf was away", "operation", cycle.name, "switch", target, "pings", len(v.away), "windows", summarizeLoss(v.away), "applied-at", settled.Format("15:04:05"))

	ignore := func(agent, path string) bool {
		return ignoreBenchTouch(agent, path) || (agent == target && path == cycle.specField)
	}
	if diffs := diffAgentSpecs(before, after, ignore); len(diffs) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("Agent specs changed: %s", strings.Join(diffs, ", ")))
		res.files["spec-diff.txt"] = strings.Join(diffs, "\n") + "\n"
	}
	pending, notes := agentsNotApplied(agents, agentGenerations(beforeAgents))
	env.noteOnce(notes)
	if len(pending) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("Agents not applied: %s", strings.Join(pending, ", ")))
	}
	if len(res.failures) == 0 {
		return nil
	}

	// line by line only what the checks judged; the rest is the loss expected while the leaf is away
	res.files["lost-pings.txt"] = formatLostPings(v.judged)
	res.files["expected-loss.txt"] = summarizeProbeLoss(v.expected, env.probeClass)

	return env.fail(res)
}

// leafCycleTimes are the moments a leaf cycle is judged against.
type leafCycleTimes struct {
	from      time.Time // where the traffic check starts: the end of the previous check
	requested time.Time // when the cycle was requested from the Agent
	settled   time.Time // when the leaf was back and had applied its config
	end       time.Time // where the traffic check ends
}

// leafCycleVerdict is how the traffic of a leaf cycle was judged.
type leafCycleVerdict struct {
	failures []string
	// away is the loss on the pairs that cross the leaf, for the log
	away []lostPing
	// judged is the loss the checks looked at, expected the rest: loss allowed while the leaf is away
	judged, expected []lostPing
}

// judgeLeafCycle judges the traffic of a cycle of the leaf target:
//   - strict pairs don't cross the leaf and may lose nothing
//   - hard and soft pairs may lose pings until the leaf has applied its config and the dataplane had
//     disruptionRecovery to converge, and nothing after that
//   - soft pairs have the other leaf to go through, so from the request on none of their loss
//     windows may last more than disruptionESLAGMaxLoss
func judgeLeafCycle(tl []probeTimeline, class map[string]string, target string, at leafCycleTimes) leafCycleVerdict {
	var strict, away, soft []probeTimeline
	for _, p := range tl {
		switch class[p.Probe] {
		case probeStrict:
			strict = append(strict, p)
		case probeSoft:
			soft = append(soft, p)
			away = append(away, p)
		default:
			away = append(away, p)
		}
	}
	lostStrict := lostPings(strict, at.from, at.end)
	lostAfter := lostPings(away, at.settled.Add(disruptionRecovery), at.end)
	lostSoft := lostPings(soft, at.requested, at.end)

	v := leafCycleVerdict{away: lostPings(away, at.from, at.end)}
	if len(lostStrict) > 0 {
		v.failures = append(v.failures, fmt.Sprintf("%d pings lost on pairs that don't cross %s: %s", len(lostStrict), target, summarizeLoss(lostStrict)))
	}
	if long := longLossWindows(lostSoft); len(long) > 0 {
		v.failures = append(v.failures, fmt.Sprintf("%.1fs loss on ESLAG pairs while %s rejoined (loss windows of at most %s allowed): %s",
			longestLossWindow(long).Seconds(), target, disruptionESLAGMaxLoss, summarizeWindows(long)))
	}
	if len(lostAfter) > 0 {
		v.failures = append(v.failures, fmt.Sprintf("%d pings lost more than %s after %s applied its config: %s", len(lostAfter), disruptionRecovery, target, summarizeLoss(lostAfter)))
	}

	judged := map[lostPing]bool{}
	for _, list := range [][]lostPing{lostStrict, lostSoft, lostAfter} {
		for _, l := range list {
			judged[l] = true
		}
	}
	for _, l := range lostPings(tl, at.from, at.end) {
		if judged[l] {
			v.judged = append(v.judged, l)
		} else {
			v.expected = append(v.expected, l)
		}
	}

	return v
}

// waitAgentBack waits for the Agent of the switch to come back from the cycle and returns when it saw that.
func (env *disruptionEnv) waitAgentBack(ctx context.Context, name string, cycle leafCycle, before *agentapi.Agent) (time.Time, error) {
	var backAt time.Time
	var getErr error
	err := wait.PollUntilContextTimeout(ctx, disruptionPollInterval, cycle.max, true, func(ctx context.Context) (bool, error) {
		ag := &agentapi.Agent{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, ag); err != nil {
			getErr = fmt.Errorf("getting agent %s during its %s: %w", name, cycle.name, err)

			return false, getErr
		}
		if !cycle.back(before, ag) {
			return false, nil
		}
		backAt = time.Now()

		return true, nil
	})
	if getErr != nil {
		return time.Time{}, getErr
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("waiting for %s to be back from the %s (took more than %s): %w", name, cycle.name, cycle.max, err)
	}

	return backAt, nil
}

// longLossWindows returns the loss windows of the lost pings that are longer than disruptionESLAGMaxLoss.
func longLossWindows(lost []lostPing) []lossWindow {
	var out []lossWindow
	for _, w := range groupLoss(lost, 3*trafficPingInterval) {
		if lossWindowLength(w) > disruptionESLAGMaxLoss {
			out = append(out, w)
		}
	}

	return out
}

// lossWindowLength is the time the probe was without replies: first to last lost echo, plus the
// interval the last one stands for.
func lossWindowLength(w lossWindow) time.Duration {
	return w.To.Sub(w.From) + trafficPingInterval
}

func longestLossWindow(windows []lossWindow) time.Duration {
	var longest time.Duration
	for _, w := range windows {
		longest = max(longest, lossWindowLength(w))
	}

	return longest
}

func summarizeWindows(windows []lossWindow) string {
	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		parts = append(parts, fmt.Sprintf("%s %s..%s (%d pings)", w.Probe, w.From.Format("15:04:05.000"), w.To.Format("15:04:05.000"), w.Pings))
	}

	return strings.Join(parts, "; ")
}
