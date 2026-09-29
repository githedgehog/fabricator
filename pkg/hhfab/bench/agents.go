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

// AgentsOpts configures the agent simulation.
type AgentsOpts struct {
	Duration time.Duration
	Interval time.Duration

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

	slog.Info("Preparing agents", "count", len(names), "interval", opts.Interval, "via", opts.APIVia)

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
	kube kclient.Client

	// Identity a real agent reports: InstallID survives reinstalls, BootID a
	// reboot, RunID is fresh per agent process.
	installID string
	runID     string
	bootID    string

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

	kube, err := kclient.New(cfg, kclient.Options{Scheme: scheme})
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

// run heartbeats until the context is done.
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

	a.beat(ctx)

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.beat(ctx)
		}
	}
}

// beat performs one heartbeat write and records what it cost.
func (a *agentSim) beat(ctx context.Context) {
	start := time.Now()

	err := a.heartbeat(ctx)

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

// heartbeat mirrors the real agent's status write: re-Get on conflict, assign
// the whole status, and update through the status subresource.
func (a *agentSim) heartbeat(ctx context.Context) error {
	key := kclient.ObjectKey{Name: a.name, Namespace: kmetav1.NamespaceDefault}

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		agent := &agentapi.Agent{}
		if err := a.kube.Get(ctx, key, agent); err != nil {
			return fmt.Errorf("getting agent: %w", err)
		}

		now := kmetav1.Now()

		agent.Status.Version = agent.Spec.Version.Default
		agent.Status.InstallID = a.installID
		agent.Status.RunID = a.runID
		agent.Status.BootID = a.bootID
		agent.Status.LastHeartbeat = now

		// Report the spec as applied. Without this the whole fabric reads as
		// unhealthy to WaitReady and inspect, and every measurement taken
		// against it would be of a broken fabric rather than a busy one.
		agent.Status.LastAttemptGen = agent.Generation
		agent.Status.LastAttemptTime = now
		agent.Status.LastAppliedGen = agent.Generation
		agent.Status.LastAppliedTime = now

		if agent.Status.Conditions == nil {
			agent.Status.Conditions = []kmetav1.Condition{}
		}
		kmeta.SetStatusCondition(&agent.Status.Conditions, kmetav1.Condition{
			Type:               conditionApplied,
			Status:             kmetav1.ConditionTrue,
			Reason:             "ApplySucceeded",
			ObservedGeneration: agent.Generation,
			Message:            fmt.Sprintf("Config applied, gen=%d", agent.Generation),
		})

		if err := a.kube.Status().Update(ctx, agent); err != nil {
			if kapierrors.IsConflict(err) {
				a.stats.conflicts.Add(1)
			}

			return fmt.Errorf("updating status: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("heartbeat for %s: %w", a.name, err)
	}

	return nil
}

// agentStats aggregates what the simulated fleet is doing.
type agentStats struct {
	heartbeats atomic.Int64
	conflicts  atomic.Int64
	errors     atomic.Int64

	mu       sync.Mutex
	latency  []time.Duration
	lastErr  error
	errCount int
}

func (s *agentStats) observe(took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.latency = append(s.latency, took)
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
	lastErr  error
	errCount int
}

// drain returns the window since the last call, leaving the buffer empty so
// each report covers its own interval.
func (s *agentStats) drain() window {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := window{latency: s.latency, lastErr: s.lastErr, errCount: s.errCount}

	s.latency, s.lastErr, s.errCount = nil, nil, 0

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
				"conflicts", s.conflicts.Load(),
				"errors", s.errors.Load(),
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
		"conflicts", s.conflicts.Load(),
		"errors", s.errors.Load(),
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
