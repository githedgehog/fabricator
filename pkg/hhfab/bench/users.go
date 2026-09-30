// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	rand "math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// LabelTouch is set to a fresh timestamp on every update.
//
// The value has to change on every write. The apiserver skips an update that
// leaves the stored object byte-identical - no resourceVersion bump, no watch
// event, no reconcile - so a mutation that round-tripped to the same bytes
// would generate admission load only, and silently miss the reconcile fan-out
// that is half of what a user costs. Unix nanos are digits, so the value is
// always a legal label.
const LabelTouch = "bench.githedgehog.com/touch"

// The kinds a user can touch. Both go through the same primitive but stress
// different halves of the write path: VPCAttachment has cheap admission and the
// widest reconcile fan-out (enqueueAllSwitches puts every switch on the queue),
// while Connection routes through the O(n) Connection.Validate that dominates
// init and enqueues only related switches.
const (
	KindVPCAttachment = "vpcattachment"
	KindConnection    = "connection"
)

// The inspect commands worth running. These are the expensive ones: lldp and
// bgp are O(N^2) - per switch they Get the Agent then List every Switch, Server
// and Connection - bfd calls the bgp path and fetches each Agent twice, and mac
// Lists every Agent with its full status in a single response. The rest are one
// or a few Lists and would never be the bottleneck.
const (
	InspectLLDP = "lldp"
	InspectBGP  = "bgp"
	InspectBFD  = "bfd"
	InspectMAC  = "mac"
)

var (
	// DefaultUserKinds and DefaultUserInspects are what a run touches unless
	// told otherwise.
	DefaultUserKinds    = []string{KindVPCAttachment, KindConnection}
	DefaultUserInspects = []string{InspectLLDP, InspectBGP, InspectBFD, InspectMAC}
)

const (
	DefaultUpdateWorkers  = 4
	DefaultUpdateSleep    = 10 * time.Second
	DefaultInspectWorkers = 4
	DefaultInspectSleep   = 15 * time.Second
	// DefaultOpTimeout bounds one operation. The O(N^2) inspects may simply not
	// return at this scale, and without a bound a single stuck lldp would stall
	// a worker for the whole run while looking like it was merely slow.
	DefaultOpTimeout = 5 * time.Minute
)

// UsersOpts configures a simulated operator load.
type UsersOpts struct {
	Kubeconfig     string
	Duration       time.Duration
	UpdateWorkers  int
	UpdateSleep    time.Duration
	InspectWorkers int
	InspectSleep   time.Duration
	Kinds          []string
	Inspects       []string
	Fabrics        []string
	OpTimeout      time.Duration
	// InspectOneSwitch scopes lldp, bgp and bfd to a single random switch.
	// Off by default, because an operator runs them unscoped and the per-switch
	// work is itself O(N) - scoping is not a smaller version of the same
	// operation but a far cheaper one.
	InspectOneSwitch bool
	QPS              float32
	Burst            int
}

func (o *UsersOpts) setDefaults() {
	if o.UpdateWorkers < 0 {
		o.UpdateWorkers = 0
	}
	if o.InspectWorkers < 0 {
		o.InspectWorkers = 0
	}
	if o.UpdateSleep <= 0 {
		o.UpdateSleep = DefaultUpdateSleep
	}
	if o.InspectSleep <= 0 {
		o.InspectSleep = DefaultInspectSleep
	}
	if o.OpTimeout <= 0 {
		o.OpTimeout = DefaultOpTimeout
	}
	if len(o.Kinds) == 0 {
		o.Kinds = DefaultUserKinds
	}
	if len(o.Inspects) == 0 {
		o.Inspects = DefaultUserInspects
	}
}

func (o UsersOpts) validate() error {
	for _, kind := range o.Kinds {
		if kind != KindVPCAttachment && kind != KindConnection {
			return fmt.Errorf("unknown kind %q, want %s or %s", kind, KindVPCAttachment, KindConnection) //nolint:err113
		}
	}

	for _, name := range o.Inspects {
		if !slices.Contains(DefaultUserInspects, name) {
			return fmt.Errorf("unknown inspect %q, want one of %s", //nolint:err113
				name, strings.Join(DefaultUserInspects, ", "))
		}
	}

	if o.UpdateWorkers == 0 && o.InspectWorkers == 0 {
		return fmt.Errorf("nothing to do: both update and inspect workers are zero") //nolint:err113
	}

	return nil
}

// targets are the object names a run may touch, collected once at startup.
//
// Names only, and listed once: holding the objects would cost hundreds of
// megabytes, and listing per operation would make the bench its own load rather
// than a measurement of the cluster's.
type targets struct {
	byKind   map[string][]string
	switches []string
}

// discoverTargets lists what a run may touch. Each half is only discovered when
// a worker will use it, so an inspect-only run does not demand attachments and
// connections it will never touch, nor an update-only run switches it will
// never inspect.
func discoverTargets(ctx context.Context, kube kclient.Client, opts UsersOpts) (*targets, error) {
	out := &targets{byKind: map[string][]string{}}

	if opts.UpdateWorkers > 0 {
		if err := discoverUpdateTargets(ctx, kube, opts, out); err != nil {
			return nil, err
		}
	}

	if opts.InspectWorkers > 0 && len(opts.Inspects) > 0 {
		names, err := benchSwitchNames(ctx, kube, opts.Fabrics)
		if err != nil {
			return nil, err
		}

		if len(names) == 0 {
			return nil, fmt.Errorf("no bench switches found; run bench init first") //nolint:err113
		}

		out.switches = names
	}

	return out, nil
}

func discoverUpdateTargets(ctx context.Context, kube kclient.Client, opts UsersOpts, out *targets) error {
	for _, kind := range opts.Kinds {
		names, err := benchNames(ctx, kube, kind, opts.Fabrics)
		if err != nil {
			return err
		}

		if len(names) == 0 {
			return fmt.Errorf("no bench %s objects found; run bench init first", kind) //nolint:err113
		}

		out.byKind[kind] = names
	}

	return nil
}

// benchNames lists the bench-owned objects of one kind, keeping only names.
func benchNames(ctx context.Context, kube kclient.Client, kind string, fabrics []string) ([]string, error) {
	names := []string{}

	switch kind {
	case KindVPCAttachment:
		list := &vpcapi.VPCAttachmentList{}
		if err := kube.List(ctx, list, kclient.HasLabels{LabelFabric}); err != nil {
			return nil, fmt.Errorf("listing vpcattachments: %w", err)
		}

		for _, item := range list.Items {
			if wantFabric(item.Labels[LabelFabric], fabrics) {
				names = append(names, item.Name)
			}
		}
	case KindConnection:
		list := &wiringapi.ConnectionList{}
		if err := kube.List(ctx, list, kclient.HasLabels{LabelFabric}); err != nil {
			return nil, fmt.Errorf("listing connections: %w", err)
		}

		for _, item := range list.Items {
			if wantFabric(item.Labels[LabelFabric], fabrics) {
				names = append(names, item.Name)
			}
		}
	default:
		return nil, fmt.Errorf("unknown kind %q", kind) //nolint:err113
	}

	return names, nil
}

func newObject(kind string) kclient.Object {
	switch kind {
	case KindVPCAttachment:
		return &vpcapi.VPCAttachment{}
	case KindConnection:
		return &wiringapi.Connection{}
	}

	return nil
}

// RunUsers drives simulated operators against the cluster until the duration
// expires: some changing objects through the API as kubectl or a GitOps
// pipeline would, others running inspect.
//
// Every worker holds its own uncached, transport-isolated client. That is the
// point of the exercise: a real operator is a separate process with a cold
// cache, so a shared informer would measure something nobody experiences.
func RunUsers(ctx context.Context, admin kclient.Client, opts UsersOpts) error {
	opts.setDefaults()

	if err := opts.validate(); err != nil {
		return err
	}

	found, err := discoverTargets(ctx, admin, opts)
	if err != nil {
		return err
	}

	for kind, names := range found.byKind {
		slog.Info("Targets", "kind", kind, "objects", len(names))
	}

	stats := &userStats{ops: map[string]*opStat{}}

	updaters, err := newUserSims(ctx, opts, found, stats, opts.UpdateWorkers, "update")
	if err != nil {
		return err
	}

	inspectors, err := newUserSims(ctx, opts, found, stats, opts.InspectWorkers, "inspect")
	if err != nil {
		return err
	}

	// Ends the run by closing stop rather than cancelling, so in-flight requests
	// finish instead of being aborted mid-body - see RunAgents.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := make(chan struct{})

	if opts.Duration > 0 {
		timer := time.AfterFunc(opts.Duration, func() { close(stop) })
		defer timer.Stop()
	}

	slog.Info("Starting users",
		"updateWorkers", opts.UpdateWorkers, "updateSleep", opts.UpdateSleep,
		"inspectWorkers", opts.InspectWorkers, "inspectSleep", opts.InspectSleep,
		"kinds", strings.Join(opts.Kinds, ","), "inspects", strings.Join(opts.Inspects, ","),
		"duration", opts.Duration)

	start := time.Now()

	var wg sync.WaitGroup

	for _, sim := range updaters {
		wg.Go(func() { sim.run(runCtx, stop, opts, sim.update) })
	}

	for _, sim := range inspectors {
		wg.Go(func() { sim.run(runCtx, stop, opts, sim.inspect) })
	}

	wg.Go(func() { stats.report(runCtx, stop) })

	wg.Wait()

	stats.summary(time.Since(start))

	return nil
}

// userSim is one simulated operator.
type userSim struct {
	kube      kclient.WithWatch
	targets   *targets
	stats     *userStats
	sleep     time.Duration
	role      string
	oneSwitch bool
	// fabricScoped is set when --only narrowed the run to some fabrics.
	fabricScoped bool
}

func newUserSims(ctx context.Context, opts UsersOpts, found *targets, stats *userStats, count int, role string) ([]*userSim, error) {
	sleep := opts.UpdateSleep
	if role == "inspect" {
		sleep = opts.InspectSleep
	}

	sims := make([]*userSim, 0, count)

	for idx := range count {
		kube, err := NewKubeClientIsolated(ctx, opts.Kubeconfig, opts.QPS, opts.Burst)
		if err != nil {
			return nil, fmt.Errorf("preparing %s worker %d: %w", role, idx, err)
		}

		sims = append(sims, &userSim{
			kube: kube, targets: found, stats: stats, sleep: sleep, role: role,
			oneSwitch:    opts.InspectOneSwitch,
			fabricScoped: len(opts.Fabrics) > 0,
		})
	}

	return sims, nil
}

// run does one operation, sleeps, repeats. Sleep-based rather than rate-based
// so it reads like operator think time: load scales with worker count and each
// worker waits after finishing, however long that took.
func (u *userSim) run(ctx context.Context, stop <-chan struct{}, opts UsersOpts, do func(context.Context, UsersOpts)) {
	// Spread the first operation so workers do not move in lockstep.
	select {
	case <-ctx.Done():
		return
	case <-stop:
		return
	case <-time.After(time.Duration(rand.Int64N(int64(u.sleep)))): //nolint:gosec // jitter, not a secret
	}

	for {
		do(ctx, opts)

		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-time.After(u.sleep):
		}
	}
}

// update sets the touch label on one randomly chosen object.
//
// It is wrapped in RetryOnConflict because the controller may be writing the
// same object: a conflict is normal contention, not a failure.
func (u *userSim) update(ctx context.Context, opts UsersOpts) {
	kind := pick(opts.Kinds)

	names := u.targets.byKind[kind]
	if len(names) == 0 {
		return
	}

	name := pick(names)

	opCtx, cancel := context.WithTimeout(ctx, opts.OpTimeout)
	defer cancel()

	start := time.Now()

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj := newObject(kind)

		if err := u.kube.Get(opCtx, kclient.ObjectKey{
			Namespace: kmetav1.NamespaceDefault, Name: name,
		}, obj); err != nil {
			return fmt.Errorf("getting %s %s: %w", kind, name, err)
		}

		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}

		labels[LabelTouch] = strconv.FormatInt(time.Now().UnixNano(), 10)
		obj.SetLabels(labels)

		if err := u.kube.Update(opCtx, obj); err != nil {
			if kapierrors.IsConflict(err) {
				u.stats.conflict()
			}

			return fmt.Errorf("updating %s %s: %w", kind, name, err)
		}

		return nil
	})

	u.stats.record(ctx, "update:"+kind, time.Since(start), err)
}

// inspect runs one inspect command against a random bench target.
//
// The rendered output is discarded rather than produced: formatting is local
// CPU and would make the host the bottleneck, while the API round trips are
// what the cluster actually pays for.
func (u *userSim) inspect(ctx context.Context, opts UsersOpts) {
	name := pick(opts.Inspects)

	opCtx, cancel := context.WithTimeout(ctx, opts.OpTimeout)
	defer cancel()

	start := time.Now()
	err := u.runInspect(opCtx, name)

	u.stats.record(ctx, "inspect:"+name, time.Since(start), err)
}

// inspectSwitches is the switch list handed to lldp, bgp and bfd.
//
// An empty list means every switch, which is what an operator gets by typing
// `kubectl fabric inspect lldp`. Since the per-switch work is itself O(N),
// scoping to one switch is not a smaller version of the same operation - it is
// roughly N^2 times cheaper, and measuring it would understate a real user by
// orders of magnitude.
//
// With --only, "unscoped" means every switch in the selected fabrics rather
// than every switch in the cluster, or the filter would be silently ignored.
// mac is the exception by nature: it lists every Agent whatever it is given.
func (u *userSim) inspectSwitches() []string {
	switch {
	case u.oneSwitch:
		return []string{pick(u.targets.switches)}
	case u.fabricScoped:
		return u.targets.switches
	}

	return nil
}

func (u *userSim) runInspect(ctx context.Context, name string) error {
	sw := u.inspectSwitches()

	switch name {
	case InspectLLDP:
		if _, err := inspect.LLDP(ctx, u.kube, inspect.LLDPIn{
			Switches: sw, Fabric: true, Server: true,
		}); err != nil {
			return fmt.Errorf("inspect lldp: %w", err)
		}
	case InspectBGP:
		if _, err := inspect.BGP(ctx, u.kube, inspect.BGPIn{Switches: sw}); err != nil {
			return fmt.Errorf("inspect bgp: %w", err)
		}
	case InspectBFD:
		if _, err := inspect.BFD(ctx, u.kube, inspect.BFDIn{Switches: sw}); err != nil {
			return fmt.Errorf("inspect bfd: %w", err)
		}
	case InspectMAC:
		// A random MAC costs exactly the same List(Agent) as a real one, and
		// finding a real one would mean reading agent status to do it.
		if _, err := inspect.MAC(ctx, u.kube, inspect.MACIn{Value: randomMAC()}); err != nil {
			return fmt.Errorf("inspect mac: %w", err)
		}
	default:
		return fmt.Errorf("unknown inspect %q", name) //nolint:err113
	}

	return nil
}

func pick[T any](items []T) T {
	return items[rand.IntN(len(items))] //nolint:gosec // selection, not a secret
}

func randomMAC() string {
	buf := make([]byte, 6)
	for idx := range buf {
		buf[idx] = byte(rand.IntN(256)) //nolint:gosec // synthetic address, not a secret
	}

	// Locally administered, unicast: never a real burned-in address.
	buf[0] = buf[0]&0xfe | 0x02

	parts := make([]string, len(buf))
	for idx, b := range buf {
		parts[idx] = fmt.Sprintf("%02x", b)
	}

	return strings.Join(parts, ":")
}
