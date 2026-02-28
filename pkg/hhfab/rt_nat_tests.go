// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
)

// excludedInterfaces contains interface names to skip when discovering server IPs.
// These are system interfaces that don't carry VPC traffic.
var excludedInterfaces = map[string]bool{
	"lo":      true, // loopback
	"enp2s0":  true, // management interface
	"docker0": true, // docker bridge
}

// calculateStaticNATIP calculates the expected NAT IP for a source IP using the static NAT offset algorithm.
// NOTE: This function mirrors the algorithm from dataplane
// nat_ip = nat_pool_start + (source_ip - source_subnet_start)
func calculateStaticNATIP(sourceIP, sourceSubnet, natPoolStart netip.Addr) (netip.Addr, error) {
	// Calculate offset from source subnet start
	var offset uint32
	if sourceIP.Is4() && sourceSubnet.Is4() && natPoolStart.Is4() {
		sourceBytes := sourceIP.As4()
		subnetBytes := sourceSubnet.As4()

		sourceInt := binary.BigEndian.Uint32(sourceBytes[:])
		subnetInt := binary.BigEndian.Uint32(subnetBytes[:])

		if sourceInt < subnetInt {
			return netip.Addr{}, fmt.Errorf("source IP %s is before subnet start %s", sourceIP, sourceSubnet) //nolint:err113
		}
		offset = sourceInt - subnetInt

		// Add offset to NAT pool start
		natPoolBytes := natPoolStart.As4()
		natPoolInt := binary.BigEndian.Uint32(natPoolBytes[:])
		natIPInt := natPoolInt + offset

		var natIPBytes [4]byte
		binary.BigEndian.PutUint32(natIPBytes[:], natIPInt)

		return netip.AddrFrom4(natIPBytes), nil
	}

	return netip.Addr{}, fmt.Errorf("only IPv4 NAT is currently supported") //nolint:err113
}

// testNATGatewayConnectivity performs E2E connectivity testing for NAT gateway peering.
// It discovers server IPs, calculates expected NAT IPs, and performs ping/iperf3 tests
// using the shared checkPing and checkIPerf functions from testing.go.
// NOTE: This uses calculateStaticNATIP which couples to the dataplane NAT algorithm.
// The function supports both source NAT and destination NAT:
// - If vpc2NATPool is set: vpc1 pings vpc2 using vpc2's NAT IPs (destination NAT)
// - If vpc2NATPool is empty: vpc1 pings vpc2's real IPs (source NAT on vpc1 side)
// vpc1NATPool is not used because source NAT is transparent from the client perspective.
func (testCtx *VPCPeeringTestCtx) testNATGatewayConnectivity(
	ctx context.Context,
	vpc1, vpc2 *vpcapi.VPC,
	_ /* vpc1NATPool */, vpc2NATPool []string,
) error {
	startTime := time.Now()
	slog.Info("Testing NAT gateway peering connectivity")

	servers := &wiringapi.ServerList{}
	if err := testCtx.kube.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}

	// Get servers attached to each VPC
	vpc1Servers := []string{}
	vpc2Servers := []string{}

	for _, server := range servers.Items {
		attachedSubnets, err := apiutil.GetAttachedSubnets(ctx, testCtx.kube, server.Name)
		if err != nil {
			continue
		}

		for subnetName := range attachedSubnets {
			if strings.HasPrefix(subnetName, vpc1.Name+"/") {
				vpc1Servers = append(vpc1Servers, server.Name)

				break
			}
			if strings.HasPrefix(subnetName, vpc2.Name+"/") {
				vpc2Servers = append(vpc2Servers, server.Name)

				break
			}
		}
	}

	if len(vpc1Servers) == 0 || len(vpc2Servers) == 0 {
		return fmt.Errorf("need servers in both VPCs for NAT connectivity test") //nolint:err113
	}

	slog.Debug("Found servers for NAT test", "vpc1", vpc1.Name, "servers", vpc1Servers, "vpc2", vpc2.Name, "servers", vpc2Servers)

	// Get SSH configs for servers
	sshConfigs := map[string]*sshutil.Config{}
	for _, serverName := range append(vpc1Servers, vpc2Servers...) {
		// Find VM by name
		var vm VM
		found := false
		for _, v := range testCtx.vlab.VMs {
			if v.Name == serverName {
				vm = v
				found = true

				break
			}
		}
		if !found {
			return fmt.Errorf("VM not found for server %s", serverName) //nolint:err113
		}

		sshCfg, err := testCtx.vlabCfg.SSHVM(ctx, testCtx.vlab, vm)
		if err != nil {
			return fmt.Errorf("getting ssh config for %s: %w", serverName, err)
		}

		sshConfigs[serverName] = sshCfg
	}

	// Discover server IPs
	serverIPs := map[string]netip.Addr{}
	for _, serverName := range append(vpc1Servers, vpc2Servers...) {
		sshCfg := sshConfigs[serverName]
		stdout, stderr, err := sshCfg.Run(ctx, "ip -o -4 addr show | awk '{print $2, $4}'")
		if err != nil {
			return fmt.Errorf("getting IP for %s: %w: %s", serverName, err, stderr)
		}

		found := false
		for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			if excludedInterfaces[fields[0]] {
				continue
			}

			addr, err := netip.ParsePrefix(fields[1])
			if err != nil {
				continue
			}

			serverIPs[serverName] = addr.Addr()
			found = true
			slog.Debug("Discovered server IP", "server", serverName, "ip", addr.Addr().String())

			break
		}

		if !found {
			return fmt.Errorf("no IP found for server %s", serverName) //nolint:err113
		}
	}

	// Parse NAT pools
	var vpc2NATPoolStart netip.Addr
	if len(vpc2NATPool) > 0 {
		prefix, err := netip.ParsePrefix(vpc2NATPool[0])
		if err != nil {
			return fmt.Errorf("parsing vpc2 NAT pool: %w", err)
		}
		vpc2NATPoolStart = prefix.Addr()
	}

	// Get VPC subnet starts for offset calculation
	vpc2SubnetStart := netip.Addr{}
	for _, subnet := range vpc2.Spec.Subnets {
		if prefix, err := netip.ParsePrefix(subnet.Subnet); err == nil {
			vpc2SubnetStart = prefix.Addr()

			break
		}
	}

	// Test connectivity: vpc1 -> vpc2
	// If vpc2NATPool is set, ping vpc2's NAT IPs (destination NAT)
	// If vpc2NATPool is empty, ping vpc2's real IPs (source NAT on vpc1 side)
	var pingErrors []*PingError

	for _, serverA := range vpc1Servers {
		for _, serverB := range vpc2Servers {
			destIP := serverIPs[serverB]

			// Calculate expected NAT IP for destination (only if vpc2 has NAT configured)
			natDestIP := destIP
			if len(vpc2NATPool) > 0 && !vpc2NATPoolStart.IsUnspecified() {
				var err error
				natDestIP, err = calculateStaticNATIP(destIP, vpc2SubnetStart, vpc2NATPoolStart)
				if err != nil {
					return fmt.Errorf("calculating NAT IP for %s: %w", serverB, err)
				}
				slog.Debug("Calculated NAT IP", "server", serverB, "real", destIP, "nat", natDestIP)
			}

			// Use checkPing from testing.go (nil semaphore = no parallelism limiting needed for sequential calls)
			if pe := checkPing(ctx, testCtx.tcOpts.PingsCount, nil, serverA, serverB, sshConfigs[serverA], natDestIP, nil, true); pe != nil {
				pingErrors = append(pingErrors, pe)
			}
		}
	}

	if len(pingErrors) > 0 {
		var errMsgs []string
		for _, pe := range pingErrors {
			errMsgs = append(errMsgs, pe.Error())
		}

		return fmt.Errorf("NAT ping test failed with %d errors: %s", len(pingErrors), strings.Join(errMsgs, "; ")) //nolint:err113
	}

	slog.Debug("NAT ping tests completed successfully, starting iperf3 tests")

	// Test iperf3: vpc1 -> vpc2 using checkIPerf from testing.go
	var iperfErrors []*IperfError
	reachability := Reachability{
		Reachable: true,
		Reason:    ReachabilityReasonGatewayPeering,
	}

	for _, serverA := range vpc1Servers {
		for _, serverB := range vpc2Servers {
			destIP := serverIPs[serverB]

			// Calculate expected NAT IP for destination (only if vpc2 has NAT configured)
			natDestIP := destIP
			if len(vpc2NATPool) > 0 && !vpc2NATPoolStart.IsUnspecified() {
				var err error
				natDestIP, err = calculateStaticNATIP(destIP, vpc2SubnetStart, vpc2NATPoolStart)
				if err != nil {
					return fmt.Errorf("calculating NAT IP for %s: %w", serverB, err)
				}
			}

			// Use checkIPerf from testing.go
			if ie := checkIPerf(ctx, testCtx.tcOpts, serverA, serverB, sshConfigs[serverA], sshConfigs[serverB], natDestIP, reachability); ie != nil {
				iperfErrors = append(iperfErrors, ie)
			}
		}
	}

	if len(iperfErrors) > 0 {
		var errMsgs []string
		for _, ie := range iperfErrors {
			errMsgs = append(errMsgs, ie.Error())
		}

		return fmt.Errorf("NAT iperf3 test failed with %d errors: %s", len(iperfErrors), strings.Join(errMsgs, "; ")) //nolint:err113
	}

	slog.Info("NAT connectivity test (ping+iperf3) completed successfully", "took", time.Since(startTime))

	return nil
}

// Test gateway peering with masquerade source NAT (only VPC1 has masquerade NAT configured)
// NOTE: Masquerade NAT on both sides of a peering is not supported (see dataplane#1248)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringMasqueradeSourceNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for NAT gateway peering test") //nolint:goerr113
	}

	// Sort VPCs to ensure consistent selection
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Only VPC1 has masquerade NAT - VPC1's traffic will be source-NATed
	vpc1NATCIDR := []string{"192.168.11.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, &GwPeeringOptions{
		VPC1NATCIDR: vpc1NATCIDR,
		VPC1NATMode: NATModeMasquerade,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up NAT gateway peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Test connectivity - VPC2 has no NAT, so we ping real IPs
	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, vpc1NATCIDR, nil); err != nil {
		return false, nil, fmt.Errorf("testing NAT gateway peering connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with static source NAT (only VPC1 has NAT configured)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringStaticSourceNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for static source NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Only VPC1 has NAT - this means VPC1's traffic will be source-NATed
	vpc1NATCIDR := []string{"192.168.21.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, &GwPeeringOptions{
		VPC1NATCIDR: vpc1NATCIDR,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up static source NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Test connectivity - VPC2 has no NAT, so we ping real IPs
	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, vpc1NATCIDR, nil); err != nil {
		return false, nil, fmt.Errorf("testing static source NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with static destination NAT (only VPC2 has NAT configured)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringStaticDestinationNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for static destination NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Only VPC2 has NAT - VPC1 will ping VPC2's NAT IPs (destination NAT)
	vpc2NATCIDR := []string{"192.168.22.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, &GwPeeringOptions{
		VPC2NATCIDR: vpc2NATCIDR,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up static destination NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Test connectivity - VPC1 pings VPC2's NAT IPs
	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, nil, vpc2NATCIDR); err != nil {
		return false, nil, fmt.Errorf("testing static destination NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with bidirectional static NAT (both VPCs have NAT configured)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringBidirectionalStaticNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for bidirectional NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Both VPCs have NAT configured - each side sees the other's NAT addresses
	vpc1NATCIDR := []string{"192.168.31.0/24"}
	vpc2NATCIDR := []string{"192.168.32.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, &GwPeeringOptions{
		VPC1NATCIDR: vpc1NATCIDR,
		VPC2NATCIDR: vpc2NATCIDR,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up bidirectional NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, vpc1NATCIDR, vpc2NATCIDR); err != nil {
		return false, nil, fmt.Errorf("testing bidirectional NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with overlapping NAT pools (edge case testing)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringOverlapNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 3 {
		return true, nil, fmt.Errorf("not enough VPCs for overlap NAT test (need 3)") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	existingVPC := &vpcs.Items[0]
	newVPC := &vpcs.Items[1]

	// Create first peering with NAT
	existingVPCNATCIDR := []string{"192.168.61.0/24"}
	newVPCNATCIDR := []string{"192.168.62.0/24"}

	appendGwPeeringSpec(gwPeerings, existingVPC, newVPC, &GwPeeringOptions{
		VPC1NATCIDR: existingVPCNATCIDR,
		VPC2NATCIDR: newVPCNATCIDR,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up overlap NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Test connectivity using custom NAT-aware connectivity testing
	if err := testCtx.testNATGatewayConnectivity(ctx, existingVPC, newVPC, existingVPCNATCIDR, newVPCNATCIDR); err != nil {
		return false, nil, fmt.Errorf("testing overlap NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with combined masquerade and port-forwarding NAT
func (testCtx *VPCPeeringTestCtx) gatewayPeeringMasqueradePortForwardNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for masquerade+port-forward NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	portForwardRules := []gwapi.PeeringNATPortForwardEntry{
		{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"},
	}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, &GwPeeringOptions{
		VPC1NATCIDR:          []string{"192.168.51.0/24"},
		VPC1NATMode:          NATModeMasqueradePortForward,
		VPC1PortForwardRules: portForwardRules,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up masquerade+port-forward NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, []string{"192.168.51.0/24"}, nil); err != nil {
		return false, nil, fmt.Errorf("testing masquerade+port-forward NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// getNATTestCases returns the NAT test cases to be added to the multi-VPC single-subnet suite
func getNATTestCases(testCtx *VPCPeeringTestCtx) []JUnitTestCase {
	return []JUnitTestCase{
		{
			Name: "Gateway Peering Masquerade Source NAT",
			F:    testCtx.gatewayPeeringMasqueradeSourceNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Static Source NAT",
			F:    testCtx.gatewayPeeringStaticSourceNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Static Destination NAT",
			F:    testCtx.gatewayPeeringStaticDestinationNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Bidirectional Static NAT",
			F:    testCtx.gatewayPeeringBidirectionalStaticNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Overlap NAT",
			F:    testCtx.gatewayPeeringOverlapNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Masquerade + Port Forward NAT",
			F:    testCtx.gatewayPeeringMasqueradePortForwardNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
	}
}
