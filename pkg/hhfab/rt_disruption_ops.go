// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func summarizeLoss(lost []lostPing) string {
	windows := groupLoss(lost, 3*trafficPingInterval)
	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		parts = append(parts, fmt.Sprintf("%s %s..%s (%d pings)", w.Probe, w.From.Format("15:04:05.000"), w.To.Format("15:04:05.000"), w.Pings))
	}

	return strings.Join(parts, "; ")
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

// fail records the evidence of a failed operation and returns the error to report.
func (env *disruptionEnv) fail(res *checkResult) error {
	if err := os.MkdirAll(env.evidenceDir, 0o755); err != nil {
		slog.Warn("Creating evidence directory failed", "dir", env.evidenceDir, "err", err)
	} else {
		for name, content := range res.files {
			file := filepath.Join(env.evidenceDir, sanitizeNameForFolder(res.op)+"-"+name)
			if err := os.WriteFile(file, []byte(content), 0o644); err != nil { //nolint:gosec
				slog.Warn("Writing evidence failed", "file", file, "err", err)
			}
		}
	}

	return fmt.Errorf("%s: %s (evidence in %s)", res.op, strings.Join(res.failures, "; "), env.evidenceDir) //nolint:err113
}

// runUnrelated runs an operation that must not disturb anything: no lost ping, no change in any
// Agent spec but the touch marker, every Agent applied.
func (env *disruptionEnv) runUnrelated(ctx context.Context, op string, do func(context.Context) error) error {
	// an operation that errors out before its traffic check must not leave its loss to the next one
	checked := false
	defer func() {
		if !checked {
			env.cursor = time.Now()
		}
	}()

	before, beforeAgents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	res := &checkResult{op: op, started: time.Now(), files: map[string]string{}}
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
	checked = true

	if len(lost) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost: %s", len(lost), summarizeLoss(lost)))
	}
	if diffs := diffAgentSpecs(before, after, ignoreBenchTouch); len(diffs) > 0 {
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

	res.files["lost-pings.txt"] = formatLostPings(lost)
	res.files["agent-timeline.txt"] = formatAgentRows(env.aw.Rows(res.started, end))
	for _, p := range tl {
		res.files["ping-"+sanitizeNameForFolder(p.Probe)+".log"] = p.Raw
	}

	return env.fail(res)
}

func (env *disruptionEnv) opCreateDeleteVPC(ctx context.Context) error {
	vpc, err := env.newVPC("dt-un")
	if err != nil {
		return err
	}

	for cycle := 1; cycle <= disruptionVPCCycles; cycle++ {
		if err := env.runUnrelated(ctx, fmt.Sprintf("create-vpc-%d", cycle), func(ctx context.Context) error {
			_, err := CreateOrUpdateVpc(ctx, env.kube, vpc.DeepCopy())

			return err
		}); err != nil {
			return err
		}
		if err := env.runUnrelated(ctx, fmt.Sprintf("delete-vpc-%d", cycle), func(ctx context.Context) error {
			if err := env.kube.Delete(ctx, vpc.DeepCopy()); err != nil {
				return fmt.Errorf("deleting VPC: %w", err)
			}

			return nil
		}); err != nil {
			return err
		}
	}

	return nil
}

// opTouchLeaves puts the benchmark touch label on the leaves of the pair in turn. The first failing
// round ends the operation.
func (env *disruptionEnv) opTouchLeaves(ctx context.Context) error {
	for round := 1; round <= disruptionTouchRounds; round++ {
		leaf := env.sc.Leaves[(round-1)%2]
		if err := env.runUnrelated(ctx, fmt.Sprintf("touch-%d-%s", round, leaf), func(ctx context.Context) error {
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
			return fmt.Errorf("round %d of %d: %w", round, disruptionTouchRounds, err)
		}
	}

	return nil
}

// opRestartController restarts fabric-ctrl the way kubectl rollout restart does.
func (env *disruptionEnv) opRestartController(ctx context.Context) error {
	kubeconfig := filepath.Join(env.testCtx.vlabCfg.WorkDir, VLABDir, VLABKubeConfig)
	kc, err := kubeutil.NewClient(ctx, kubeconfig, appsapi.AddToScheme)
	if err != nil {
		return fmt.Errorf("creating kube client for deployments: %w", err)
	}
	key := kclient.ObjectKey{Namespace: comp.FabNamespace, Name: "fabric-ctrl"}

	for n := 1; n <= disruptionCtrlRestarts; n++ {
		if err := env.runUnrelated(ctx, fmt.Sprintf("restart-fabric-ctrl-%d", n), func(ctx context.Context) error {
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
			return err
		}
	}

	return nil
}

func waitRollout(ctx context.Context, kc kclient.Client, key kclient.ObjectKey) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for {
		dep := &appsapi.Deployment{}
		if err := kc.Get(ctx, key, dep); err != nil {
			return fmt.Errorf("getting deployment %s: %w", key, err)
		}
		want := int32(1)
		if dep.Spec.Replicas != nil {
			want = *dep.Spec.Replicas
		}
		st := dep.Status
		if st.ObservedGeneration >= dep.Generation && st.UpdatedReplicas == want && st.Replicas == want && st.AvailableReplicas == want {
			return nil
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return fmt.Errorf("waiting for rollout of %s: %w", key, err)
		}
	}
}

// opReinstall reinstalls the second leaf of the pair through the Agent. Only on hardware for now.
func (env *disruptionEnv) opReinstall(ctx context.Context) error {
	target := env.sc.Leaves[1]

	for _, leaf := range env.sc.Leaves {
		sw := &wiringapi.Switch{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: leaf, Namespace: kmetav1.NamespaceDefault}, sw); err != nil {
			return fmt.Errorf("getting switch %s: %w", leaf, err)
		}
		if sw.Spec.Profile == meta.SwitchProfileVS || sw.Spec.Profile == meta.SwitchProfileVSCLSP {
			env.testCtx.addNote(fmt.Sprintf("agent reinstall not run: %s is a virtual switch, the reinstall is only run on hardware", leaf))

			return nil
		}
	}

	// an operation that errors out before its traffic check must not leave its loss to the next one
	checked := false
	defer func() {
		if !checked {
			env.cursor = time.Now()
		}
	}()

	before, beforeAgents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("reinstall: %w", err)
	}

	agent := &agentapi.Agent{}
	if err := env.kube.Get(ctx, kclient.ObjectKey{Name: target, Namespace: kmetav1.NamespaceDefault}, agent); err != nil {
		return fmt.Errorf("getting agent %s: %w", target, err)
	}
	oldInstall := agent.Status.InstallID
	oldHeartbeat := agent.Status.LastHeartbeat.Time
	if oldInstall == "" {
		return fmt.Errorf("agent %s is not installed (missing .status.installID)", target) //nolint:err113
	}

	res := &checkResult{op: "reinstall-" + target, started: time.Now(), files: map[string]string{}}
	slog.Info("Reinstalling switch through the Agent", "switch", target, "installID", oldInstall)
	patch := kclient.MergeFrom(agent.DeepCopy())
	agent.Spec.Reinstall = oldInstall
	if err := env.kube.Patch(ctx, agent, patch); err != nil {
		return fmt.Errorf("requesting reinstall of %s: %w", target, err)
	}

	settled, err := env.waitReinstalled(ctx, target, oldInstall, oldHeartbeat)
	if err != nil {
		return err
	}
	after, agents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("reinstall: %w", err)
	}

	if err := sleepCtx(ctx, disruptionRecovery+disruptionWatchAfter+disruptionTrailGrace); err != nil {
		return err
	}
	end := time.Now()

	tl, err := env.tw.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("reinstall: %w", err)
	}
	if stale := staleProbes(tl, end.Add(-disruptionTrailGrace)); len(stale) > 0 {
		return fmt.Errorf("%w: reinstall: probes stopped recording, the traffic can't be judged: %s", errInconclusive, strings.Join(stale, ", "))
	}

	// strict probes never cross the leaf; hard and soft ones may lose pings until the leaf has
	// applied its config and the dataplane had disruptionRecovery to converge
	var strict, away []probeTimeline
	for _, p := range tl {
		if env.probeClass[p.Probe] == probeStrict {
			strict = append(strict, p)
		} else {
			away = append(away, p)
		}
	}
	lostStrict := lostPings(strict, env.cursor, end)
	lostAfter := lostPings(away, settled.Add(disruptionRecovery), end)
	lastAway := lostPings(away, env.cursor, end)
	env.cursor = end
	checked = true

	if len(lostStrict) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost on pairs that don't cross %s: %s", len(lostStrict), target, summarizeLoss(lostStrict)))
	}
	if len(lostAfter) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost more than %s after %s applied its config: %s", len(lostAfter), disruptionRecovery, target, summarizeLoss(lostAfter)))
	}
	slog.Info("Loss while the leaf was away", "switch", target, "pings", len(lastAway), "windows", summarizeLoss(lastAway), "applied-at", settled.Format("15:04:05"))

	ignore := func(agent, path string) bool {
		return ignoreBenchTouch(agent, path) || (agent == target && path == "reinstall")
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

	res.files["lost-pings.txt"] = formatLostPings(lostPings(tl, res.started, end))
	res.files["agent-timeline.txt"] = formatAgentRows(env.aw.Rows(res.started, end))
	for _, p := range tl {
		res.files["ping-"+sanitizeNameForFolder(p.Probe)+".log"] = p.Raw
	}

	return env.fail(res)
}

// waitReinstalled waits for the Agent of the switch to come back from the install: a new install
// ID, a heartbeat newer than the one before the reinstall (both stamped by the switch, so no
// comparison with the runner clock) and the current generation applied. It returns when it saw that.
func (env *disruptionEnv) waitReinstalled(ctx context.Context, name, oldInstall string, oldHeartbeat time.Time) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, disruptionReinstallMax)
	defer cancel()

	for {
		ag := &agentapi.Agent{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, ag); err != nil {
			return time.Time{}, fmt.Errorf("getting agent %s while it reinstalls: %w", name, err)
		}
		if ag.Status.InstallID != "" && ag.Status.InstallID != oldInstall &&
			ag.Status.LastHeartbeat.Time.After(oldHeartbeat) && ag.Status.LastAppliedGen == ag.Generation {
			return time.Now(), nil
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return time.Time{}, fmt.Errorf("waiting for %s to be reinstalled (took more than %s): %w", name, disruptionReinstallMax, err)
		}
	}
}
