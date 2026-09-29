// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	coreapi "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// How a simulated agent reaches the API.
//
// A real switch talks to the control VIP over the management network. The host
// running the bench has no address on that network, so by default the per-agent
// kubeconfig is rewritten to the VLAB's port forward. That keeps the real
// ServiceAccount identity - and with it the real RBAC path and API Priority and
// Fairness flow - while giving up the distinct source address per agent.
const (
	APIViaHostfwd = "hostfwd"
	APIViaBridge  = "bridge"
)

var APIVias = []string{APIViaHostfwd, APIViaBridge}

// AgentKubeconfigKey is the key holding the kubeconfig in the per-switch Secret
// the Fabric controller creates.
const AgentKubeconfigKey = "kubeconfig"

// conditionApplied is the condition a real agent sets once it has applied a
// generation; WaitReady and inspect both read it.
const conditionApplied = "Applied"

// DefaultApplyDelay stands in for the time a real switch spends pushing config
// to the NOS between the two status writes of an apply.
const DefaultApplyDelay = 5 * time.Second

// eventCoalesce is how long the real agent drains queued watch events before
// acting on them, so a burst of spec rewrites costs one apply rather than many.
const eventCoalesce = 5 * time.Second

// AgentsOpts configures the agent simulation.
type AgentsOpts struct {
	Duration time.Duration
	Interval time.Duration

	// ApplyDelay is how long a simulated apply takes between the two status
	// writes. Zero measures the control plane alone, with no simulated work.
	ApplyDelay time.Duration

	// Agents restricts the simulation to agents whose name starts with one of
	// these prefixes; empty means every agent in the cluster.
	Agents []string

	APIVia string
	// APIServer overrides the server address in each agent's kubeconfig, which
	// is needed whenever the control VIP is not routable from here.
	APIServer string

	// SyncHeartbeats removes the per-agent phase offset so every agent writes
	// at the same instant, which is the thundering herd worst case rather than
	// the steady state.
	SyncHeartbeats bool
}

// RunAgents simulates switch agents against every matching Agent object until
// the duration elapses or the context is cancelled.
//
// Each agent gets its own goroutine, its own client built from its own
// ServiceAccount kubeconfig, and its own connection, so the load the apiserver
// sees is N distinct identities rather than one client multiplexing.
func RunAgents(ctx context.Context, admin kclient.Client, opts AgentsOpts) error {
	if opts.Interval <= 0 {
		opts.Interval = HeartbeatPeriod
	}
	if opts.APIVia == "" {
		opts.APIVia = APIViaHostfwd
	}
	if !slices.Contains(APIVias, opts.APIVia) {
		return fmt.Errorf("unknown --api-via %q, valid values are %s", opts.APIVia, strings.Join(APIVias, ", ")) //nolint:err113
	}
	if opts.APIVia == APIViaBridge {
		return fmt.Errorf("--api-via=%s is not implemented yet: it needs per-agent addresses on the management bridge", APIViaBridge) //nolint:err113
	}

	names, err := discoverAgents(ctx, admin, opts.Agents)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no agents found to simulate") //nolint:err113
	}

	slog.Info("Preparing agents", "count", len(names), "interval", opts.Interval,
		"applyDelay", opts.ApplyDelay, "via", opts.APIVia)

	stats := &agentStats{}

	sims := make([]*agentSim, 0, len(names))
	for _, name := range names {
		sim, err := newAgentSim(ctx, admin, name, opts, stats)
		if err != nil {
			return fmt.Errorf("preparing agent %s: %w", name, err)
		}

		sims = append(sims, sim)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if opts.Duration > 0 {
		var timerCancel context.CancelFunc
		runCtx, timerCancel = context.WithTimeout(runCtx, opts.Duration)
		defer timerCancel()
	}

	slog.Info("Starting agents", "count", len(sims), "duration", opts.Duration)

	start := time.Now()

	var wg sync.WaitGroup
	for _, sim := range sims {
		wg.Go(func() {
			sim.run(runCtx, opts)
		})
	}

	wg.Go(func() {
		stats.report(runCtx, len(sims))
	})

	wg.Wait()

	stats.summary(len(sims), time.Since(start))

	return nil
}

// discoverAgents lists the agents to simulate using the admin credentials. The
// per-switch Role grants get and watch on its own Agent only, with no list, so
// discovery cannot use the agent identities.
func discoverAgents(ctx context.Context, admin kclient.Client, prefixes []string) ([]string, error) {
	agents := &agentapi.AgentList{}
	if err := admin.List(ctx, agents); err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	names := []string{}
	for _, agent := range agents.Items {
		if len(prefixes) > 0 && !slices.ContainsFunc(prefixes, func(p string) bool {
			return strings.HasPrefix(agent.Name, p)
		}) {
			continue
		}

		names = append(names, agent.Name)
	}

	sort.Strings(names)

	return names, nil
}

// agentSim is one simulated switch agent.
type agentSim struct {
	name string
	kube kclient.WithWatch

	// Identity a real agent reports: InstallID survives reinstalls, BootID a
	// reboot, RunID is fresh per agent process.
	installID string
	runID     string
	bootID    string

	// currentGen is the spec generation this agent has applied. Only the run
	// loop touches it, so no lock is needed.
	currentGen int64

	stats *agentStats
}

// newAgentSim builds a client for one agent from its own ServiceAccount
// kubeconfig, so every write carries that switch's identity.
func newAgentSim(ctx context.Context, admin kclient.Client, name string, opts AgentsOpts, stats *agentStats) (*agentSim, error) {
	secret := &coreapi.Secret{}
	key := kclient.ObjectKey{Name: AgentPrefix + name, Namespace: kmetav1.NamespaceDefault}
	if err := admin.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting kubeconfig secret %s: %w", key.Name, err)
	}

	raw, ok := secret.Data[AgentKubeconfigKey]
	if !ok {
		return nil, fmt.Errorf("secret %s has no %q key", key.Name, AgentKubeconfigKey) //nolint:err113
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}

	if opts.APIServer != "" {
		cfg.Host = opts.APIServer
	}

	// Match the real agent, which inherits client-go's defaults.
	cfg.QPS, cfg.Burst = 5, 10

	// A distinct Dial per agent bypasses client-go's transport cache, which
	// keys on TLS config rather than on the bearer token. Without it every
	// agent would share one connection and the apiserver would see a single
	// multiplexed client instead of N.
	cfg.Dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext

	scheme, err := benchScheme()
	if err != nil {
		return nil, err
	}

	kube, err := kclient.NewWithWatch(cfg, kclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	return &agentSim{
		name:      name,
		kube:      kube,
		installID: stableID("install", name),
		runID:     randomID(),
		bootID:    stableID("boot", name),
		stats:     stats,
	}, nil
}

// run drives one agent until the context is done: a startup write, then a
// single loop selecting over the heartbeat ticker and the watch, exactly as the
// real agent does. Keeping both in one loop is also what stops a heartbeat and
// an apply from racing on the same status.
func (a *agentSim) run(ctx context.Context, opts AgentsOpts) {
	// Real agents are not synchronised, so spread the first write across the
	// interval unless the herd is what is being measured.
	if !opts.SyncHeartbeats {
		offset := time.Duration(rand.Int64N(int64(opts.Interval))) //nolint:gosec // jitter, not a secret

		select {
		case <-ctx.Done():
			return
		case <-time.After(offset):
		}
	}

	a.startup(ctx)

	watcher := a.watch(ctx)
	defer func() {
		if watcher != nil {
			watcher.Stop()
		}
	}()

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()

	for {
		var events <-chan watchEvent
		if watcher != nil {
			events = watcher.events
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.beat(ctx)
		case ev, ok := <-events:
			if !ok {
				// The apiserver closed the watch; re-establish it rather than
				// spinning on a dead channel.
				watcher.Stop()
				watcher = a.watch(ctx)
				a.stats.watchResets.Add(1)

				continue
			}

			a.onGeneration(ctx, ev.generation, opts)
		}
	}
}

// startup mirrors the real agent's first write, which declares the current
// generation as already applied and publishes the agent's identity.
func (a *agentSim) startup(ctx context.Context) {
	err := a.updateStatus(ctx, func(agent *agentapi.Agent) {
		now := kmetav1.Now()

		agent.Status.Version = agent.Spec.Version.Default
		agent.Status.InstallID = a.installID
		agent.Status.RunID = a.runID
		agent.Status.BootID = a.bootID
		agent.Status.LastAttemptGen = agent.Generation
		agent.Status.LastAttemptTime = now
		agent.Status.LastAppliedGen = agent.Generation
		agent.Status.LastAppliedTime = now
		agent.Status.LastHeartbeat = now

		a.currentGen = agent.Generation

		setApplied(agent, true, fmt.Sprintf("Config applied, gen=%d", agent.Generation))
	})
	if err != nil {
		a.stats.errors.Add(1)
		a.stats.recordErr(err)
	}
}

// beat performs one heartbeat write and records what it cost.
//
// A heartbeat deliberately does not touch LastAppliedGen: that is the apply
// cycle's job, and having the heartbeat declare every generation applied would
// make a spec change look like it converged instantly.
func (a *agentSim) beat(ctx context.Context) {
	start := time.Now()

	err := a.updateStatus(ctx, func(agent *agentapi.Agent) {
		agent.Status.Version = agent.Spec.Version.Default
		agent.Status.InstallID = a.installID
		agent.Status.RunID = a.runID
		agent.Status.BootID = a.bootID
		agent.Status.LastHeartbeat = kmetav1.Now()
	})

	took := time.Since(start)

	switch {
	case err == nil:
		a.stats.heartbeats.Add(1)
		a.stats.observe(took)
	case ctx.Err() != nil:
		// Shutting down, not a failure.
	default:
		a.stats.errors.Add(1)
		a.stats.recordErr(err)
	}
}

// onGeneration runs the two-write apply cycle a real agent performs when the
// controller rewrites its spec: mark the attempt, do the work, mark it applied.
// This is what makes a single VPC edit cost two status writes on every switch.
func (a *agentSim) onGeneration(ctx context.Context, gen int64, opts AgentsOpts) {
	if gen == 0 || gen == a.currentGen {
		return
	}

	start := time.Now()

	if err := a.updateStatus(ctx, func(agent *agentapi.Agent) {
		agent.Status.LastAttemptGen = agent.Generation
		agent.Status.LastAttemptTime = kmetav1.Now()

		setApplied(agent, false, fmt.Sprintf("Config will be applied, gen=%d", agent.Generation))
	}); err != nil {
		if ctx.Err() == nil {
			a.stats.errors.Add(1)
			a.stats.recordErr(err)
		}

		return
	}

	// Stand in for the time a real switch spends pushing config to the NOS.
	if opts.ApplyDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(opts.ApplyDelay):
		}
	}

	var applied int64
	if err := a.updateStatus(ctx, func(agent *agentapi.Agent) {
		now := kmetav1.Now()

		agent.Status.LastAppliedGen = agent.Generation
		agent.Status.LastAppliedTime = now
		agent.Status.LastHeartbeat = now
		applied = agent.Generation

		setApplied(agent, true, fmt.Sprintf("Config applied, gen=%d", agent.Generation))
	}); err != nil {
		if ctx.Err() == nil {
			a.stats.errors.Add(1)
			a.stats.recordErr(err)
		}

		return
	}

	a.currentGen = applied
	a.stats.applies.Add(1)
	a.stats.observeApply(time.Since(start))
}

// updateStatus mirrors the real agent's write: re-Get on conflict, assign the
// whole status, and update through the status subresource.
func (a *agentSim) updateStatus(ctx context.Context, mutate func(*agentapi.Agent)) error {
	key := kclient.ObjectKey{Name: a.name, Namespace: kmetav1.NamespaceDefault}

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		agent := &agentapi.Agent{}
		if err := a.kube.Get(ctx, key, agent); err != nil {
			return fmt.Errorf("getting agent: %w", err)
		}

		if agent.Status.Conditions == nil {
			agent.Status.Conditions = []kmetav1.Condition{}
		}

		mutate(agent)

		if err := a.kube.Status().Update(ctx, agent); err != nil {
			if kapierrors.IsConflict(err) {
				a.stats.conflicts.Add(1)
			}

			return fmt.Errorf("updating status: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("status update for %s: %w", a.name, err)
	}

	return nil
}

// setApplied sets the Applied condition the same way the real agent does.
func setApplied(agent *agentapi.Agent, ok bool, message string) {
	status, reason := kmetav1.ConditionFalse, "ApplyPending"
	if ok {
		status, reason = kmetav1.ConditionTrue, "ApplySucceeded"
	}

	kmeta.SetStatusCondition(&agent.Status.Conditions, kmetav1.Condition{
		Type:               conditionApplied,
		Status:             status,
		Reason:             reason,
		ObservedGeneration: agent.Generation,
		Message:            message,
	})
}

// watchEvent carries the generation seen on the watched Agent.
type watchEvent struct {
	generation int64
}

// agentWatch is a watch on this agent's own Agent object, with the queued
// events coalesced the way the real agent coalesces them.
type agentWatch struct {
	events chan watchEvent
	stop   func()
}

func (w *agentWatch) Stop() {
	if w != nil && w.stop != nil {
		w.stop()
	}
}

// watch opens a watch restricted to this agent's own object.
//
// The field selector is not an optimisation: the per-switch Role grants watch
// with resourceNames scoped to this one Agent, and RBAC can only match that for
// a collection request when the apiserver can read the name out of a
// metadata.name field selector.
func (a *agentSim) watch(ctx context.Context) *agentWatch {
	watcher, err := a.kube.Watch(ctx, &agentapi.AgentList{},
		kclient.MatchingFields{"metadata.name": a.name},
		kclient.InNamespace(kmetav1.NamespaceDefault))
	if err != nil {
		if ctx.Err() == nil {
			a.stats.errors.Add(1)
			a.stats.recordErr(fmt.Errorf("watching %s: %w", a.name, err))
		}

		return nil
	}

	out := make(chan watchEvent, 1)

	go func() {
		defer close(out)
		defer watcher.Stop()

		for {
			raw, ok := <-watcher.ResultChan()
			if !ok {
				return
			}

			agent, ok := raw.Object.(*agentapi.Agent)
			if !ok {
				continue
			}

			gen := agent.Generation

			// Coalesce whatever else is already queued: a burst of spec
			// rewrites should cost one apply, not one per event.
			gen = drainWatch(ctx, watcher.ResultChan(), gen)

			select {
			case out <- watchEvent{generation: gen}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &agentWatch{events: out, stop: watcher.Stop}
}

// drainWatch consumes further events that arrive within the coalesce window,
// returning the newest generation seen.
func drainWatch(ctx context.Context, ch <-chan watch.Event, gen int64) int64 {
	deadline := time.NewTimer(eventCoalesce)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			return gen
		case <-deadline.C:
			return gen
		case raw, ok := <-ch:
			if !ok {
				return gen
			}
			if agent, ok := raw.Object.(*agentapi.Agent); ok && agent.Generation > gen {
				gen = agent.Generation
			}
		default:
			return gen
		}
	}
}

// agentStats aggregates what the simulated fleet is doing.
type agentStats struct {
	heartbeats  atomic.Int64
	applies     atomic.Int64
	conflicts   atomic.Int64
	errors      atomic.Int64
	watchResets atomic.Int64

	mu       sync.Mutex
	latency  []time.Duration
	applyLat []time.Duration
	lastErr  error
	errCount int
}

func (s *agentStats) observe(took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.latency = append(s.latency, took)
}

func (s *agentStats) observeApply(took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.applyLat = append(s.applyLat, took)
}

func (s *agentStats) recordErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastErr = err
	s.errCount++
}

// window is what happened since the last report.
type window struct {
	latency  []time.Duration
	applyLat []time.Duration
	lastErr  error
	errCount int
}

// drain returns the window since the last call, leaving the buffers empty so
// each report covers its own interval.
func (s *agentStats) drain() window {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := window{latency: s.latency, applyLat: s.applyLat, lastErr: s.lastErr, errCount: s.errCount}

	s.latency, s.applyLat, s.lastErr, s.errCount = nil, nil, nil, 0

	return out
}

// report prints an aggregate line periodically, which is how a long run is
// watched without per-agent noise.
func (s *agentStats) report(ctx context.Context, agents int) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	last := s.heartbeats.Load()
	lastAt := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.heartbeats.Load()
			elapsed := time.Since(lastAt)

			win := s.drain()
			p50, p95 := percentiles(win.latency)

			args := []any{
				"agents", agents,
				"heartbeats", now,
				"rate", fmt.Sprintf("%.1f/s", float64(now-last)/elapsed.Seconds()),
				"p50", p50.Truncate(time.Millisecond),
				"p95", p95.Truncate(time.Millisecond),
				"applies", s.applies.Load(),
				"conflicts", s.conflicts.Load(),
				"errors", s.errors.Load(),
			}

			if applyP50, applyP95 := percentiles(win.applyLat); applyP50 > 0 {
				args = append(args, "applyP50", applyP50.Truncate(time.Millisecond),
					"applyP95", applyP95.Truncate(time.Millisecond))
			}
			if resets := s.watchResets.Load(); resets > 0 {
				args = append(args, "watchResets", resets)
			}
			if win.errCount > 0 && win.lastErr != nil {
				args = append(args, "lastErr", win.lastErr.Error())
			}

			slog.Info("Agents", args...)

			last, lastAt = now, time.Now()
		}
	}
}

func (s *agentStats) summary(agents int, took time.Duration) {
	beats := s.heartbeats.Load()

	slog.Info("Agents stopped",
		"agents", agents,
		"heartbeats", beats,
		"rate", fmt.Sprintf("%.1f/s", float64(beats)/took.Seconds()),
		"applies", s.applies.Load(),
		"conflicts", s.conflicts.Load(),
		"errors", s.errors.Load(),
		"watchResets", s.watchResets.Load(),
		"took", took.Truncate(time.Second))
}

func percentiles(lat []time.Duration) (time.Duration, time.Duration) {
	if len(lat) == 0 {
		return 0, 0
	}

	slices.Sort(lat)

	return lat[len(lat)*50/100], lat[min(len(lat)*95/100, len(lat)-1)]
}

// stableID derives an ID that stays the same across runs for a given switch,
// matching how InstallID and BootID persist on a real switch.
func stableID(kind, name string) string {
	sum := uint64(1469598103934665603)
	for _, b := range []byte(kind + "/" + name) {
		sum ^= uint64(b)
		sum *= 1099511628211
	}

	return fmt.Sprintf("%016x", sum)
}

func randomID() string {
	buf := make([]byte, 8)
	if _, err := crand.Read(buf); err != nil {
		return "0000000000000000"
	}

	return hex.EncodeToString(buf)
}
