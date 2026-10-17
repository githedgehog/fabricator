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
	// disruptionMinEntries is how many VPC IRB VLAN entries each leaf of the pair must hold alone:
	// with fewer the allocator hands the lone entry the lowest free id every time and a
	// renumbering never shows.
	disruptionMinEntries   = 3
	disruptionSettleQuiet  = 10 * time.Second
	disruptionSettleMax    = 5 * time.Minute
	disruptionWarmup       = 5 * time.Second
	disruptionTrailGrace   = 2 * time.Second
	disruptionMaxClockSkew = 500 * time.Millisecond
	disruptionReinstallMax = 30 * time.Minute
	disruptionRebootMax    = 20 * time.Minute
	// disruptionESLAGMaxLoss is the longest a pair that fails over to the other leaf of the pair may
	// be without replies at a time: a leaf leaving costs two pings at most
	disruptionESLAGMaxLoss = time.Second
	// after the reinstalled leaf has applied its config the dataplane may still converge:
	// loss is only asserted from disruptionRecovery after that on, and for disruptionWatchAfter
	disruptionRecovery   = 5 * time.Second
	disruptionWatchAfter = 30 * time.Second

	disruptionExternalTarget = "1.0.0.1"
	disruptionTouchLabel     = meta.BenchTouchLabel
)

const (
	disruptionSuiteName = "Disruption Suite"

	testDisruptionSetup     = "Disruption Setup and Baseline"
	testDisruptionVPC       = "Disruption VPC Create/Delete"
	testDisruptionTouch     = "Disruption Leaf Touch"
	testDisruptionCtrl      = "Disruption Fabric Controller Restart"
	testDisruptionReboot    = "Disruption Agent Reboot"
	testDisruptionReinstall = "Disruption Agent Reinstall"
)

// errDisruptionHardwareOnly makes a test skip: it can't run on the leaves of the pair.
var errDisruptionHardwareOnly = errors.New("hardware only")

// makeDisruptionTestSuite is one test per operation. The first builds the VPCs, starts the
// traffic and watches it with no change; the others each apply one kind of change to the same
// running traffic and check only what happens from their own start. The traffic is stopped and
// the VPCs removed once, after the last test, by the suite teardown.
func makeDisruptionTestSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: disruptionSuiteName,
		Teardown: func(ctx context.Context, testCtx *VPCPeeringTestCtx) error {
			return testCtx.teardownDisruption(ctx)
		},
	}
	suite.TestCases = []JUnitTestCase{
		{Name: testDisruptionSetup, F: disruptionSetupTest, SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionVPC, F: disruptionVPCTest, SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionTouch, F: disruptionTouchTest, SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionCtrl, F: disruptionCtrlTest, SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionReboot, F: disruptionRebootTest, SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionReinstall, F: disruptionReinstallTest, SkipFlags: SkipFlags{NoServers: true}},
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

// disruptionState is what the setup produced, kept on the test context for the tests that follow.
type disruptionState struct {
	env *disruptionEnv
	// skip is why the suite can't run on this fabric: every test of it skips with the same reason
	skip error
	// err is why the setup or the baseline did not succeed: the other tests have no verdict
	err error
	// started is set once the traffic runs
	started bool
}

// ensureDisruption builds the environment on the first call, and returns it on the others, so a
// selection of tests that leaves out the setup test still works.
func (testCtx *VPCPeeringTestCtx) ensureDisruption(ctx context.Context) *disruptionState {
	if testCtx.disruption == nil {
		testCtx.disruption = testCtx.setupDisruption(ctx)
	}

	return testCtx.disruption
}

func (testCtx *VPCPeeringTestCtx) setupDisruption(ctx context.Context) *disruptionState {
	st := &disruptionState{}
	kube := testCtx.kube

	if testCtx.setupOpts.VPCMode != vpcapi.VPCModeL2VNI {
		st.skip = fmt.Errorf("VPC mode is %q, ESLAG servers only take part in l2vni", testCtx.setupOpts.VPCMode) //nolint:goerr113

		return st
	}
	if testCtx.extName == "" {
		st.skip = errNoExternals

		return st
	}

	sc, err := discoverDisruptionScenario(ctx, kube)
	switch {
	case errors.Is(err, errDisruptionNoPair), errors.Is(err, errDisruptionNoESLAGServer), errors.Is(err, errDisruptionNoSingleLeaf):
		st.skip = err

		return st
	case err != nil:
		st.err = err

		return st
	}
	slog.Info("Disruption scenario", "scenario", sc.describe())

	env := &disruptionEnv{
		testCtx:    testCtx,
		kube:       kube,
		sc:         sc,
		probeClass: map[string]string{},
		touched:    map[string]bool{},
		noted:      map[string]bool{},
	}
	st.env = env

	if err := env.setup(ctx); err != nil {
		st.err = err

		return st
	}

	env.aw = NewAgentWatch(kube)
	env.aw.Start(ctx)
	if err := env.tw.Start(ctx); err != nil {
		st.err = fmt.Errorf("starting background traffic: %w", err)

		return st
	}
	st.started = true

	if err := env.baseline(ctx); err != nil {
		st.err = err
	}

	return st
}

// disruptionSetupTest builds the VPCs, starts the traffic and watches it with no change. Loss in
// the baseline makes the whole suite inconclusive, and it is reported here.
//
// Builds the entries that only one leaf holds, which the renumbering in
// fabric#1649 needs to show.
func disruptionSetupTest(ctx context.Context, testCtx *VPCPeeringTestCtx, _ *ConnectivityMatrix) (bool, []RevertFunc, error) {
	st := testCtx.ensureDisruption(ctx)
	if st.skip != nil {
		return true, nil, st.skip
	}

	return false, nil, st.err
}

// disruptionVPCTest creates and deletes a VPC that is attached nowhere, 5 times. Nothing that
// carries traffic changes, so nothing may be lost and no Agent spec may change.
//
// Targets fabric#1649: the reconcile renumbers the IRB
// VLANs of the entries that only one leaf of the pair holds.
func disruptionVPCTest(ctx context.Context, testCtx *VPCPeeringTestCtx, m *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return disruptionOpTest(testDisruptionVPC, (*disruptionEnv).opCreateDeleteVPC)(ctx, testCtx, m)
}

// disruptionTouchTest puts a label on each leaf of the pair in turn, 12 times. The label reaches
// no config, so nothing may be lost and no Agent spec may change but the touch marker.
//
// Targets fabric#1649: any reconcile of one leaf renumbers
// the IRB VLANs of the entries attached to that leaf alone.
func disruptionTouchTest(ctx context.Context, testCtx *VPCPeeringTestCtx, m *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return disruptionOpTest(testDisruptionTouch, (*disruptionEnv).opTouchLeaves)(ctx, testCtx, m)
}

// disruptionCtrlTest restarts fabric-ctrl through a rollout, 3 times. A new controller reconciles
// everything again and has to arrive at what was there.
//
// Targets fabric#1649: the first reconcile of a new
// controller renumbers the IRB VLANs of the entries attached to one leaf alone.
func disruptionCtrlTest(ctx context.Context, testCtx *VPCPeeringTestCtx, m *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return disruptionOpTest(testDisruptionCtrl, (*disruptionEnv).opRestartController)(ctx, testCtx, m)
}

// disruptionRebootTest reboots the second leaf of the pair through its Agent. Hardware only: it
// skips when a leaf of the pair is a virtual switch.
//
// Targets fabric#1488: the traffic of ESLAG dual-homed
// servers is unstable after the leaf comes back.
func disruptionRebootTest(ctx context.Context, testCtx *VPCPeeringTestCtx, m *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return disruptionOpTest(testDisruptionReboot, (*disruptionEnv).opReboot)(ctx, testCtx, m)
}

// disruptionReinstallTest reinstalls the second leaf of the pair through its Agent. Hardware
// only: it skips when a leaf of the pair is a virtual switch.
//
// Targets fabric#1663, the Agent Applied condition that stays
// ApplyPending after a reinstall, and fabric#1488, the
// ESLAG dual-homed servers that lose traffic while the leaf comes back.
func disruptionReinstallTest(ctx context.Context, testCtx *VPCPeeringTestCtx, m *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return disruptionOpTest(testDisruptionReinstall, (*disruptionEnv).opReinstall)(ctx, testCtx, m)
}

// disruptionOpTest makes a test of one operation under the running traffic. It checks loss only
// from its own start, so one test's loss is never charged to the next. Without a clean setup it
// has no verdict.
func disruptionOpTest(name string, run func(*disruptionEnv, context.Context) error) TestFunc {
	return func(ctx context.Context, testCtx *VPCPeeringTestCtx, _ *ConnectivityMatrix) (bool, []RevertFunc, error) {
		st := testCtx.ensureDisruption(ctx)
		if st.skip != nil {
			return true, nil, st.skip
		}
		if st.err != nil {
			return false, nil, fmt.Errorf("%w: setup and baseline did not succeed: %w", errInconclusive, st.err)
		}

		env := st.env
		env.evidenceDir = filepath.Join(testCtx.vlabCfg.WorkDir, ShowTechOutputDir, sanitizeNameForFolder(disruptionSuiteName), sanitizeNameForFolder(name), "disruption")
		env.cursor = time.Now()

		if err := run(env, ctx); err != nil {
			if errors.Is(err, errDisruptionHardwareOnly) {
				return true, nil, err
			}

			return false, nil, err
		}

		return false, nil, nil
	}
}

// teardownDisruption stops the traffic and removes what the setup created. It runs once, after the last test.
func (testCtx *VPCPeeringTestCtx) teardownDisruption(ctx context.Context) error {
	st := testCtx.disruption
	if st == nil || st.env == nil {
		return nil
	}
	testCtx.disruption = nil
	env := st.env

	var errs []error
	if env.tw != nil {
		if st.started {
			if drift, err := env.tw.ClockDrift(ctx); err != nil {
				slog.Warn("Measuring server clock drift failed", "err", err)
			} else if drift > disruptionMaxClockSkew {
				slog.Warn("Server clocks drifted during the run, the times of lost pings are approximate", "drift", drift)
			}
		}
		if err := env.tw.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping background traffic: %w", err))
		}
	}
	if env.aw != nil {
		env.aw.Stop()
	}
	if err := env.cleanup(ctx); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
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
// most), one or two with an ESLAG server (the first peered with the external and with the
// second). Two single-leaf VPCs of a
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

	// a second ESLAG server, peered with the first: traffic between two servers that stay
	// attached to both leaves, to see what the reinstall costs them
	var eslagVPC2 *vpcapi.VPC
	if len(sc.ESLAG) > 1 {
		eslagVPC2, err = env.newVPC("dt-e2")
		if err != nil {
			return err
		}
		all = append(all, disruptionVPC{vpc: eslagVPC2, server: sc.ESLAG[1]})
	}

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
	if eslagVPC2 != nil {
		appendVpcPeeringSpecByName(vpcPeerings, eslagVPC.Name, eslagVPC2.Name, []string{}, []string{})
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
	// The entries the Agents then hold are counted, and a leaf that is still short gets more.
	need := map[string]int{}
	for _, leaf := range sc.Leaves {
		if len(sc.SingleLeaf[leaf]) > 0 {
			need[leaf] = disruptionMinEntries - leafOnly[leaf]
		}
	}
	fillers := 0
	var entries map[string][]string
	for pass := 1; ; pass++ {
		for i, leaf := range sc.Leaves {
			for range need[leaf] {
				fillers++
				vpc, err := env.newVPC(fmt.Sprintf("dt-f%d-%d", i+1, fillers))
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
		entries = singleLeafEntries(specs, sc.Leaves)
		short := false
		for leaf := range need {
			need[leaf] = max(0, disruptionMinEntries-len(entries[leaf]))
			short = short || need[leaf] > 0
		}
		if !short {
			break
		}
		if pass == 3 {
			return fmt.Errorf("leaves of the pair hold too few VPC IRB VLAN entries alone, need at least %d each for a renumbering to show: %v", //nolint:goerr113
				disruptionMinEntries, entries)
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

// singleLeafEntries returns, per leaf of the pair, the VPC IRB VLAN entries its Agent holds that the
// other leaf's Agent doesn't. The entries of externals ("ext@<name>") are left out: the setup
// doesn't build them, and one on a single leaf would make up for a VPC entry that is missing.
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
			if strings.HasPrefix(name, "ext@") {
				continue
			}
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
	for _, s := range env.sc.ESLAG[:min(2, len(env.sc.ESLAG))] {
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

// summarizeProbeLoss says per probe how many pings were lost and between when, one line each, in
// the order the probes first lost one. It is empty without loss.
func summarizeProbeLoss(lost []LostPing, class map[string]string) string {
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

func summarizeLoss(lost []LostPing) string {
	return summarizeWindows(GroupLoss(lost, 3*trafficPingInterval))
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

	return fmt.Errorf("%s: %s (evidence in %s)", res.op, strings.Join(res.failures, "; "), env.evidenceDir) //nolint:goerr113
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
	// timeline is the traffic as of the last round, for the raw ping logs
	timeline []ProbeTimeline
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

// finish returns the error of the operation: err when a round could not run, with what the rounds
// before it found, else what the rounds found.
func (env *disruptionEnv) finish(rep *roundReport, err error) error {
	if !rep.failed() {
		return err
	}
	files := map[string]string{}
	for _, p := range rep.timeline {
		files["ping-"+sanitizeNameForFolder(p.Probe)+".log"] = p.Raw
	}
	env.writeEvidence("", files)

	if err != nil {
		return fmt.Errorf("%w; before that: %s (evidence in %s)", err, rep.summary(), env.evidenceDir)
	}

	return fmt.Errorf("%s (evidence in %s)", rep.summary(), env.evidenceDir) //nolint:goerr113
}

// summarizeSpecDiffs counts the changes per Agent: "leaf-01 3, leaf-02 3". Each change reads "<agent>: <path>: <old> -> <new>".
func summarizeSpecDiffs(diffs []string) string {
	counts := map[string]int{}
	for _, d := range diffs {
		agent, _, _ := strings.Cut(d, ": ")
		counts[agent]++
	}
	parts := make([]string, 0, len(counts))
	for _, agent := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%s %d", agent, counts[agent]))
	}

	return strings.Join(parts, ", ")
}

// runUnrelated runs a round of an operation that must not disturb anything: no lost ping, no change
// in any Agent spec but the touch marker, every Agent applied. What it finds goes to rep; it returns an error only
// when the round could not run.
func (env *disruptionEnv) runUnrelated(ctx context.Context, rep *roundReport, op string, do func(context.Context) error) error {
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
	lost := LostPings(tl, env.cursor, end)
	env.cursor = end
	checked = true

	rep.ran++
	rep.timeline = tl
	files := map[string]string{}
	if diffs := DiffAgentSpecs(before, after, IgnoreBenchTouch); len(diffs) > 0 {
		rep.specs = append(rep.specs, fmt.Sprintf("%s: %s", op, summarizeSpecDiffs(diffs)))
		if rep.first == "" {
			rep.first = fmt.Sprintf("%s: %s", op, strings.Join(diffs, ", "))
		}
		files["spec-diff.txt"] = strings.Join(diffs, "\n") + "\n"
	}
	if len(lost) > 0 {
		rep.losses = append(rep.losses, fmt.Sprintf("%s: %d pings lost: %s", op, len(lost), summarizeLoss(lost)))
		files["lost-pings.txt"] = FormatLostPings(lost)
	}
	pending, notes := AgentsNotApplied(agents, AgentGenerations(beforeAgents))
	env.noteOnce(notes)
	if len(pending) > 0 {
		rep.pending = append(rep.pending, fmt.Sprintf("%s: %s", op, strings.Join(pending, ", ")))
	}
	if len(files) > 0 {
		files["agent-timeline.txt"] = FormatAgentRows(env.aw.Rows(started, end))
		env.writeEvidence(op, files)
	}

	return nil
}

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

// opTouchLeaves puts the benchmark touch label on the leaves of the pair in turn. Rounds that only
// change Agent specs are recorded and the next one runs; lost pings and Agents that did not apply
// end the operation.
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

// opRestartController restarts fabric-ctrl the way kubectl rollout restart does.
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

func (env *disruptionEnv) opReinstall(ctx context.Context) error {
	return env.opLeafCycle(ctx, leafCycle{
		name:      "reinstall",
		specField: "reinstall",
		max:       disruptionReinstallMax,
		request: func(ag *agentapi.Agent) (string, error) {
			if ag.Status.InstallID == "" {
				return "", fmt.Errorf("agent %s is not installed (missing .status.installID)", ag.Name) //nolint:goerr113
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
// the boot ID it is running on.
func (env *disruptionEnv) opReboot(ctx context.Context) error {
	return env.opLeafCycle(ctx, leafCycle{
		name:      "reboot",
		specField: "reboot",
		max:       disruptionRebootMax,
		request: func(ag *agentapi.Agent) (string, error) {
			if ag.Status.BootID == "" {
				return "", fmt.Errorf("agent %s doesn't report a boot ID (missing .status.bootID)", ag.Name) //nolint:goerr113
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

// opLeafCycle runs a cycle of the second leaf under traffic. Strict pairs must lose nothing, no pair
// may lose pings after the leaf applied its config and the dataplane had disruptionRecovery, and a
// pair that fails over to the other leaf may not lose more than disruptionESLAGMaxLoss in a row at any time.
func (env *disruptionEnv) opLeafCycle(ctx context.Context, cycle leafCycle) error {
	target := env.sc.Leaves[1]

	for _, leaf := range env.sc.Leaves {
		sw := &wiringapi.Switch{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: leaf, Namespace: kmetav1.NamespaceDefault}, sw); err != nil {
			return fmt.Errorf("getting switch %s: %w", leaf, err)
		}
		if sw.Spec.Profile == meta.SwitchProfileVS {
			return fmt.Errorf("%w: %s is a virtual switch, the %s is only run on hardware", errDisruptionHardwareOnly, leaf, cycle.name)
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

	// strict probes never cross the leaf; hard and soft ones may lose pings until the leaf has
	// applied its config and the dataplane had disruptionRecovery to converge. Soft ones have the
	// other leaf to go through, so they may not lose more than disruptionESLAGMaxLoss at a time.
	var strict, away, soft []ProbeTimeline
	for _, p := range tl {
		switch env.probeClass[p.Probe] {
		case probeStrict:
			strict = append(strict, p)
		case probeSoft:
			soft = append(soft, p)
			away = append(away, p)
		default:
			away = append(away, p)
		}
	}
	lostStrict := LostPings(strict, env.cursor, end)
	lostAfter := LostPings(away, settled.Add(disruptionRecovery), end)
	lostSoft := LostPings(soft, res.started, end)
	lastAway := LostPings(away, env.cursor, end)
	allLost := LostPings(tl, env.cursor, end)
	env.cursor = end
	checked = true

	if len(lostStrict) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost on pairs that don't cross %s: %s", len(lostStrict), target, summarizeLoss(lostStrict)))
	}
	if long := longLossWindows(lostSoft); len(long) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%.1fs loss on ESLAG pairs while %s rejoined (at most %s in a row allowed): %s",
			longestLossWindow(long).Seconds(), target, disruptionESLAGMaxLoss, summarizeWindows(long)))
	}
	if len(lostAfter) > 0 {
		res.failures = append(res.failures, fmt.Sprintf("%d pings lost more than %s after %s applied its config: %s", len(lostAfter), disruptionRecovery, target, summarizeLoss(lostAfter)))
	}
	slog.Info("Loss while the leaf was away", "operation", cycle.name, "switch", target, "pings", len(lastAway), "windows", summarizeLoss(lastAway), "applied-at", settled.Format("15:04:05"))

	ignore := func(agent, path string) bool {
		return IgnoreBenchTouch(agent, path) || (agent == target && path == cycle.specField)
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

	// line by line only what the checks judged; the rest is the loss expected while the leaf is away
	judged := map[LostPing]bool{}
	for _, list := range [][]LostPing{lostStrict, lostSoft, lostAfter} {
		for _, l := range list {
			judged[l] = true
		}
	}
	var judgedLost, expectedLost []LostPing
	for _, l := range allLost {
		if judged[l] {
			judgedLost = append(judgedLost, l)
		} else {
			expectedLost = append(expectedLost, l)
		}
	}
	res.files["lost-pings.txt"] = FormatLostPings(judgedLost)
	res.files["expected-loss.txt"] = summarizeProbeLoss(expectedLost, env.probeClass)
	res.files["agent-timeline.txt"] = FormatAgentRows(env.aw.Rows(res.started, end))
	for _, p := range tl {
		res.files["ping-"+sanitizeNameForFolder(p.Probe)+".log"] = p.Raw
	}

	return env.fail(res)
}

// waitAgentBack waits for the Agent of the switch to come back from the cycle and returns when it saw that.
func (env *disruptionEnv) waitAgentBack(ctx context.Context, name string, cycle leafCycle, before *agentapi.Agent) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, cycle.max)
	defer cancel()

	for {
		ag := &agentapi.Agent{}
		if err := env.kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, ag); err != nil {
			return time.Time{}, fmt.Errorf("getting agent %s during its %s: %w", name, cycle.name, err)
		}
		if cycle.back(before, ag) {
			return time.Now(), nil
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return time.Time{}, fmt.Errorf("waiting for %s to be back from the %s (took more than %s): %w", name, cycle.name, cycle.max, err)
		}
	}
}

// longLossWindows returns the loss windows of the lost pings that are longer than disruptionESLAGMaxLoss.
func longLossWindows(lost []LostPing) []LossWindow {
	var out []LossWindow
	for _, w := range GroupLoss(lost, 3*trafficPingInterval) {
		if lossWindowLength(w) > disruptionESLAGMaxLoss {
			out = append(out, w)
		}
	}

	return out
}

// lossWindowLength is the time the probe was without replies: first to last lost echo, plus the
// interval the last one stands for.
func lossWindowLength(w LossWindow) time.Duration {
	return w.To.Sub(w.From) + trafficPingInterval
}

func longestLossWindow(windows []LossWindow) time.Duration {
	var longest time.Duration
	for _, w := range windows {
		longest = max(longest, lossWindowLength(w))
	}

	return longest
}

func summarizeWindows(windows []LossWindow) string {
	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		parts = append(parts, fmt.Sprintf("%s %s..%s (%d pings)", w.Probe, w.From.Format("15:04:05.000"), w.To.Format("15:04:05.000"), w.Pings))
	}

	return strings.Join(parts, "; ")
}
