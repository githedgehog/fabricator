// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/errgroup"
	appsapi "k8s.io/api/apps/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	disruptionBaseline     = 2 * time.Minute
	disruptionTouchRounds  = 12
	disruptionVPCCycles    = 5
	disruptionCtrlRestarts = 3
	// disruptionMinEntries is how many IRB VLAN entries each leaf of the pair must hold alone:
	// with fewer the allocator hands the lone entry the lowest free id every time and a
	// renumbering never shows.
	disruptionMinEntries   = 3
	disruptionSettleQuiet  = 10 * time.Second
	disruptionSettleMax    = 5 * time.Minute
	disruptionWarmup       = 5 * time.Second
	disruptionTrailGrace   = 2 * time.Second
	disruptionMaxClockSkew = 500 * time.Millisecond
	disruptionReinstallMax = 30 * time.Minute
	// after the reinstalled leaf has applied its config the dataplane may still converge:
	// loss is only asserted from disruptionRecovery after that on, and for disruptionWatchAfter
	disruptionRecovery   = 30 * time.Second
	disruptionWatchAfter = 30 * time.Second

	disruptionExternalTarget = "1.0.0.1"
	disruptionTouchLabel     = meta.BenchTouchLabel
)

func makeDisruptionTestSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Disruption Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name:      "Disruption Under Traffic",
			F:         disruptionTest,
			SkipFlags: SkipFlags{NoServers: true},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// probe classes for the reinstall: what the traffic is allowed to lose while the leaf is away
const (
	probeStrict = "strict" // never crosses the leaf: no loss at any time
	probeHard   = "hard"   // has an endpoint attached to the leaf alone: gone while it is away
	probeSoft   = "soft"   // fails over to the other leaf, or its path isn't known: bounded loss
)

type disruptionEnv struct {
	testCtx *VPCPeeringTestCtx
	kube    kclient.Client
	sc      *disruptionScenario
	alloc   *vpcSubnetAllocator
	tw      *TrafficWatch
	aw      *AgentWatch
	// probeClass maps a probe name to the class it has when the second leaf is reinstalled
	probeClass map[string]string
	// cursor is where the next traffic check starts, so every moment is checked once
	cursor      time.Time
	evidenceDir string
	// touched holds the leaves that got a touch label, to remove it in the end
	touched map[string]bool
	// noted holds the notes already in the result
	noted map[string]bool
}

// disruptionTest runs the common changes under background traffic and fails on any disruption
// the change was not expected to cause, on a spec change it was not expected to make and on an
// Agent that doesn't report the generation as applied.
func disruptionTest(ctx context.Context, testCtx *VPCPeeringTestCtx, _ *ConnectivityMatrix) (bool, []RevertFunc, error) {
	kube := testCtx.kube

	if testCtx.setupOpts.VPCMode != vpcapi.VPCModeL2VNI {
		return true, nil, fmt.Errorf("VPC mode is %q, ESLAG servers only take part in l2vni", testCtx.setupOpts.VPCMode) //nolint:goerr113
	}
	if testCtx.extName == "" {
		return true, nil, errNoExternals
	}

	sc, err := discoverDisruptionScenario(ctx, kube)
	if err != nil {
		return true, nil, err
	}
	slog.Info("Disruption scenario", "scenario", sc.describe())

	env := &disruptionEnv{
		testCtx:     testCtx,
		kube:        kube,
		sc:          sc,
		probeClass:  map[string]string{},
		touched:     map[string]bool{},
		noted:       map[string]bool{},
		evidenceDir: filepath.Join(testCtx.vlabCfg.WorkDir, ShowTechOutputDir, sanitizeNameForFolder("Disruption Suite"), sanitizeNameForFolder("Disruption Under Traffic"), "disruption"),
	}

	reverts := []RevertFunc{env.cleanup}

	if err := env.setup(ctx); err != nil {
		return false, reverts, err
	}

	env.aw = NewAgentWatch(kube)
	env.aw.Start(ctx)
	defer env.aw.Stop()
	defer func() {
		if err := env.tw.Stop(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("Stopping background traffic failed", "err", err)
		}
	}()

	if err := env.tw.Start(ctx); err != nil {
		return false, reverts, fmt.Errorf("starting background traffic: %w", err)
	}

	if err := env.baseline(ctx); err != nil {
		return false, reverts, err
	}

	var errs []error
	for _, group := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"create and delete an unattached VPC", env.opCreateDeleteVPC},
		{"touch label on the leaves of the pair", env.opTouchLeaves},
		{"fabric-ctrl restart", env.opRestartController},
		{"agent reinstall", env.opReinstall},
	} {
		slog.Info("Disruption operation", "operation", group.name)
		if err := group.run(ctx); err != nil {
			slog.Error("FAIL", "operation", group.name, "error", err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", group.name, err))

			continue
		}
		slog.Info("PASS", "operation", group.name)
	}

	if drift, err := env.tw.ClockDrift(ctx); err != nil {
		slog.Warn("Measuring server clock drift failed", "err", err)
	} else if drift > disruptionMaxClockSkew {
		slog.Warn("Server clocks drifted during the run, the times of lost pings are approximate", "drift", drift)
	}

	return false, reverts, errors.Join(errs...)
}

func discoverDisruptionScenario(ctx context.Context, kube kclient.Client) (*disruptionScenario, error) {
	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return nil, fmt.Errorf("listing switches: %w", err)
	}
	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns); err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}
	attaches := &vpcapi.ExternalAttachmentList{}
	if err := kube.List(ctx, attaches); err != nil {
		return nil, fmt.Errorf("listing external attachments: %w", err)
	}

	byName := map[string]wiringapi.Connection{}
	for _, c := range conns.Items {
		byName[c.Name] = c
	}
	extLeaves := map[string]int{}
	for _, att := range attaches.Items {
		c, ok := byName[att.Spec.Connection]
		if !ok {
			continue
		}
		sws, _, _, _, err := c.Spec.Endpoints()
		if err != nil {
			continue
		}
		for _, sw := range sws {
			extLeaves[sw]++
		}
	}

	return pickDisruptionScenario(switches.Items, conns.Items, extLeaves)
}

func (env *disruptionEnv) newVPC(name string) (*vpcapi.VPC, error) {
	cidr, err := env.alloc.allocSubnet()
	if err != nil {
		return nil, err
	}
	vlan, err := env.alloc.allocVLAN()
	if err != nil {
		return nil, err
	}

	return &vpcapi.VPC{
		ObjectMeta: kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault},
		Spec: vpcapi.VPCSpec{
			Mode: vpcapi.VPCModeL2VNI,
			Subnets: map[string]*vpcapi.VPCSubnet{
				"subnet-01": {Subnet: cidr.String(), VLAN: vlan, DHCP: vpcapi.VPCDHCP{Enable: true}},
			},
		},
	}, nil
}

type disruptionVPC struct {
	vpc    *vpcapi.VPC
	server disruptionServer
}

// setup builds the VPCs the traffic runs between, adds the entries that live on a single leaf,
// and starts the traffic. Layout: a VPC per server attached to a leaf alone (two per leaf at
// most), one with an ESLAG server that is peered with the external. Two single-leaf VPCs of a
// leaf are peered with each other, so that traffic crosses entries that only that leaf holds;
// a lone one is peered with the ESLAG VPC. A VPC peered with one on both leaves is requested by
// both, so it doesn't count as a single-leaf entry.
func (env *disruptionEnv) setup(ctx context.Context) error {
	testCtx, kube, sc := env.testCtx, env.kube, env.sc

	if err := hhfctl.VPCWipeWithClient(ctx, kube); err != nil {
		return fmt.Errorf("wiping VPCs and peerings: %w", err)
	}

	alloc, err := newVPCSubnetAllocator(ctx, kube, testCtx.setupOpts.VLANNamespace, testCtx.setupOpts.IPv4Namespace)
	if err != nil {
		return fmt.Errorf("setting up VPC subnet allocator: %w", err)
	}
	env.alloc = alloc

	singles := make([]disruptionVPC, 0, 2*len(sc.Leaves))
	leafOnly := map[string]int{}
	var intra [][2]string
	var lone []string
	for i, leaf := range sc.Leaves {
		var mine []disruptionVPC
		for j, srv := range sc.SingleLeaf[leaf][:min(2, len(sc.SingleLeaf[leaf]))] {
			vpc, err := env.newVPC(fmt.Sprintf("dt-%d%c", i+1, 'a'+j))
			if err != nil {
				return err
			}
			mine = append(mine, disruptionVPC{vpc: vpc, server: srv})
		}
		switch len(mine) {
		case 2:
			intra = append(intra, [2]string{mine[0].vpc.Name, mine[1].vpc.Name})
			leafOnly[leaf] = 2
		case 1:
			lone = append(lone, mine[0].vpc.Name)
		}
		singles = append(singles, mine...)
	}
	eslagVPC, err := env.newVPC("dt-e")
	if err != nil {
		return err
	}
	all := append(slices.Clone(singles), disruptionVPC{vpc: eslagVPC, server: sc.ESLAG[0]})

	for _, d := range all {
		if _, err := CreateOrUpdateVpc(ctx, kube, d.vpc); err != nil {
			return fmt.Errorf("creating VPC %s: %w", d.vpc.Name, err)
		}
	}
	for _, d := range all {
		if err := kube.Create(ctx, makeVPCAttachment(d.server.Conn.Name, d.vpc.Name, "subnet-01")); err != nil {
			return fmt.Errorf("attaching VPC %s: %w", d.vpc.Name, err)
		}
	}

	time.Sleep(15 * time.Second)
	if err := WaitReady(ctx, kube, testCtx.wrOpts); err != nil {
		return fmt.Errorf("waiting for ready after creating VPCs: %w", err)
	}

	g := &errgroup.Group{}
	for _, d := range all {
		g.Go(func() error {
			cmd, err := GetServerNetconfCmd(&d.server.Conn, ServerNetconfOpts{VLAN: d.vpc.Spec.Subnets["subnet-01"].VLAN, HashPolicy: testCtx.setupOpts.HashPolicy})
			if err != nil {
				return fmt.Errorf("netconf for %s: %w", d.server.Name, err)
			}
			ssh, err := testCtx.getSSH(ctx, d.server.Name)
			if err != nil {
				return fmt.Errorf("getting SSH for %s: %w", d.server.Name, err)
			}
			if _, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
				return fmt.Errorf("hhnet cleanup on %s: %w: %s", d.server.Name, err, stderr)
			}
			stdout, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet "+cmd)
			if err != nil {
				return fmt.Errorf("hhnet on %s: %w: %s", d.server.Name, err, stderr)
			}
			slog.Debug("Server configured", "server", d.server.Name, "addr", strings.TrimSpace(stdout))

			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("configuring server networking: %w", err)
	}

	vpcPeerings := map[string]*vpcapi.VPCPeeringSpec{}
	extPeerings := map[string]*vpcapi.ExternalPeeringSpec{}
	for _, pair := range intra {
		appendVpcPeeringSpecByName(vpcPeerings, pair[0], pair[1], []string{}, []string{})
	}
	for _, name := range lone {
		appendVpcPeeringSpecByName(vpcPeerings, name, eslagVPC.Name, []string{}, []string{})
	}
	appendExtPeeringSpecByName(extPeerings, eslagVPC.Name, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	if err := DoSetupPeerings(ctx, kube, vpcPeerings, extPeerings, map[string]*gwapi.PeeringSpec{}, true); err != nil {
		return fmt.Errorf("setting up peerings: %w", err)
	}

	matrix, err := BuildConnectivityMatrixFromCluster(ctx, kube, func(server string) (*sshutil.Config, error) {
		return testCtx.getSSH(ctx, server)
	})
	if err != nil {
		return fmt.Errorf("building connectivity matrix: %w", err)
	}

	// entries on a single leaf: more VPCs attached to the connection of a server on that leaf
	// alone. No server is configured for them, the controller allocates for them all the same.
	for i, leaf := range sc.Leaves {
		if len(sc.SingleLeaf[leaf]) == 0 {
			continue
		}
		for n := range disruptionMinEntries - leafOnly[leaf] {
			vpc, err := env.newVPC(fmt.Sprintf("dt-f%d-%d", i+1, n+1))
			if err != nil {
				return err
			}
			if _, err := CreateOrUpdateVpc(ctx, kube, vpc); err != nil {
				return fmt.Errorf("creating VPC %s: %w", vpc.Name, err)
			}
			if err := kube.Create(ctx, makeVPCAttachment(sc.SingleLeaf[leaf][0].Conn.Name, vpc.Name, "subnet-01")); err != nil {
				return fmt.Errorf("attaching VPC %s: %w", vpc.Name, err)
			}
		}
	}

	if _, err := WaitAgentsSettled(ctx, kube, disruptionSettleQuiet, disruptionSettleMax); err != nil {
		return err
	}
	if err := WaitReady(ctx, kube, testCtx.wrOpts); err != nil {
		return fmt.Errorf("waiting for ready after adding entries: %w", err)
	}

	specs, err := SnapshotAgentSpecs(ctx, kube)
	if err != nil {
		return err
	}
	entries := singleLeafEntries(specs, sc.Leaves)
	for _, leaf := range sc.Leaves {
		if len(sc.SingleLeaf[leaf]) == 0 {
			continue
		}
		if len(entries[leaf]) < disruptionMinEntries {
			return fmt.Errorf("leaf %s holds %d IRB VLAN entries alone (%v), need at least %d for a renumbering to show", //nolint:goerr113
				leaf, len(entries[leaf]), entries[leaf], disruptionMinEntries)
		}
	}
	slog.Info("Entries held by a single leaf of the pair", "entries", entries)

	probes, err := env.buildProbes(ctx, matrix, []disruptionVPC{{vpc: eslagVPC, server: sc.ESLAG[0]}})
	if err != nil {
		return err
	}
	if len(probes) == 0 {
		return fmt.Errorf("no traffic pair expected to be reachable, nothing to watch") //nolint:goerr113
	}
	env.tw = NewTrafficWatch(probes)

	return nil
}

// singleLeafEntries returns, per leaf of the pair, the IRB VLAN entries its Agent holds that the
// other leaf's Agent doesn't.
func singleLeafEntries(specs AgentSpecs, leaves [2]string) map[string][]string {
	irb := func(leaf string) map[string]any {
		cat, _ := specs[leaf]["catalog"].(map[string]any)
		ids, _ := cat["irbVLANs"].(map[string]any)

		return ids
	}
	out := map[string][]string{}
	for i, leaf := range leaves {
		other := irb(leaves[1-i])
		for name := range irb(leaf) {
			if _, shared := other[name]; !shared {
				out[leaf] = append(out[leaf], name)
			}
		}
		slices.Sort(out[leaf])
	}

	return out
}

// buildProbes turns what the matrix expects to be reachable into background pings. A pair where
// either end sits on the second leaf alone is hard for the reinstall, one with an ESLAG server
// or an external soft, the rest strict.
func (env *disruptionEnv) buildProbes(ctx context.Context, matrix *ConnectivityMatrix, externalSources []disruptionVPC) ([]TrafficProbe, error) {
	sc := env.sc
	target := sc.Leaves[1]
	onTarget := map[string]bool{}
	for _, s := range sc.SingleLeaf[target] {
		onTarget[s.Name] = true
	}
	onESLAG := map[string]bool{}
	for _, s := range sc.ESLAG {
		onESLAG[s.Name] = true
	}
	classify := func(servers ...string) string {
		class := probeStrict
		for _, s := range servers {
			switch {
			case onTarget[s]:
				return probeHard
			case onESLAG[s]:
				class = probeSoft
			}
		}

		return class
	}

	probes := make([]TrafficProbe, 0, len(matrix.AllEndpoints))
	for _, src := range matrix.AllEndpoints {
		if src.Server == nil {
			continue
		}
		ssh, err := env.testCtx.getSSH(ctx, src.Server.Name)
		if err != nil {
			return nil, fmt.Errorf("getting SSH for %s: %w", src.Server.Name, err)
		}
		for _, dst := range matrix.AllEndpoints {
			if dst.Server == nil || src.Server.Name == dst.Server.Name {
				continue
			}
			if matrix.Lookup(src, dst, ProtoPort{}).Verdict != VerdictAllow {
				continue
			}
			name := fmt.Sprintf("%s -> %s", src.Server.Name, dst.Server.Name)
			probes = append(probes, TrafficProbe{Name: name, From: src.Server.Name, FromSSH: ssh, To: dst.Server.IP})
			env.probeClass[name] = classify(src.Server.Name, dst.Server.Name)
		}
	}

	for _, d := range externalSources {
		var src *Endpoint
		for _, ep := range matrix.AllEndpoints {
			if ep.Server != nil && ep.Server.Name == d.server.Name {
				src = ep

				break
			}
		}
		if src == nil || !matrix.hasExternalCurlAllow(src) {
			continue
		}
		ssh, err := env.testCtx.getSSH(ctx, d.server.Name)
		if err != nil {
			return nil, fmt.Errorf("getting SSH for %s: %w", d.server.Name, err)
		}
		// the matrix probes externals with curl; whether this one answers ping is not known up front
		if _, stderr, err := ssh.Run(ctx, "ping -c 3 -W 1 "+disruptionExternalTarget); err != nil {
			slog.Debug("Ping to the external failed", "server", d.server.Name, "err", err, "stderr", stderr)
			env.testCtx.addNote(fmt.Sprintf("external not probed: no ping reply from %s (from %s)", disruptionExternalTarget, d.server.Name))

			continue
		}
		name := fmt.Sprintf("%s -> external", d.server.Name)
		probes = append(probes, TrafficProbe{Name: name, From: d.server.Name, FromSSH: ssh, To: netip.MustParseAddr(disruptionExternalTarget)})
		env.probeClass[name] = probeSoft
		if onTarget[d.server.Name] {
			env.probeClass[name] = probeHard
		}
	}

	slog.Info("Background traffic", "probes", len(probes))

	return probes, nil
}

func (env *disruptionEnv) cleanup(ctx context.Context) error {
	var errs []error
	for leaf := range env.touched {
		sw := &wiringapi.Switch{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: leaf, Namespace: kmetav1.NamespaceDefault}, sw); err != nil {
			errs = append(errs, fmt.Errorf("getting switch %s: %w", leaf, err))

			continue
		}
		patch := kclient.MergeFrom(sw.DeepCopy())
		delete(sw.Labels, disruptionTouchLabel)
		if err := env.kube.Patch(ctx, sw, patch); err != nil {
			errs = append(errs, fmt.Errorf("removing touch label from %s: %w", leaf, err))
		}
	}

	if err := hhfctl.VPCWipeWithClient(ctx, env.kube); err != nil {
		errs = append(errs, fmt.Errorf("wiping VPCs and peerings: %w", err))
	}

	servers := map[string]bool{}
	for _, s := range env.sc.ESLAG[:1] {
		servers[s.Name] = true
	}
	for _, list := range env.sc.SingleLeaf {
		if len(list) > 0 {
			servers[list[0].Name] = true
		}
	}
	for _, name := range sortedKeys(servers) {
		ssh, err := env.testCtx.getSSH(ctx, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("getting SSH for cleanup of %s: %w", name, err))

			continue
		}
		if _, _, err := ssh.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
			errs = append(errs, fmt.Errorf("hhnet cleanup on %s: %w", name, err))
		}
	}

	if env.alloc != nil {
		env.alloc.stop()
	}

	return errors.Join(errs...)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting: %w", ctx.Err())
	case <-time.After(d):
		return nil
	}
}

// baseline watches the traffic with no change. Loss makes it run once more; loss again leaves the
// suite without a verdict: the traffic isn't clean before any change, so nothing can be told from it.
func (env *disruptionEnv) baseline(ctx context.Context) error {
	if err := sleepCtx(ctx, disruptionWarmup); err != nil {
		return err
	}

	var lost []LostPing
	for attempt := 1; attempt <= 2; attempt++ {
		from := time.Now()
		if err := sleepCtx(ctx, disruptionBaseline); err != nil {
			return err
		}
		to := time.Now()
		if err := sleepCtx(ctx, disruptionTrailGrace); err != nil {
			return err
		}
		tl, err := env.tw.Snapshot(ctx)
		if err != nil {
			return err
		}
		if broken := BrokenProbes(tl); len(broken) > 0 {
			return fmt.Errorf("no ping reply at all on %v during the baseline", broken) //nolint:goerr113
		}
		lost = LostPings(tl, from, to)
		env.cursor = to
		if len(lost) == 0 {
			slog.Info("Baseline traffic is clean", "duration", disruptionBaseline, "probes", len(tl))

			return nil
		}
		slog.Warn("Lost pings in the baseline", "attempt", attempt, "lost", len(lost), "windows", summarizeLoss(lost))
	}

	return fmt.Errorf("%w: lost pings in the baseline twice, last time %s", errInconclusive, summarizeLoss(lost))
}

func summarizeLoss(lost []LostPing) string {
	windows := GroupLoss(lost, 3*trafficPingInterval)
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

func (env *disruptionEnv) specsAfterSettle(ctx context.Context) (AgentSpecs, []agentapi.Agent, error) {
	agents, err := WaitAgentsSettled(ctx, env.kube, disruptionSettleQuiet, disruptionSettleMax)
	if err != nil {
		return nil, nil, err
	}
	specs, err := SnapshotAgentSpecs(ctx, env.kube)
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

	return fmt.Errorf("%s: %s (evidence in %s)", res.op, strings.Join(res.failures, "; "), env.evidenceDir) //nolint:goerr113
}

// runUnrelated runs an operation that must not disturb anything: no lost ping, no change in any
// Agent spec but the touch marker, every Agent applied.
func (env *disruptionEnv) runUnrelated(ctx context.Context, op string, do func(context.Context) error) error {
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
	lost := LostPings(tl, env.cursor, end)
	env.cursor = end

	if len(lost) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost: %s", len(lost), summarizeLoss(lost)))
	}
	if diffs := DiffAgentSpecs(before, after, IgnoreBenchTouch); len(diffs) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("Agent specs changed: %s", strings.Join(diffs, ", ")))
		res.files["spec-diff.txt"] = strings.Join(diffs, "\n") + "\n"
	}
	pending, notes := AgentsNotApplied(agents, AgentGenerations(beforeAgents))
	env.noteOnce(notes)
	if len(pending) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("Agents not applied: %s", strings.Join(pending, ", ")))
	}
	if len(res.failures) == 0 {
		return nil
	}

	res.files["lost-pings.txt"] = FormatLostPings(lost)
	res.files["agent-timeline.txt"] = FormatAgentRows(env.aw.Rows(res.started, end))
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
		if sw.Spec.Profile == meta.SwitchProfileVS {
			env.testCtx.addNote(fmt.Sprintf("agent reinstall not run: %s is a virtual switch, the reinstall is only run on hardware", leaf))

			return nil
		}
	}

	before, beforeAgents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("reinstall: %w", err)
	}

	agent := &agentapi.Agent{}
	if err := env.kube.Get(ctx, kclient.ObjectKey{Name: target, Namespace: kmetav1.NamespaceDefault}, agent); err != nil {
		return fmt.Errorf("getting agent %s: %w", target, err)
	}
	oldInstall := agent.Status.InstallID
	if oldInstall == "" {
		return fmt.Errorf("agent %s is not installed (missing .status.installID)", target) //nolint:goerr113
	}

	res := &checkResult{op: "reinstall-" + target, started: time.Now(), files: map[string]string{}}
	slog.Info("Reinstalling switch through the Agent", "switch", target, "installID", oldInstall)
	patch := kclient.MergeFrom(agent.DeepCopy())
	agent.Spec.Reinstall = oldInstall
	if err := env.kube.Patch(ctx, agent, patch); err != nil {
		return fmt.Errorf("requesting reinstall of %s: %w", target, err)
	}

	if err := env.waitReinstalled(ctx, target, oldInstall); err != nil {
		return err
	}
	after, agents, err := env.specsAfterSettle(ctx)
	if err != nil {
		return fmt.Errorf("reinstall: %w", err)
	}
	settled := time.Now()

	if err := sleepCtx(ctx, disruptionRecovery+disruptionWatchAfter+disruptionTrailGrace); err != nil {
		return err
	}
	end := time.Now()

	tl, err := env.tw.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("reinstall: %w", err)
	}

	// strict probes never cross the leaf; hard and soft ones may lose pings until the leaf has
	// applied its config and the dataplane had disruptionRecovery to converge
	var strict, away []ProbeTimeline
	for _, p := range tl {
		if env.probeClass[p.Probe] == probeStrict {
			strict = append(strict, p)
		} else {
			away = append(away, p)
		}
	}
	lostStrict := LostPings(strict, env.cursor, end)
	lostAfter := LostPings(away, settled.Add(disruptionRecovery), end)
	lastAway := LostPings(away, env.cursor, end)
	env.cursor = end

	if len(lostStrict) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost on pairs that don't cross %s: %s", len(lostStrict), target, summarizeLoss(lostStrict)))
	}
	if len(lostAfter) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost more than %s after %s applied its config: %s", len(lostAfter), disruptionRecovery, target, summarizeLoss(lostAfter)))
	}
	slog.Info("Loss while the leaf was away", "switch", target, "pings", len(lastAway), "windows", summarizeLoss(lastAway), "applied-at", settled.Format("15:04:05"))

	ignore := func(agent, path string) bool {
		return IgnoreBenchTouch(agent, path) || (agent == target && path == "reinstall")
	}
	if diffs := DiffAgentSpecs(before, after, ignore); len(diffs) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("Agent specs changed: %s", strings.Join(diffs, ", ")))
		res.files["spec-diff.txt"] = strings.Join(diffs, "\n") + "\n"
	}
	pending, notes := AgentsNotApplied(agents, AgentGenerations(beforeAgents))
	env.noteOnce(notes)
	if len(pending) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("Agents not applied: %s", strings.Join(pending, ", ")))
	}
	if len(res.failures) == 0 {
		return nil
	}

	res.files["lost-pings.txt"] = FormatLostPings(LostPings(tl, res.started, end))
	res.files["agent-timeline.txt"] = FormatAgentRows(env.aw.Rows(res.started, end))
	for _, p := range tl {
		res.files["ping-"+sanitizeNameForFolder(p.Probe)+".log"] = p.Raw
	}

	return env.fail(res)
}

// waitReinstalled waits for the Agent of the switch to come back from the install: a new install
// ID, a fresh heartbeat and the current generation applied.
func (env *disruptionEnv) waitReinstalled(ctx context.Context, name, oldInstall string) error {
	ctx, cancel := context.WithTimeout(ctx, disruptionReinstallMax)
	defer cancel()

	for {
		ag := &agentapi.Agent{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, ag); err != nil {
			return fmt.Errorf("getting agent %s while it reinstalls: %w", name, err)
		}
		if ag.Status.InstallID != "" && ag.Status.InstallID != oldInstall &&
			time.Since(ag.Status.LastHeartbeat.Time) < time.Minute && ag.Status.LastAppliedGen == ag.Generation {
			return nil
		}
		if err := sleepCtx(ctx, 10*time.Second); err != nil {
			return fmt.Errorf("waiting for %s to be reinstalled (took more than %s): %w", name, disruptionReinstallMax, err)
		}
	}
}
