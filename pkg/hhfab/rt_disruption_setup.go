// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/errgroup"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

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
		Name: name, Namespace: kmetav1.NamespaceDefault,
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
// and prepares the traffic probes. The layout:
//   - one VPC per server attached to a single leaf, at most two per leaf
//   - one VPC per ESLAG server, at most two; the first is peered with the external and with
//     the second
//   - the two single-leaf VPCs of a leaf are peered with each other, so traffic crosses entries
//     that only that leaf holds; a leaf with one single-leaf VPC peers it with the ESLAG VPC
//
// A VPC peered with a VPC on both leaves is needed by both leaves, so it isn't an entry held
// by a single leaf.
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

	if _, err := waitAgentsSettled(ctx, kube, disruptionSettleQuiet, disruptionSettleMax); err != nil {
		return err
	}
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

		if _, err := waitAgentsSettled(ctx, kube, disruptionSettleQuiet, disruptionSettleMax); err != nil {
			return err
		}
		if err := WaitReady(ctx, kube, testCtx.wrOpts); err != nil {
			return fmt.Errorf("waiting for ready after adding entries: %w", err)
		}

		specs, err := snapshotAgentSpecs(ctx, kube)
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
			return fmt.Errorf("leaves of the pair hold too few VPC IRB VLAN entries alone, need at least %d each for a renumbering to show: %v", //nolint:err113
				disruptionMinEntries, entries)
		}
	}
	slog.Info("Entries held by a single leaf of the pair", "entries", entries)

	probes, err := env.buildProbes(ctx, matrix, []disruptionVPC{{vpc: eslagVPC, server: sc.ESLAG[0]}})
	if err != nil {
		return err
	}
	if len(probes) == 0 {
		return fmt.Errorf("no traffic pair expected to be reachable, nothing to watch") //nolint:err113
	}
	env.tw = newTrafficWatch(probes)

	return nil
}

// singleLeafEntries returns, per leaf of the pair, the VPC IRB VLAN entries its Agent holds that the
// other leaf's Agent doesn't. The entries of externals ("ext@<name>") are left out: the setup
// doesn't build them, and one on a single leaf would make up for a VPC entry that is missing.
func singleLeafEntries(specs agentSpecs, leaves [2]string) map[string][]string {
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
func (env *disruptionEnv) buildProbes(ctx context.Context, matrix *ConnectivityMatrix, externalSources []disruptionVPC) ([]trafficProbe, error) {
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

	probes := make([]trafficProbe, 0, len(matrix.AllEndpoints))
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
			probes = append(probes, trafficProbe{Name: name, From: src.Server.Name, FromSSH: ssh, To: dst.Server.IP})
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
		probes = append(probes, trafficProbe{Name: name, From: d.server.Name, FromSSH: ssh, To: netip.MustParseAddr(disruptionExternalTarget)})
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
		for _, s := range list[:min(2, len(list))] {
			servers[s.Name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
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

// baseline watches the traffic with no change. Loss makes it run once more; loss again leaves the
// suite without a verdict: the traffic isn't clean before any change, so nothing can be told from it.
func (env *disruptionEnv) baseline(ctx context.Context) error {
	if err := sleepCtx(ctx, disruptionWarmup); err != nil {
		return err
	}

	var lost []lostPing
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
		if broken := brokenProbes(tl); len(broken) > 0 {
			return fmt.Errorf("no ping reply at all on %v during the baseline", broken) //nolint:err113
		}
		lost = lostPings(tl, from, to)
		env.cursor = to
		if len(lost) == 0 {
			slog.Info("Baseline traffic is clean", "duration", disruptionBaseline, "probes", len(tl))

			return nil
		}
		slog.Warn("Lost pings in the baseline", "attempt", attempt, "lost", len(lost), "windows", summarizeLoss(lost))
	}

	return fmt.Errorf("%w: lost pings in the baseline twice, last time %s", errInconclusive, summarizeLoss(lost))
}
