// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/api/valid"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	fabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/hhfab/bench"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// BenchInitOpts configures `hhfab vlab bench init`.
type BenchInitOpts struct {
	Fabrics []string
	Workers int
	QPS     float32
	Burst   int
	Phase   string
	Force   bool
	DryRun  bool
	Out     string
	// SkipValidate bypasses the local check, leaving it to the admission
	// webhooks. Worth it on a large run, where validating locally costs more
	// than the apply.
	SkipValidate bool
}

// DoVLABBenchInit generates the benchmark topology and applies it to the VLAB
// control node.
func DoVLABBenchInit(ctx context.Context, workDir, cacheDir string, opts BenchInitOpts) error {
	specs, err := bench.ParseFabricSpecs(opts.Fabrics)
	if err != nil {
		return err //nolint:wrapcheck // already describes which --fabric failed
	}

	c, err := load(ctx, workDir, cacheDir, nil, true, HydrateModeIfNotPresent, "")
	if err != nil {
		return err
	}

	gen, l, err := benchGenerate(ctx, c, specs, opts.SkipValidate)
	if err != nil {
		return err
	}

	summarize(specs, gen)

	if opts.DryRun {
		return benchDryRun(ctx, l, opts.Out)
	}

	kube, err := bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	if err := checkCoexistence(ctx, kube, opts.Force); err != nil {
		return err
	}

	start := time.Now()

	results, err := bench.Apply(ctx, kube, l, bench.ApplyOpts{
		Workers: opts.Workers,
		Phase:   opts.Phase,
	})

	reportPhases(results)

	if err != nil {
		return fmt.Errorf("applying: %w", err)
	}

	slog.Info("Benchmark topology applied", "took", time.Since(start).Truncate(time.Millisecond))

	return nil
}

// BenchAgentsOpts configures `hhfab vlab bench agents`.
type BenchAgentsOpts struct {
	Duration       time.Duration
	Interval       time.Duration
	ApplyDelay     time.Duration
	Agents         []string
	APIVia         string
	SyncHeartbeats bool
	QPS            float32
	Burst          int
}

// DoVLABBenchAgents simulates switch agents against the Agent objects in the
// cluster.
func DoVLABBenchAgents(ctx context.Context, workDir, cacheDir string, opts BenchAgentsOpts) error {
	kubeconfig := filepath.Join(workDir, VLABDir, VLABKubeConfig)

	// Discovery needs admin credentials: the per-switch Role grants get and
	// watch on that switch's own Agent, with no list.
	admin, err := bench.NewKubeClient(ctx, kubeconfig, opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	// Each agent's own kubeconfig points at the control VIP on the management
	// network, which the host running the bench has no address on. Reuse
	// whatever address the admin kubeconfig reaches the API by.
	apiServer := ""
	if opts.APIVia == bench.APIViaHostfwd || opts.APIVia == "" {
		cfg, err := kubeutil.NewClientConfig(ctx, kubeconfig)
		if err != nil {
			return fmt.Errorf("reading kubeconfig: %w", err)
		}

		apiServer = cfg.Host
	}

	if err := bench.RunAgents(ctx, admin, bench.AgentsOpts{
		Duration:       opts.Duration,
		Interval:       opts.Interval,
		ApplyDelay:     opts.ApplyDelay,
		Agents:         opts.Agents,
		APIVia:         opts.APIVia,
		APIServer:      apiServer,
		SyncHeartbeats: opts.SyncHeartbeats,
	}); err != nil {
		return fmt.Errorf("running agents: %w", err)
	}

	return nil
}

// BenchHealthOpts configures `hhfab vlab bench health`.
type BenchHealthOpts struct {
	QPS   float32
	Burst int
}

// DoVLABBenchHealth prints the current state of the control plane.
func DoVLABBenchHealth(ctx context.Context, workDir, cacheDir string, opts BenchHealthOpts) error {
	kube, err := bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	// etcd metrics are bound to localhost on the control node and the node
	// stats obviously are too, so those sections need a shell there. Losing it
	// degrades the report rather than failing it.
	run, err := controlNodeRunner(ctx, workDir, cacheDir)
	if err != nil {
		slog.Warn("No control node access, skipping etcd and node sections", "err", err)
	}

	if err := bench.Health(ctx, kube, run, os.Stdout); err != nil {
		return fmt.Errorf("collecting health: %w", err)
	}

	return nil
}

// controlNodeRunner returns a command runner for the VLAB control node.
func controlNodeRunner(ctx context.Context, workDir, cacheDir string) (bench.Runner, error) {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return nil, err
	}

	for _, vm := range vlab.VMs {
		if vm.Type != VMTypeControl {
			continue
		}

		ssh, err := c.SSHVM(ctx, vlab, vm)
		if err != nil {
			return nil, fmt.Errorf("preparing ssh to %s: %w", vm.Name, err)
		}

		return func(ctx context.Context, cmd string) (string, error) {
			stdout, stderr, err := ssh.Run(ctx, cmd)
			if err != nil {
				return "", fmt.Errorf("running %q on %s: %w: %s", cmd, vm.Name, err, strings.TrimSpace(stderr))
			}

			return stdout, nil
		}, nil
	}

	return nil, fmt.Errorf("no control node in the VLAB") //nolint:err113
}

// BenchCleanOpts configures `hhfab vlab bench clean`.
type BenchCleanOpts struct {
	Fabrics []string
	QPS     float32
	Burst   int
}

// DoVLABBenchClean removes everything the benchmark created.
func DoVLABBenchClean(ctx context.Context, workDir, cacheDir string, opts BenchCleanOpts) error {
	if _, err := load(ctx, workDir, cacheDir, nil, false, HydrateModeNever, ""); err != nil {
		return err
	}

	kube, err := bench.NewKubeClient(ctx, filepath.Join(workDir, VLABDir, VLABKubeConfig), opts.QPS, opts.Burst)
	if err != nil {
		return err //nolint:wrapcheck
	}

	start := time.Now()

	results, err := bench.Clean(ctx, kube, opts.Fabrics)

	total := 0
	for _, res := range results {
		total += res.Deleted
	}

	if err != nil {
		return fmt.Errorf("cleaning: %w", err)
	}

	slog.Info("Benchmark objects removed", "count", total, "took", time.Since(start).Truncate(time.Millisecond))

	return nil
}

// benchGenerate builds the topology into an in-memory loader and validates it
// with exactly the Default() plus Validate() the admission webhooks run, so a
// bad shape fails before anything reaches the cluster.
func benchGenerate(ctx context.Context, c *Config, specs []bench.FabricSpec, skipValidate bool) (*bench.Generator, *apiutil.Loader, error) {
	gateways := uint(0)
	for _, node := range c.Nodes {
		if slices.Contains(node.Spec.Roles, fabapi.NodeRoleGateway) {
			gateways++
		}
	}

	alloc, err := bench.NewAllocator(c.Fab, uint(len(c.Controls)), uint(len(c.Nodes)), gateways, specs)
	if err != nil {
		return nil, nil, fmt.Errorf("planning addresses: %w", err)
	}

	fabricCfg, err := fabcomp.GetFabricConfig(c.Fab)
	if err != nil {
		return nil, nil, fmt.Errorf("getting fabric config: %w", err)
	}
	if fabricCfg, err = fabricCfg.Init(fmeta.ExtraValidators{Peering: valid.Peering}); err != nil {
		return nil, nil, fmt.Errorf("initializing fabric config: %w", err)
	}

	l := apiutil.NewLoader()

	profiles := switchprofile.NewDefaultSwitchProfiles()
	if err := profiles.RegisterAll(ctx, l.GetClient(), fabricCfg); err != nil {
		return nil, nil, fmt.Errorf("registering switch profiles: %w", err)
	}

	byName := map[string]*wiringapi.SwitchProfile{}
	for _, sp := range profiles.List() {
		byName[sp.Name] = sp
	}

	gen, err := bench.NewGenerator(specs, alloc, byName)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing generator: %w", err)
	}

	if err := gen.Generate(ctx, l); err != nil {
		return nil, nil, fmt.Errorf("generating: %w", err)
	}

	if skipValidate {
		slog.Warn("Skipping local validation, relying on the admission webhooks")

		return gen, l, nil
	}

	// Validation is quadratic for the same reason the apply is - Connection
	// validation lists every other connection - but against the in-memory
	// client it is slower than the cluster: a full 96 switch fabric takes ~50s
	// locally against ~20s to apply. Worth it to fail before touching the
	// cluster at normal sizes, worth skipping on a big run, which is what
	// --skip-validate is for. The admission webhooks validate either way.
	start := time.Now()

	if err := apiutil.ValidateFabricGateway(ctx, l, fabricCfg); err != nil {
		return nil, nil, fmt.Errorf("validating generated topology: %w", err)
	}

	slog.Info("Validated generated topology", "took", time.Since(start).Truncate(time.Millisecond))

	return gen, l, nil
}

// benchDryRun writes the generated objects as YAML instead of applying them.
func benchDryRun(ctx context.Context, l *apiutil.Loader, out string) error {
	w := os.Stdout
	if out != "" {
		f, err := os.OpenFile(out, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("creating %q: %w", out, err)
		}
		defer f.Close()

		w = f
	}

	if err := apiutil.PrintInclude(ctx, l.GetClient(), w); err != nil {
		return fmt.Errorf("writing objects: %w", err)
	}

	if out != "" {
		slog.Info("Wrote generated topology", "path", out)
	}

	return nil
}

// checkCoexistence refuses to run against a cluster that already has switches
// the bench did not create: the allocator derives its bases from what the
// control plane owns, so real switches would overlap it.
func checkCoexistence(ctx context.Context, kube kclient.Client, force bool) error {
	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}

	foreign := []string{}
	for _, sw := range switches.Items {
		if _, ok := sw.Labels[bench.LabelFabric]; !ok {
			foreign = append(foreign, sw.Name)
		}
	}

	if len(foreign) == 0 {
		return nil
	}

	sort.Strings(foreign)
	if len(foreign) > 5 {
		foreign = append(foreign[:5], "...")
	}

	if !force {
		return fmt.Errorf("cluster already has %d switch(es) the bench did not create (%s); the address allocator assumes it owns the pools, so use a switchless VLAB (hhfab vlab gen --no-switches) or pass --force to proceed anyway", //nolint:err113
			len(switches.Items), strings.Join(foreign, ", "))
	}

	slog.Warn("Proceeding with non-bench switches present", "switches", strings.Join(foreign, ", "))

	return nil
}

// summarize prints what is about to be created, per the plan: counts, then go.
func summarize(specs []bench.FabricSpec, gen *bench.Generator) {
	for _, spec := range specs {
		slog.Info("Fabric", "name", spec.Name,
			"spines", spec.Spines, "leaves", spec.Leaves,
			"serverPorts", spec.ServerPorts, "breakout", spec.ServerBreakout,
			"servers", spec.Servers(), "vpcs", spec.VPCs, "attach", spec.Attach, "peerings", spec.Peerings)
	}

	objects := gen.Objects()

	kinds := make([]string, 0, len(objects))
	for kind := range objects {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	total := uint(0)
	for _, kind := range kinds {
		slog.Info("Objects", "kind", kind, "count", objects[kind])
		total += objects[kind]
	}

	slog.Info("Objects total", "count", total,
		"note", "the Fabric controller will add an Agent and 5 RBAC/Secret objects per switch")
}

func reportPhases(results []bench.PhaseResult) {
	if len(results) == 0 {
		return
	}

	total := time.Duration(0)
	for _, res := range results {
		rate := float64(res.Objects) / res.Took.Seconds()
		slog.Info("Phase", "name", res.Phase, "objects", res.Objects,
			"created", res.Created, "updated", res.Updated,
			"took", res.Took.Truncate(time.Millisecond), "rate", fmt.Sprintf("%.1f/s", rate))
		total += res.Took
	}

	slog.Info("Phases total", "took", total.Truncate(time.Millisecond))
}
