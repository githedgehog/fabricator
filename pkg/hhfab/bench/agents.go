// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"sort"
	"strconv"
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

// The real agent debounces its watch before acting on an event
// (fabric/pkg/agent/agent.go, "skip queued events"): it keeps draining while
// events arrive no more than eventQuiet apart, and gives up after eventCoalesce
// however busy the watch is. A burst of spec rewrites therefore costs one
// apply, while a lone rewrite waits only eventQuiet rather than the full cap.
const (
	eventQuiet    = time.Second
	eventCoalesce = 5 * time.Second
)

// watchRetryDelay is how long an agent waits before reopening a watch that
// failed or was closed. The real agent exits when its watch closes and is
// restarted by systemd, so it too comes back after a pause rather than at once.
const watchRetryDelay = 5 * time.Second

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

	// PadSize pads each Agent to at least this many bytes, so object size
	// can be swept without waiting on a realistic State tree, and so a run can
	// be given headroom above whatever production currently reports. Zero
	// leaves the status at its natural size.
	PadSize int
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
		"applyDelay", opts.ApplyDelay, "padSize", opts.PadSize, "via", opts.APIVia)

	// One shared filler for the whole fleet: it is junk, so there is no reason
	// to pay for per-agent randomness. It is random rather than repeated so it
	// does not compress away and understate what is being stored.
	filler := ""
	if opts.PadSize > 0 {
		var err error
		if filler, err = newFiller(opts.PadSize); err != nil {
			return err
		}
	}

	stats := &agentStats{}

	sims := make([]*agentSim, 0, len(names))
	for _, name := range names {
		sim, err := newAgentSim(ctx, admin, name, opts, filler, stats)
		if err != nil {
			return fmt.Errorf("preparing agent %s: %w", name, err)
		}

		sims = append(sims, sim)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The duration ends the run by closing stop, not by cancelling the context.
	// Agents then leave their loops and close their watches while the context is
	// still live, so client-go finishes its in-flight reads cleanly. Cancelling
	// instead aborts those reads mid-body, which the client logs as an error and
	// which makes a run that finished correctly look like it failed.
	//
	// Cancelling the parent context still works for an interrupt: every loop
	// selects on both.
	stop := make(chan struct{})

	if opts.Duration > 0 {
		timer := time.AfterFunc(opts.Duration, func() { close(stop) })
		defer timer.Stop()
	}

	slog.Info("Starting agents", "count", len(sims), "duration", opts.Duration)

	start := time.Now()

	var wg sync.WaitGroup
	for _, sim := range sims {
		wg.Go(func() {
			sim.run(runCtx, stop, opts)
		})
	}

	wg.Go(func() {
		stats.report(runCtx, stop, len(sims), opts.Interval)
	})

	wg.Wait()

	stats.summary(len(sims), time.Since(start), opts.Interval)

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

	// padSize, when set, pads every status write up to that many bytes.
	padSize int
	filler  string

	stats *agentStats
}

// newAgentSim builds a client for one agent from its own ServiceAccount
// kubeconfig, so every write carries that switch's identity.
func newAgentSim(ctx context.Context, admin kclient.Client, name string, opts AgentsOpts, filler string, stats *agentStats) (*agentSim, error) {
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
		padSize:   opts.PadSize,
		filler:    filler,
		stats:     stats,
	}, nil
}

// run drives one agent until stop closes or the context is done: a startup
// write, then a single loop selecting over the heartbeat ticker and the watch,
// exactly as the real agent does. Keeping both in one loop is also what stops a
// heartbeat and an apply from racing on the same status.
//
// stop is the orderly end of the run and ctx.Done an interrupt. Returning on
// stop lets the deferred watch close run against a live context, which is what
// keeps a finished run quiet in the log.
func (a *agentSim) run(ctx context.Context, stop <-chan struct{}, opts AgentsOpts) {
	// Real agents are not synchronised, so spread the first write across the
	// interval unless the herd is what is being measured.
	if !opts.SyncHeartbeats {
		offset := time.Duration(rand.Int64N(int64(opts.Interval))) //nolint:gosec // jitter, not a secret

		select {
		case <-ctx.Done():
			return
		case <-stop:
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

	// retry is armed whenever there is no watch - because opening one failed or
	// the apiserver closed it - and reopens it after watchRetryDelay. Without
	// it a single failed watch would leave the agent heartbeating but never
	// applying for the rest of the run, and a watch the apiserver keeps closing
	// would be reopened in a tight loop against the server being measured.
	var retry <-chan time.Time

	for {
		var events <-chan watchEvent
		if watcher != nil {
			events = watcher.events
		} else if retry == nil {
			retry = time.After(watchRetryDelay)
		}

		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			a.beat(ctx)
		case <-retry:
			retry = nil
			watcher = a.watch(ctx)
		case ev, ok := <-events:
			if !ok {
				watcher.Stop()
				watcher = nil
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
	if err != nil && ctx.Err() == nil {
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

		if err := a.pad(agent); err != nil {
			return err
		}

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

// padKeyPrefix marks the entries pad adds, so they can be stripped before
// measuring rather than compounding on every write.
const padKeyPrefix = "bench-pad-"

// padConverge bounds the measure-and-grow loop. Each pass closes most of the
// gap, so this only guards against pathological cases.
const padConverge = 6

// pad grows the whole Agent object to at least padSize bytes. The target is
// the marshalled object, spec included, not the status subtree alone - a switch
// with a large spec therefore needs less filler to reach the same total.
//
// The padding goes into Status.State.Firmware because it is a map[string]string
// in the CRD's structural schema, so arbitrary keys survive the round trip -
// anything not in the schema would be pruned by the apiserver. It is also inert
// for the readers that matter: inspect reads the LLDP and BGP neighbour maps,
// not firmware.
func (a *agentSim) pad(agent *agentapi.Agent) error {
	if a.padSize <= 0 {
		return nil
	}

	if agent.Status.State.Firmware == nil {
		agent.Status.State.Firmware = map[string]string{}
	}

	// Drop previous padding so the object is measured at its real size.
	for key := range agent.Status.State.Firmware {
		if strings.HasPrefix(key, padKeyPrefix) {
			delete(agent.Status.State.Firmware, key)
		}
	}

	for idx := range padConverge {
		raw, err := json.Marshal(agent)
		if err != nil {
			return fmt.Errorf("measuring agent %s: %w", a.name, err)
		}

		need := a.padSize - len(raw)
		if need <= 0 {
			return nil
		}

		if need > len(a.filler) {
			need = len(a.filler)
		}

		agent.Status.State.Firmware[fmt.Sprintf("%s%02d", padKeyPrefix, idx)] = a.filler[:need]
	}

	return nil
}

// newFiller builds the shared padding: random so it does not compress away,
// hex-encoded so it is valid UTF-8 for JSON.
func newFiller(size int) (string, error) {
	buf := make([]byte, size/2+1)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generating status filler: %w", err)
	}

	return hex.EncodeToString(buf)[:size], nil
}

// ParseSize reads a human byte size such as "100KB", "256KiB" or a bare byte
// count.
func ParseSize(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}

	units := []struct {
		suffix string
		mult   int
	}{
		{"KiB", 1024}, {"MiB", 1024 * 1024},
		{"KB", 1000}, {"MB", 1000 * 1000},
		{"K", 1024}, {"M", 1024 * 1024},
		{"B", 1},
	}

	upper := strings.ToUpper(value)
	for _, unit := range units {
		if !strings.HasSuffix(upper, strings.ToUpper(unit.suffix)) {
			continue
		}

		num := strings.TrimSpace(upper[:len(upper)-len(unit.suffix)])
		parsed, err := strconv.Atoi(num)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q", value) //nolint:err113
		}

		return parsed * unit.mult, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q, expected a byte count or a value like 100KB", value) //nolint:err113
	}

	return parsed, nil
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

			// Debounce like the real agent: a burst of spec rewrites should
			// cost one apply, not one per event.
			gen = drainWatch(ctx, watcher.ResultChan(), gen, eventQuiet, eventCoalesce)

			select {
			case out <- watchEvent{generation: gen}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &agentWatch{events: out, stop: watcher.Stop}
}

// drainWatch consumes further events until the watch has been quiet for
// eventQuiet or eventCoalesce has passed, returning the newest generation seen.
// It must block: returning as soon as nothing is queued would give every
// rewrite in a burst its own apply, overstating the writes a real fleet makes.
func drainWatch(ctx context.Context, ch <-chan watch.Event, gen int64, quietFor, limit time.Duration) int64 {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()

	quiet := time.NewTimer(quietFor)
	defer quiet.Stop()

	for {
		select {
		case <-ctx.Done():
			return gen
		case <-deadline.C:
			return gen
		case <-quiet.C:
			return gen
		case raw, ok := <-ch:
			if !ok {
				return gen
			}
			if agent, ok := raw.Object.(*agentapi.Agent); ok && agent.Generation > gen {
				gen = agent.Generation
			}

			// Another event arrived, so the quiet period starts over.
			quiet.Reset(quietFor)
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
func (s *agentStats) report(ctx context.Context, stop <-chan struct{}, agents int, interval time.Duration) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	expected := expectedRate(agents, interval)

	last := s.heartbeats.Load()
	lastAt := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
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
				"expected", expected,
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

func (s *agentStats) summary(agents int, took, interval time.Duration) {
	beats := s.heartbeats.Load()

	slog.Info("Agents stopped",
		"agents", agents,
		"heartbeats", beats,
		"rate", fmt.Sprintf("%.1f/s", float64(beats)/took.Seconds()),
		"expected", expectedRate(agents, interval),
		"applies", s.applies.Load(),
		"conflicts", s.conflicts.Load(),
		"errors", s.errors.Load(),
		"watchResets", s.watchResets.Load(),
		"took", took.Truncate(time.Second))
}

// expectedRate is the heartbeat rate a fleet produces when every agent keeps
// its schedule: one write per agent per interval. It is demand, not capacity,
// so a run that sits below it is falling behind rather than saturating.
func expectedRate(agents int, interval time.Duration) string {
	if interval <= 0 {
		return "n/a"
	}

	return fmt.Sprintf("%.1f/s", float64(agents)/interval.Seconds())
}

func percentiles(lat []time.Duration) (time.Duration, time.Duration) {
	if len(lat) == 0 {
		return 0, 0
	}

	slices.Sort(lat)

	return lat[nearestRank(len(lat), 50)], lat[nearestRank(len(lat), 95)]
}

// nearestRank is the index of the pth percentile, ceil(p/100 * n) - 1.
//
// The obvious n*p/100 biases high on small samples: with two observations it
// picks the larger for p50, so p50 and p95 come out identical and a pair of
// wildly different measurements reads as one. That matters here because an
// inspect worker produces only a handful of samples per reporting window.
func nearestRank(n, p int) int {
	idx := (n*p+99)/100 - 1

	return max(0, min(idx, n-1))
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
