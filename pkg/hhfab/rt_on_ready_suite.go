// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	"golang.org/x/sync/errgroup"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func makeOnReadyTestSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "OnReady Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name:      "New VLAB OnReady Test",
			F:         testCtx.newOnReadyTest,
			SkipFlags: SkipFlags{},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// ortServerInfo holds information about a server and its connection for the on-ready test.
type ortServerInfo struct {
	name        string
	conn        wiringapi.Connection
	switches    []string
	isUnbundled bool
	isMclag     bool
}

// Test to get as much coverage as possible for Fabric while being short and compact
// Unlike the other tests, here we create ad-hoc VPCs and attach them to servers to
// maximize feature coverage. Specifically we want:
// - 1 VPC with two subnets, 1 server per subnet
// - 1 VPC with a single host bgp subnet and a server
// - 1 VPC with a single subnet and 2 servers attached to two different switches
// - as many VPCs with a single regular subnet and a single server as there are remaining servers (at least 2)
// in terms of peering, we want to cover:
// - 1 single-server VPC peered to the bgp external
// - 1 single-server VPC peered to the static external without proxy
// - the 2-servers VPC peered with the proxied static external via the gateway, using masquerade NAT
// - the host bgp VPC peered with a regular VPC
// the test should fail if this target setup cannot be achieved, with the only exception of gateway
// not being enabled, in which case we skip gateway peerings
// Note that there are more things that we could test but are not supported by virtual switches,
// such as restricted/isolated flags, multiple peerings to the same external etc.
// At the end of the test, we clean up all VPCs and peerings to leave a clean slate.
//
//nolint:cyclop
func (testCtx *VPCPeeringTestCtx) newOnReadyTest(ctx context.Context) (bool, []RevertFunc, error) {
	slog.Info("Starting new on-ready test: discovering resources")

	kube := testCtx.kube
	reverts := make([]RevertFunc, 0)

	// ── Phase 1: Discover switches and classify MCLAG ────────────────────
	swList := &wiringapi.SwitchList{}
	if err := kube.List(ctx, swList); err != nil {
		return false, nil, fmt.Errorf("listing switches: %w", err)
	}
	mclagSwitches := map[string]bool{}
	for _, sw := range swList.Items {
		if sw.Spec.Redundancy.Type == meta.RedundancyTypeMCLAG {
			mclagSwitches[sw.Name] = true
		}
	}

	// ── Phase 2: Discover servers and their connections ───────────────────
	servers := &wiringapi.ServerList{}
	if err := kube.List(ctx, servers); err != nil {
		return false, nil, fmt.Errorf("listing servers: %w", err)
	}
	// Sort servers by their numeric ID for deterministic allocation
	serverIDs := map[string]uint64{}
	for _, server := range servers.Items {
		if !strings.HasPrefix(server.Name, ServerNamePrefix) {
			continue
		}

		serverID, err := strconv.ParseUint(server.Name[len(ServerNamePrefix):], 10, 64)
		if err != nil {
			continue
		}
		serverIDs[server.Name] = serverID
	}
	slices.SortFunc(servers.Items, func(a, b wiringapi.Server) int {
		return int(serverIDs[a.Name]) - int(serverIDs[b.Name]) //nolint:gosec
	})

	available := make([]ortServerInfo, 0, len(servers.Items))
	for _, server := range servers.Items {
		if _, ok := serverIDs[server.Name]; !ok {
			continue
		}

		conns := &wiringapi.ConnectionList{}
		if err := kube.List(ctx, conns, wiringapi.MatchingLabelsForListLabelServer(server.Name)); err != nil {
			return false, nil, fmt.Errorf("listing connections for server %q: %w", server.Name, err)
		}
		if len(conns.Items) != 1 {
			slog.Debug("Skipping server with unexpected connection count", "server", server.Name, "conns", len(conns.Items))

			continue
		}
		conn := conns.Items[0]

		// Skip ESLAG servers in non-L2VNI mode (they need special handling)
		if conn.Spec.ESLAG != nil && testCtx.setupOpts.VPCMode != vpcapi.VPCModeL2VNI {
			slog.Debug("Skipping ESLAG server", "server", server.Name)

			continue
		}

		switches, _, _, _, err := conn.Spec.Endpoints()
		if err != nil {
			slog.Debug("Skipping server with endpoint error", "server", server.Name, "err", err)

			continue
		}
		isMclag := false
		for _, sw := range switches {
			if mclagSwitches[sw] {
				isMclag = true

				break
			}
		}
		available = append(available, ortServerInfo{
			name:        server.Name,
			conn:        conn,
			switches:    switches,
			isUnbundled: conn.Spec.Unbundled != nil,
			isMclag:     isMclag,
		})
	}
	slog.Info("Discovered available servers", "count", len(available))

	// ── Phase 3: Discover externals ──────────────────────────────────────
	extList := &vpcapi.ExternalList{}
	if err := kube.List(ctx, extList); err != nil {
		return false, nil, fmt.Errorf("listing externals: %w", err)
	}
	extAttachList := &vpcapi.ExternalAttachmentList{}
	if err := kube.List(ctx, extAttachList); err != nil {
		return false, nil, fmt.Errorf("listing external attachments: %w", err)
	}

	var bgpExtName, staticExtNonProxyName, staticExtProxyName string
	// var staticExtProxyRemoteIP string
	for _, ext := range extList.Items {
		if ext.Spec.Static == nil {
			// BGP external
			if bgpExtName == "" {
				// Verify it has at least one attachment
				for _, att := range extAttachList.Items {
					if att.Spec.External == ext.Name {
						bgpExtName = ext.Name

						break
					}
				}
			}
		} else {
			// Static external – classify by attachment proxy flag
			for _, att := range extAttachList.Items {
				if att.Spec.External != ext.Name || att.Spec.Static == nil {
					continue
				}
				if att.Spec.Static.Proxy && staticExtProxyName == "" {
					staticExtProxyName = ext.Name
					// staticExtProxyRemoteIP = att.Spec.Static.RemoteIP
				} else if !att.Spec.Static.Proxy && staticExtNonProxyName == "" {
					staticExtNonProxyName = ext.Name
				}
			}
		}
	}
	slog.Info("Discovered externals",
		"bgp", bgpExtName, "staticNonProxy", staticExtNonProxyName, "staticProxy", staticExtProxyName)

	// ── Phase 4: Validate preconditions ──────────────────────────────────
	if bgpExtName == "" {
		return false, nil, fmt.Errorf("no BGP external found, cannot run on-ready test") //nolint:goerr113
	}
	if staticExtNonProxyName == "" {
		return false, nil, fmt.Errorf("no static external without proxy found, cannot run on-ready test") //nolint:goerr113
	}
	if staticExtProxyName == "" {
		slog.Warn("no proxied static external found, but we are currently skipping its testing")
	}
	const minServers = 7
	if len(available) < minServers {
		return false, nil, fmt.Errorf("not enough servers: need at least %d, found %d", minServers, len(available)) //nolint:goerr113
	}

	// ── Phase 5: Allocate servers to VPC roles ───────────────────────────
	// We consume servers from the `available` slice as we assign them.
	used := map[string]bool{}
	take := func(pred func(s ortServerInfo) bool) (ortServerInfo, bool) {
		for _, s := range available {
			if used[s.name] {
				continue
			}

			if pred(s) {
				used[s.name] = true

				return s, true
			}
		}

		return ortServerInfo{}, false
	}

	// VPC B: 1 hostBGP subnet – needs unbundled, non-MCLAG
	hostBGPServer, ok := take(func(s ortServerInfo) bool {
		return s.isUnbundled && !s.isMclag
	})
	if !ok {
		return false, nil, fmt.Errorf("no unbundled non-MCLAG server available for hostBGP VPC") //nolint:goerr113
	}

	// VPC C: 1 subnet, 2 servers on different non mclag switches
	vpcCServer1, ok := take(func(s ortServerInfo) bool { return !s.isMclag })
	if !ok {
		return false, nil, fmt.Errorf("not enough servers for multi-switch VPC") //nolint:goerr113
	}
	vpcCServer2, ok := take(func(s ortServerInfo) bool {
		if s.isMclag {
			return false
		}
		// Must be on a different switch than vpcCServer1
		for _, sw := range s.switches {
			for _, sw1 := range vpcCServer1.switches {
				if sw == sw1 {
					return false
				}
			}
		}

		return true
	})
	if !ok {
		return false, nil, fmt.Errorf("no server on a different switch found for multi-switch VPC") //nolint:goerr113
	}

	// VPC A: 2 subnets, 1 server per subnet
	vpcAServer1, ok := take(func(s ortServerInfo) bool { return true })
	if !ok {
		return false, nil, fmt.Errorf("not enough servers for dual-subnet VPC (server 1)") //nolint:goerr113
	}
	vpcAServer2, ok := take(func(s ortServerInfo) bool { return true })
	if !ok {
		return false, nil, fmt.Errorf("not enough servers for dual-subnet VPC (server 2)") //nolint:goerr113
	}

	// VPC D+: remaining servers, need at least 2 for BGP external + static external peerings
	singleServers := make([]ortServerInfo, 0)
	for _, s := range available {
		if used[s.name] {
			continue
		}
		used[s.name] = true
		singleServers = append(singleServers, s)
	}
	if len(singleServers) < 2 {
		return false, nil, fmt.Errorf("not enough remaining servers for single-server VPCs: need at least 2, got %d", len(singleServers)) //nolint:goerr113
	}
	slog.Info("Server allocation complete",
		"vpcA", []string{vpcAServer1.name, vpcAServer2.name},
		"vpcB_hostBGP", hostBGPServer.name,
		"vpcC_multiSwitch", []string{vpcCServer1.name, vpcCServer2.name},
		"singleServerVPCs", len(singleServers))

	// ── Phase 6: Allocate VLANs and subnets ──────────────────────────────
	vlanNS := &wiringapi.VLANNamespace{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: testCtx.setupOpts.VLANNamespace, Namespace: kmetav1.NamespaceDefault}, vlanNS); err != nil {
		return false, nil, fmt.Errorf("getting VLAN namespace %s: %w", testCtx.setupOpts.VLANNamespace, err)
	}
	nextVLAN, stopVLAN := iter.Pull(VLANsFrom(vlanNS.Spec.Ranges...))
	defer stopVLAN()

	ipNS := &vpcapi.IPv4Namespace{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: testCtx.setupOpts.IPv4Namespace, Namespace: kmetav1.NamespaceDefault}, ipNS); err != nil {
		return false, nil, fmt.Errorf("getting IPv4 namespace %s: %w", testCtx.setupOpts.IPv4Namespace, err)
	}
	prefixes := make([]netip.Prefix, 0)
	for _, p := range ipNS.Spec.Subnets {
		parsed, err := netip.ParsePrefix(p)
		if err != nil {
			return false, nil, fmt.Errorf("parsing IPv4 namespace prefix %q: %w", p, err)
		}
		prefixes = append(prefixes, parsed)
	}
	nextPrefix, stopPrefix := iter.Pull(SubPrefixesFrom(24, prefixes...))
	defer stopPrefix()

	allocVLAN := func() (uint16, error) {
		v, ok := nextVLAN()
		if !ok {
			return 0, fmt.Errorf("no more VLANs available") //nolint:goerr113
		}

		return v, nil
	}
	allocSubnet := func() (string, error) {
		p, ok := nextPrefix()
		if !ok {
			return "", fmt.Errorf("no more subnets available") //nolint:goerr113
		}

		return p.String(), nil
	}

	vpcMode := testCtx.setupOpts.VPCMode
	hashPolicy := testCtx.setupOpts.HashPolicy

	// ── Phase 7: Build VPCs, attachments, and server netconf commands ────
	type vpcPlan struct {
		vpc      *vpcapi.VPC
		attaches []*vpcapi.VPCAttachment
		// map server name → netconf command (for hhnet) or hostBGP arguments
		netconfs map[string]string
		hostBGP  map[string]bool
	}
	plans := make([]vpcPlan, 0)

	makeRegularSubnet := func() (*vpcapi.VPCSubnet, error) {
		cidr, err := allocSubnet()
		if err != nil {
			return nil, err
		}
		vlan, err := allocVLAN()
		if err != nil {
			return nil, err
		}

		return &vpcapi.VPCSubnet{
			Subnet: cidr,
			VLAN:   vlan,
			DHCP:   vpcapi.VPCDHCP{Enable: true},
		}, nil
	}

	makeAttach := func(connName, vpcName, subnetName string) *vpcapi.VPCAttachment {
		attachName := fmt.Sprintf("%s--%s--%s", connName, vpcName, subnetName)

		return &vpcapi.VPCAttachment{
			ObjectMeta: kmetav1.ObjectMeta{
				Name:      attachName,
				Namespace: kmetav1.NamespaceDefault,
			},
			Spec: vpcapi.VPCAttachmentSpec{
				Connection: connName,
				Subnet:     fmt.Sprintf("%s/%s", vpcName, subnetName),
			},
		}
	}

	serverNetconf := func(s ortServerInfo, vlan uint16) (string, error) {
		return GetServerNetconfCmd(&s.conn, vlan, hashPolicy)
	}

	// ── VPC A: 2 subnets, 1 server per subnet ───────────────────────────
	const vpcAName = "ort-a"
	{
		sub1, err := makeRegularSubnet()
		if err != nil {
			return false, nil, fmt.Errorf("allocating VPC A subnet-01: %w", err)
		}
		sub2, err := makeRegularSubnet()
		if err != nil {
			return false, nil, fmt.Errorf("allocating VPC A subnet-02: %w", err)
		}
		vpc := &vpcapi.VPC{
			ObjectMeta: kmetav1.ObjectMeta{Name: vpcAName, Namespace: kmetav1.NamespaceDefault},
			Spec: vpcapi.VPCSpec{
				Mode: vpcMode,
				Subnets: map[string]*vpcapi.VPCSubnet{
					"subnet-01": sub1,
					"subnet-02": sub2,
				},
			},
		}
		att1 := makeAttach(vpcAServer1.conn.Name, vpcAName, "subnet-01")
		att2 := makeAttach(vpcAServer2.conn.Name, vpcAName, "subnet-02")
		nc1, err := serverNetconf(vpcAServer1, sub1.VLAN)
		if err != nil {
			return false, nil, fmt.Errorf("netconf for %s: %w", vpcAServer1.name, err)
		}
		nc2, err := serverNetconf(vpcAServer2, sub2.VLAN)
		if err != nil {
			return false, nil, fmt.Errorf("netconf for %s: %w", vpcAServer2.name, err)
		}
		plans = append(plans, vpcPlan{
			vpc:      vpc,
			attaches: []*vpcapi.VPCAttachment{att1, att2},
			netconfs: map[string]string{vpcAServer1.name: nc1, vpcAServer2.name: nc2},
			hostBGP:  map[string]bool{},
		})
	}

	// ── VPC B: 1 hostBGP subnet, 1 server ────────────────────────────────
	const vpcBName = "ort-b"
	{
		subCIDR, err := allocSubnet()
		if err != nil {
			return false, nil, fmt.Errorf("allocating VPC B subnet: %w", err)
		}
		vlan, err := allocVLAN()
		if err != nil {
			return false, nil, fmt.Errorf("allocating VPC B VLAN: %w", err)
		}
		sub := &vpcapi.VPCSubnet{
			Subnet:  subCIDR,
			HostBGP: true,
			VLAN:    vlan,
		}
		vpc := &vpcapi.VPC{
			ObjectMeta: kmetav1.ObjectMeta{Name: vpcBName, Namespace: kmetav1.NamespaceDefault},
			Spec: vpcapi.VPCSpec{
				Mode: vpcMode,
				Subnets: map[string]*vpcapi.VPCSubnet{
					"subnet-01": sub,
				},
			},
		}
		att := makeAttach(hostBGPServer.conn.Name, vpcBName, "subnet-01")
		subPrefix, err := netip.ParsePrefix(subCIDR)
		if err != nil {
			return false, nil, fmt.Errorf("parsing VPC B subnet: %w", err)
		}
		hostBGPCmd, err := getServerHostBGPCmd(&hostBGPServer.conn, vlan, subPrefix, 1)
		if err != nil {
			return false, nil, fmt.Errorf("hostBGP cmd for %s: %w", hostBGPServer.name, err)
		}
		plans = append(plans, vpcPlan{
			vpc:      vpc,
			attaches: []*vpcapi.VPCAttachment{att},
			netconfs: map[string]string{hostBGPServer.name: hostBGPCmd},
			hostBGP:  map[string]bool{hostBGPServer.name: true},
		})
	}

	// ── VPC C: 1 subnet, 2 servers on different switches ─────────────────
	const vpcCName = "ort-c"
	{
		sub, err := makeRegularSubnet()
		if err != nil {
			return false, nil, fmt.Errorf("allocating VPC C subnet: %w", err)
		}
		vpc := &vpcapi.VPC{
			ObjectMeta: kmetav1.ObjectMeta{Name: vpcCName, Namespace: kmetav1.NamespaceDefault},
			Spec: vpcapi.VPCSpec{
				Mode: vpcMode,
				Subnets: map[string]*vpcapi.VPCSubnet{
					"subnet-01": sub,
				},
			},
		}
		att1 := makeAttach(vpcCServer1.conn.Name, vpcCName, "subnet-01")
		att2 := makeAttach(vpcCServer2.conn.Name, vpcCName, "subnet-01")
		nc1, err := serverNetconf(vpcCServer1, sub.VLAN)
		if err != nil {
			return false, nil, fmt.Errorf("netconf for %s: %w", vpcCServer1.name, err)
		}
		nc2, err := serverNetconf(vpcCServer2, sub.VLAN)
		if err != nil {
			return false, nil, fmt.Errorf("netconf for %s: %w", vpcCServer2.name, err)
		}
		plans = append(plans, vpcPlan{
			vpc:      vpc,
			attaches: []*vpcapi.VPCAttachment{att1, att2},
			netconfs: map[string]string{vpcCServer1.name: nc1, vpcCServer2.name: nc2},
			hostBGP:  map[string]bool{},
		})
	}

	// ── VPC D+: 1 regular subnet + 1 server each, for remaining servers ──
	singleServerVPCs := make(map[string]*vpcapi.VPC, len(singleServers))
	for i, s := range singleServers {
		vpcName := fmt.Sprintf("ort-d%d", i+1)
		sub, err := makeRegularSubnet()
		if err != nil {
			return false, nil, fmt.Errorf("allocating %s subnet: %w", vpcName, err)
		}
		vpc := &vpcapi.VPC{
			ObjectMeta: kmetav1.ObjectMeta{Name: vpcName, Namespace: kmetav1.NamespaceDefault},
			Spec: vpcapi.VPCSpec{
				Mode: vpcMode,
				Subnets: map[string]*vpcapi.VPCSubnet{
					"subnet-01": sub,
				},
			},
		}
		singleServerVPCs[s.name] = vpc
		att := makeAttach(s.conn.Name, vpcName, "subnet-01")
		nc, err := serverNetconf(s, sub.VLAN)
		if err != nil {
			return false, nil, fmt.Errorf("netconf for %s: %w", s.name, err)
		}
		plans = append(plans, vpcPlan{
			vpc:      vpc,
			attaches: []*vpcapi.VPCAttachment{att},
			netconfs: map[string]string{s.name: nc},
			hostBGP:  map[string]bool{},
		})
	}

	// ── Phase 8: Create VPCs and attachments in Kubernetes ────────────────
	slog.Info("Creating VPCs and attachments", "vpcs", len(plans))

	// Register a cleanup revert that removes everything we created
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Info("Cleaning up on-ready test VPCs and peerings")
		if err := hhfctl.VPCWipeWithClient(ctx, kube); err != nil {
			slog.Warn("failed to wipe VPCs and peerings", "error", err)
		}
		// Clean up server networking
		for _, plan := range plans {
			for serverName := range plan.netconfs {
				ssh, err := testCtx.getSSH(ctx, serverName)
				if err != nil {
					slog.Warn("Failed to get SSH for cleanup", "server", serverName, "err", err)

					continue
				}
				if plan.hostBGP[serverName] {
					_, _, _ = ssh.Run(ctx, "docker stop -t 1 hostbgp")
				}

				if _, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
					slog.Warn("hhnet cleanup failed", "server", serverName, "err", err, "stderr", stderr)
				}
			}
		}

		return nil
	})

	for _, plan := range plans {
		if _, err := CreateOrUpdateVpc(ctx, kube, plan.vpc); err != nil {
			return false, reverts, fmt.Errorf("creating VPC %s: %w", plan.vpc.Name, err)
		}
	}

	for _, plan := range plans {
		for _, att := range plan.attaches {
			if err := kube.Create(ctx, att); err != nil {
				return false, reverts, fmt.Errorf("creating attachment %s: %w", att.Name, err)
			}
		}
	}

	// Wait for switches to pick up the new VPCs/attachments
	time.Sleep(15 * time.Second)
	if err := WaitReady(ctx, kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready after creating VPCs: %w", err)
	}

	// ── Phase 9: Configure server networking ─────────────────────────────
	slog.Info("Configuring server networking")

	g := &errgroup.Group{}
	for _, plan := range plans {
		for serverName, cmd := range plan.netconfs {
			g.Go(func() error {
				ssh, err := testCtx.getSSH(ctx, serverName)
				if err != nil {
					return fmt.Errorf("getting SSH for %s: %w", serverName, err)
				}
				// Cleanup any previous config
				if _, _, err := ssh.Run(ctx, "docker stop -t 1 hostbgp"); err != nil {
					// Ignore – container may not be running
					_ = err
				}
				if _, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
					return fmt.Errorf("hhnet cleanup on %s: %w: %s", serverName, err, stderr)
				}

				if plan.hostBGP[serverName] {
					slog.Debug("Starting hostBGP container", "server", serverName, "args", cmd)
					_, stderr, err := ssh.Run(ctx, "docker run --network=host --privileged --rm --detach --name hostbgp ghcr.io/githedgehog/host-bgp "+cmd)
					if err != nil {
						return fmt.Errorf("starting hostbgp on %s: %w: %s", serverName, err, stderr)
					}
					// Wait for hostBGP to acquire a VIP
					acquired := false
					for attempt := range 10 {
						time.Sleep(2 * time.Second)
						stdout, _, err := ssh.Run(ctx, "/opt/bin/hhnet getvips")
						if err != nil {
							continue
						}
						for line := range strings.Lines(stdout) {
							line = strings.TrimSpace(line)
							if _, parseErr := netip.ParsePrefix(line); parseErr == nil {
								acquired = true
								slog.Debug("HostBGP VIP acquired", "server", serverName, "vip", line, "attempt", attempt+1)

								break
							}
						}
						if acquired {
							break
						}
					}
					if !acquired {
						return fmt.Errorf("hostBGP on %s did not acquire VIP in time", serverName) //nolint:goerr113
					}
				} else {
					slog.Debug("Configuring server networking", "server", serverName, "cmd", cmd)
					stdout, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet "+cmd)
					if err != nil {
						return fmt.Errorf("hhnet on %s: %w: %s", serverName, err, stderr)
					}
					addr := strings.TrimSpace(stdout)
					if _, parseErr := netip.ParsePrefix(addr); parseErr != nil {
						return fmt.Errorf("unexpected hhnet output on %s: %q", serverName, addr) //nolint:goerr113
					}
					slog.Debug("Server configured", "server", serverName, "addr", addr)
				}

				return nil
			})
		}
	}

	if err := g.Wait(); err != nil {
		return false, reverts, fmt.Errorf("configuring server networking: %w", err)
	}

	// In L3VNI mode, give extra time for routes to propagate
	if vpcMode == vpcapi.VPCModeL3VNI || vpcMode == vpcapi.VPCModeL3Flat {
		time.Sleep(10 * time.Second)
	}

	// ── Phase 10: Build and apply peerings ───────────────────────────────
	slog.Info("Setting up peerings")

	// We need the created VPCs (with defaults applied) to build gateway peering specs.
	// Re-fetch VPC C to get full subnet CIDRs after defaults.
	vpcC := &vpcapi.VPC{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: vpcCName, Namespace: kmetav1.NamespaceDefault}, vpcC); err != nil {
		return false, reverts, fmt.Errorf("getting VPC %s: %w", vpcCName, err)
	}

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	extPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	// Peering 1: first single-server VPC peered to the BGP external
	bgpPeerVPC := singleServerVPCs[singleServers[0].name]
	appendExtPeeringSpecByName(extPeerings, bgpPeerVPC.Name, bgpExtName, []string{"subnet-01"}, AllZeroPrefix)
	slog.Debug("Added BGP external peering", "server", singleServers[0].name, "vpc", bgpPeerVPC.Name, "external", bgpExtName)

	// Peering 2: second single-server VPC peered to the static external without proxy
	staticPeerVPC := singleServerVPCs[singleServers[1].name]
	appendExtPeeringSpecByName(extPeerings, staticPeerVPC.Name, staticExtNonProxyName, []string{"subnet-01"}, AllZeroPrefix)
	slog.Debug("Added static (non-proxy) external peering", "server", singleServers[1].name, "vpc", staticPeerVPC.Name, "external", staticExtNonProxyName)

	// Peering 3: VPC C (2-server, multi-switch) peered with the proxied static external via gateway, masquerade NAT
	// commented out as test-connectivity does not support NAT currently
	// if testCtx.gwSupported {
	// 	entryName := fmt.Sprintf("%s--ext.%s", vpcCName, staticExtProxyName)
	// 	// Expose VPC C's subnet with masquerade NAT
	// 	vpcCSubnets := make([]gwapi.PeeringEntryIP, 0)
	// 	for _, sub := range vpcC.Spec.Subnets {
	// 		vpcCSubnets = append(vpcCSubnets, gwapi.PeeringEntryIP{CIDR: sub.Subnet})
	// 	}
	// 	extIP, err := netip.ParseAddr(staticExtProxyRemoteIP)
	// 	if err != nil {
	// 		return false, reverts, fmt.Errorf("parsing static external proxy remote IP %q: %w", staticExtProxyRemoteIP, err)
	// 	}
	// 	masqPrefix, err := extIP.Next().Prefix(32)
	// 	if err != nil {
	// 		return false, reverts, fmt.Errorf("creating masquerade prefix from %q: %w", extIP.Next(), err)
	// 	}
	// 	gwPeerings[entryName] = &gwapi.PeeringSpec{
	// 		Peering: map[string]*gwapi.PeeringEntry{
	// 			vpcCName: {
	// 				Expose: []gwapi.PeeringEntryExpose{
	// 					{
	// 						IPs: vpcCSubnets,
	// 						As:  []gwapi.PeeringEntryAs{{CIDR: masqPrefix.String()}},
	// 						NAT: &gwapi.PeeringNAT{Masquerade: &gwapi.PeeringNATMasquerade{}},
	// 					},
	// 				},
	// 			},
	// 			"ext." + staticExtProxyName: {
	// 				Expose: []gwapi.PeeringEntryExpose{
	// 					{DefaultDestination: true},
	// 				},
	// 			},
	// 		},
	// 	}
	// 	slog.Debug("Added gateway masquerade peering", "vpc", vpcCName, "external", staticExtProxyName)
	// }

	// Peering 4: hostBGP VPC (B) peered with VPC A (a regular VPC)
	appendVpcPeeringSpecByName(vpcPeerings, vpcBName, vpcAName, "", []string{}, []string{})
	slog.Debug("Added VPC peering", "vpc1", vpcBName, "vpc2", vpcAName)

	// Extra gateway peerings to increase coverage. TODO: add NAT once test-connectivity supports it.
	// Note that we skip peering between the first two servers as they are respectively peered to the BGP external
	// and the static non-proxy one, and by peering them the bgp VRF would get a LPM route to the second server
	// compared to the flat ipv4 namespace route the static external vrf has in the virtual external
	if testCtx.gwSupported {
		for i := 1; i <= len(singleServers)-2; i++ {
			appendGwPeeringSpec(gwPeerings, singleServerVPCs[singleServers[i].name], singleServerVPCs[singleServers[i+1].name], nil, nil)
		}
	}

	if err := DoSetupPeerings(ctx, kube, vpcPeerings, extPeerings, gwPeerings, true); err != nil {
		return false, reverts, fmt.Errorf("setting up peerings: %w", err)
	}

	// ── Phase 11: Test connectivity ──────────────────────────────────────
	slog.Info("Running connectivity test")

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, reverts, fmt.Errorf("connectivity test failed: %w", err)
	}

	slog.Info("On-ready test completed successfully")

	return false, reverts, nil
}
