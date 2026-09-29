// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"golang.org/x/sync/errgroup"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// DefaultWorkers is the apply concurrency. The constraint is the admission
// webhook, not the client: Connection.Validate Lists every existing Connection
// and walks its endpoints on each create, so the create rate is bounded by the
// controller's CPU rather than by how many requests are in flight.
const DefaultWorkers = 16

// Phase names, in apply order. Phases up to and including unbundled
// connections only enqueue the switches they touch; VPCs, attachments and
// peerings fan out to every switch through the Agent controller's
// enqueueAllSwitches, so they go last and are measured separately.
const (
	PhaseNamespaces  = "namespaces"
	PhaseSwitches    = "switches"
	PhaseFabricConns = "fabric-conns"
	PhaseServers     = "servers"
	PhaseServerConns = "server-conns"
	PhaseVPCs        = "vpcs"
	PhaseAttachments = "attachments"
	PhasePeerings    = "peerings"
)

// Phases in apply order.
var Phases = []string{
	PhaseNamespaces,
	PhaseSwitches,
	PhaseFabricConns,
	PhaseServers,
	PhaseServerConns,
	PhaseVPCs,
	PhaseAttachments,
	PhasePeerings,
}

// ApplyOpts controls the apply.
type ApplyOpts struct {
	Workers int
	// Phase stops after the named phase; empty runs them all.
	Phase string
}

// PhaseResult records what one phase did.
type PhaseResult struct {
	Phase   string
	Objects int
	Created int
	Updated int
	Took    time.Duration
}

// Apply creates everything in the loader against the cluster, phase by phase.
// It is idempotent: re-running updates in place rather than failing, so a run
// interrupted partway can simply be repeated.
func Apply(ctx context.Context, kube kclient.Client, l *apiutil.Loader, opts ApplyOpts) ([]PhaseResult, error) {
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.Phase != "" && !slices.Contains(Phases, opts.Phase) {
		return nil, fmt.Errorf("unknown phase %q, valid phases are %v", opts.Phase, Phases) //nolint:err113
	}

	grouped, err := collect(ctx, l)
	if err != nil {
		return nil, err
	}

	results := make([]PhaseResult, 0, len(Phases))

	for _, phase := range Phases {
		objs := grouped[phase]
		if len(objs) == 0 {
			continue
		}

		res, err := applyPhase(ctx, kube, phase, objs, opts.Workers)
		results = append(results, res)
		if err != nil {
			return results, err
		}

		if opts.Phase == phase {
			slog.Info("Stopping after phase", "phase", phase)

			break
		}
	}

	return results, nil
}

func applyPhase(ctx context.Context, kube kclient.Client, phase string, objs []kclient.Object, workers int) (PhaseResult, error) {
	res := PhaseResult{Phase: phase, Objects: len(objs)}
	start := time.Now()

	var created, updated atomic.Int64

	// Progress is worth printing per phase: the create rate visibly decays as
	// the Connection webhook's per-create List grows.
	var done atomic.Int64
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	progressCtx, stopProgress := context.WithCancel(ctx)
	defer stopProgress()

	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				n := done.Load()
				elapsed := time.Since(start)
				rate := float64(n) / elapsed.Seconds()
				slog.Info("Applying", "phase", phase, "done", n, "total", len(objs),
					"rate", fmt.Sprintf("%.1f/s", rate), "elapsed", elapsed.Truncate(time.Second))
			}
		}
	})

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(workers)

	for _, obj := range objs {
		eg.Go(func() error {
			op, err := applyOne(egCtx, kube, obj)
			if err != nil {
				return err
			}

			switch op {
			case ctrlutil.OperationResultCreated:
				created.Add(1)
			case ctrlutil.OperationResultUpdated:
				updated.Add(1)
			case ctrlutil.OperationResultNone,
				ctrlutil.OperationResultUpdatedStatus,
				ctrlutil.OperationResultUpdatedStatusOnly:
			}

			done.Add(1)

			return nil
		})
	}

	err := eg.Wait()

	stopProgress()
	wg.Wait()

	res.Created = int(created.Load())
	res.Updated = int(updated.Load())
	res.Took = time.Since(start)

	if err != nil {
		return res, fmt.Errorf("phase %s: %w", phase, err)
	}

	slog.Info("Applied", "phase", phase, "objects", res.Objects,
		"created", res.Created, "updated", res.Updated, "took", res.Took.Truncate(time.Millisecond))

	return res, nil
}

// applyOne creates or updates a single object, copying spec and labels from
// the generated copy onto whatever is already in the cluster.
func applyOne(ctx context.Context, kube kclient.Client, obj kclient.Object) (ctrlutil.OperationResult, error) {
	var res ctrlutil.OperationResult
	var err error

	switch src := obj.(type) {
	case *wiringapi.VLANNamespace:
		dst := &wiringapi.VLANNamespace{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *vpcapi.IPv4Namespace:
		dst := &vpcapi.IPv4Namespace{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *wiringapi.SwitchGroup:
		dst := &wiringapi.SwitchGroup{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *wiringapi.Switch:
		dst := &wiringapi.Switch{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *wiringapi.Server:
		dst := &wiringapi.Server{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *wiringapi.Connection:
		dst := &wiringapi.Connection{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *vpcapi.VPC:
		dst := &vpcapi.VPC{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *vpcapi.VPCAttachment:
		dst := &vpcapi.VPCAttachment{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	case *vpcapi.VPCPeering:
		dst := &vpcapi.VPCPeering{ObjectMeta: objectKey(src)}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, dst, func() error {
			dst.Spec = src.Spec
			setLabels(dst, src)

			return nil
		})
	default:
		return ctrlutil.OperationResultNone, fmt.Errorf("unsupported object type %T", obj) //nolint:err113
	}

	if err != nil {
		return res, fmt.Errorf("applying %T %s: %w", obj, obj.GetName(), err)
	}

	return res, nil
}

func objectKey(obj kclient.Object) kmetav1.ObjectMeta {
	return kmetav1.ObjectMeta{Name: obj.GetName(), Namespace: obj.GetNamespace()}
}

// setLabels copies the bench labels onto the target without dropping labels
// the Fabric controller maintains.
func setLabels(dst, src kclient.Object) {
	labels := dst.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	maps.Copy(labels, src.GetLabels())

	dst.SetLabels(labels)
}

// collect reads every generated object out of the loader and buckets it by
// apply phase.
func collect(ctx context.Context, l *apiutil.Loader) (map[string][]kclient.Object, error) {
	out := map[string][]kclient.Object{}

	add := func(phase string, obj kclient.Object) {
		out[phase] = append(out[phase], obj)
	}

	vlanNSs := &wiringapi.VLANNamespaceList{}
	if err := l.List(ctx, vlanNSs); err != nil {
		return nil, fmt.Errorf("listing VLAN namespaces: %w", err)
	}
	for idx := range vlanNSs.Items {
		add(PhaseNamespaces, &vlanNSs.Items[idx])
	}

	ipNSs := &vpcapi.IPv4NamespaceList{}
	if err := l.List(ctx, ipNSs); err != nil {
		return nil, fmt.Errorf("listing IPv4 namespaces: %w", err)
	}
	for idx := range ipNSs.Items {
		add(PhaseNamespaces, &ipNSs.Items[idx])
	}

	groups := &wiringapi.SwitchGroupList{}
	if err := l.List(ctx, groups); err != nil {
		return nil, fmt.Errorf("listing switch groups: %w", err)
	}
	for idx := range groups.Items {
		add(PhaseNamespaces, &groups.Items[idx])
	}

	switches := &wiringapi.SwitchList{}
	if err := l.List(ctx, switches); err != nil {
		return nil, fmt.Errorf("listing switches: %w", err)
	}
	for idx := range switches.Items {
		add(PhaseSwitches, &switches.Items[idx])
	}

	servers := &wiringapi.ServerList{}
	if err := l.List(ctx, servers); err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	for idx := range servers.Items {
		add(PhaseServers, &servers.Items[idx])
	}

	conns := &wiringapi.ConnectionList{}
	if err := l.List(ctx, conns); err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}
	for idx := range conns.Items {
		conn := &conns.Items[idx]
		if conn.Spec.Fabric != nil || conn.Spec.Mesh != nil {
			add(PhaseFabricConns, conn)
		} else {
			add(PhaseServerConns, conn)
		}
	}

	vpcs := &vpcapi.VPCList{}
	if err := l.List(ctx, vpcs); err != nil {
		return nil, fmt.Errorf("listing vpcs: %w", err)
	}
	for idx := range vpcs.Items {
		add(PhaseVPCs, &vpcs.Items[idx])
	}

	attaches := &vpcapi.VPCAttachmentList{}
	if err := l.List(ctx, attaches); err != nil {
		return nil, fmt.Errorf("listing vpc attachments: %w", err)
	}
	for idx := range attaches.Items {
		add(PhaseAttachments, &attaches.Items[idx])
	}

	peerings := &vpcapi.VPCPeeringList{}
	if err := l.List(ctx, peerings); err != nil {
		return nil, fmt.Errorf("listing vpc peerings: %w", err)
	}
	for idx := range peerings.Items {
		add(PhasePeerings, &peerings.Items[idx])
	}

	// Stable order so a re-run applies objects the same way, which makes
	// interrupted runs resume predictably.
	for phase := range out {
		sort.Slice(out[phase], func(i, j int) bool {
			return out[phase][i].GetName() < out[phase][j].GetName()
		})
	}

	return out, nil
}
