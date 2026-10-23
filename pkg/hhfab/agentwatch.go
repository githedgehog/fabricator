// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// AgentSpecs holds the decoded .spec of every Agent, by Agent name.
type AgentSpecs map[string]map[string]any

// SnapshotAgentSpecs reads the .spec of every Agent. It stays on the Agent API: the contract
// between the controller and the switches, never what the switches run.
func SnapshotAgentSpecs(ctx context.Context, kube kclient.Reader) (AgentSpecs, error) {
	agents := &agentapi.AgentList{}
	if err := kube.List(ctx, agents); err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	specs := AgentSpecs{}
	for _, ag := range agents.Items {
		raw, err := json.Marshal(ag.Spec)
		if err != nil {
			return nil, fmt.Errorf("marshalling spec of agent %s: %w", ag.Name, err)
		}
		decoded := map[string]any{}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("decoding spec of agent %s: %w", ag.Name, err)
		}
		specs[ag.Name] = decoded
	}

	return specs, nil
}

// SpecIgnore reports whether a change at path (dotted, relative to .spec) of the agent is
// expected and has to be left out of the diff.
type SpecIgnore func(agent, path string) bool

// IgnoreBenchTouch ignores the benchmark touch marker the controller copies from the labels.
func IgnoreBenchTouch(_, path string) bool {
	return path == "benchTouch"
}

// DiffAgentSpecs lists the differences between two snapshots, one line per changed leaf, sorted.
func DiffAgentSpecs(before, after AgentSpecs, ignore SpecIgnore) []string {
	var diffs []string
	names := map[string]bool{}
	for n := range before {
		names[n] = true
	}
	for n := range after {
		names[n] = true
	}

	for _, name := range sortedKeys(names) {
		b, bok := before[name]
		a, aok := after[name]
		switch {
		case !bok:
			diffs = append(diffs, fmt.Sprintf("%s: agent appeared", name))
		case !aok:
			diffs = append(diffs, fmt.Sprintf("%s: agent disappeared", name))
		default:
			diffValues(name, "", b, a, ignore, &diffs)
		}
	}

	return diffs
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}

func diffValues(agent, path string, before, after any, ignore SpecIgnore, out *[]string) {
	if path != "" && ignore != nil && ignore(agent, path) {
		return
	}

	bm, bIsMap := before.(map[string]any)
	am, aIsMap := after.(map[string]any)
	if bIsMap && aIsMap {
		keys := map[string]bool{}
		for k := range bm {
			keys[k] = true
		}
		for k := range am {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			child := k
			if path != "" {
				child = path + "." + k
			}
			diffValues(agent, child, bm[k], am[k], ignore, out)
		}

		return
	}

	if reflect.DeepEqual(before, after) {
		return
	}
	*out = append(*out, fmt.Sprintf("%s: spec.%s: %s -> %s", agent, path, renderSpecValue(before), renderSpecValue(after)))
}

func renderSpecValue(v any) string {
	if v == nil {
		return "<absent>"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}

	return string(raw)
}

// AgentGenerations returns the generation of every Agent by name.
func AgentGenerations(agents []agentapi.Agent) map[string]int64 {
	gens := make(map[string]int64, len(agents))
	for _, ag := range agents {
		gens[ag.Name] = ag.Generation
	}

	return gens
}

// AgentsNotApplied returns, per Agent, why it doesn't report the current generation as applied,
// and notes about what it could not judge. The Applied condition has to be True on top of
// lastAppliedGen == generation: after a reinstall the generation can match while the condition
// still says ApplyPending.
//
// An Agent writes the condition when it processes a generation change, not when it starts. So an
// Agent whose generation is not in before, or differs from it, has just processed a change and
// must report Applied=True. One that didn't change fails only on a condition that exists and
// isn't True; a missing condition there is a note, nothing was applied that could be checked.
func AgentsNotApplied(agents []agentapi.Agent, before map[string]int64) ([]string, []string) {
	var failures, notes []string
	for _, ag := range agents {
		cond := kmeta.FindStatusCondition(ag.Status.Conditions, "Applied")
		prev, seen := before[ag.Name]
		changed := !seen || prev != ag.Generation
		switch {
		case ag.Generation == 0 || ag.Status.LastAppliedGen != ag.Generation:
			failures = append(failures, fmt.Sprintf("%s: lastAppliedGen=%d generation=%d", ag.Name, ag.Status.LastAppliedGen, ag.Generation))
		case cond == nil && changed:
			failures = append(failures, fmt.Sprintf("%s: no Applied condition after generation %d was applied", ag.Name, ag.Generation))
		case cond == nil:
			notes = append(notes, fmt.Sprintf("%s: no Applied condition, its generation did not change so nothing was checked", ag.Name))
		case cond.Status != kmetav1.ConditionTrue:
			failures = append(failures, fmt.Sprintf("%s: Applied=%s reason=%s message=%q (lastAppliedGen == generation == %d)",
				ag.Name, cond.Status, cond.Reason, cond.Message, ag.Generation))
		}
	}

	return failures, notes
}

// WaitAgentsSettled waits until every Agent reports lastAppliedGen == generation and no
// generation has changed for the quiet period, then returns the Agents. It doesn't look at the
// Applied condition: a stuck condition has to be reported by the caller, not waited for.
func WaitAgentsSettled(ctx context.Context, kube kclient.Reader, quiet, timeout time.Duration) ([]agentapi.Agent, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	gens := map[string]int64{}
	lastChange := time.Now()
	for {
		agents := &agentapi.AgentList{}
		if err := kube.List(ctx, agents); err != nil {
			return nil, fmt.Errorf("listing agents while waiting for them to settle: %w", err)
		}

		pending := 0
		for _, ag := range agents.Items {
			if g, ok := gens[ag.Name]; !ok || g != ag.Generation {
				gens[ag.Name] = ag.Generation
				lastChange = time.Now()
			}
			if ag.Generation == 0 || ag.Status.LastAppliedGen != ag.Generation {
				pending++
			}
		}
		if pending == 0 && time.Since(lastChange) >= quiet {
			return agents.Items, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("agents did not settle in %s (%d pending): %w", timeout, pending, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// AgentRow is one change of what an Agent reports.
type AgentRow struct {
	At             time.Time
	Agent          string
	Generation     int64
	LastAppliedGen int64
	Applied        string
}

func (r AgentRow) String() string {
	return fmt.Sprintf("%s %s gen=%d lastAppliedGen=%d Applied=%s", r.At.Format("15:04:05.000"), r.Agent, r.Generation, r.LastAppliedGen, r.Applied)
}

// AgentWatch records when each Agent changed generation, applied generation or Applied
// condition, so a failure comes with the apply timeline.
type AgentWatch struct {
	kube   kclient.Reader
	mu     sync.Mutex
	rows   []AgentRow
	last   map[string]AgentRow
	cancel context.CancelFunc
	done   chan struct{}
}

func NewAgentWatch(kube kclient.Reader) *AgentWatch {
	return &AgentWatch{kube: kube, last: map[string]AgentRow{}}
}

func appliedSummary(ag *agentapi.Agent) string {
	cond := kmeta.FindStatusCondition(ag.Status.Conditions, "Applied")
	if cond == nil {
		return "none"
	}

	return fmt.Sprintf("%s/%s", cond.Status, cond.Reason)
}

func (w *AgentWatch) poll(ctx context.Context) {
	agents := &agentapi.AgentList{}
	if err := w.kube.List(ctx, agents); err != nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range agents.Items {
		ag := &agents.Items[i]
		row := AgentRow{At: now, Agent: ag.Name, Generation: ag.Generation, LastAppliedGen: ag.Status.LastAppliedGen, Applied: appliedSummary(ag)}
		prev, seen := w.last[ag.Name]
		if seen && prev.Generation == row.Generation && prev.LastAppliedGen == row.LastAppliedGen && prev.Applied == row.Applied {
			continue
		}
		w.last[ag.Name] = row
		w.rows = append(w.rows, row)
	}
}

// Start polls once a second until Stop.
func (w *AgentWatch) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		w.poll(ctx)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.poll(ctx)
			}
		}
	}()
}

func (w *AgentWatch) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		w.cancel = nil
	}
}

// Rows returns the recorded changes with a stamp in [from, to].
func (w *AgentWatch) Rows(from, to time.Time) []AgentRow {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.DeleteFunc(slices.Clone(w.rows), func(r AgentRow) bool { return r.At.Before(from) || r.At.After(to) })
}

func FormatAgentRows(rows []AgentRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.String())
		b.WriteString("\n")
	}

	return b.String()
}
