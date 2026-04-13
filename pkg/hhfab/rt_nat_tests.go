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

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
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
	if !sourceIP.Is4() || !sourceSubnet.Is4() || !natPoolStart.Is4() {
		return netip.Addr{}, fmt.Errorf("only IPv4 NAT is currently supported") //nolint:err113
	}

	sourceBytes := sourceIP.As4()
	subnetBytes := sourceSubnet.As4()

	sourceInt := binary.BigEndian.Uint32(sourceBytes[:])
	subnetInt := binary.BigEndian.Uint32(subnetBytes[:])

	if sourceInt < subnetInt {
		return netip.Addr{}, fmt.Errorf("source IP %s is before subnet start %s", sourceIP, sourceSubnet) //nolint:err113
	}

	// Calculate offset from source subnet start
	offset := sourceInt - subnetInt

	// Add offset to NAT pool start
	natPoolBytes := natPoolStart.As4()
	natPoolInt := binary.BigEndian.Uint32(natPoolBytes[:])
	natIPInt := natPoolInt + offset

	var natIPBytes [4]byte
	binary.BigEndian.PutUint32(natIPBytes[:], natIPInt)

	return netip.AddrFrom4(natIPBytes), nil
}

// testNATGatewayConnectivity performs E2E connectivity testing for NAT gateway peering.
// It discovers server IPs, calculates expected NAT IPs, and performs ping/iperf3 tests
// using the shared checkPing and checkIPerf functions from testing.go.
// NOTE: This uses calculateStaticNATIP which couples to the dataplane NAT algorithm.
// The function supports both source NAT and destination NAT:
// - If vpc2NATPool is set: vpc1 pings vpc2 using vpc2's NAT IPs (destination NAT)
// - If vpc2NATPool is empty: vpc1 pings vpc2's real IPs (source NAT on vpc1 side)
// - If vpc1NATPool is set: also test vpc2 -> vpc1 direction (bidirectional NAT)
func (testCtx *VPCPeeringTestCtx) testNATGatewayConnectivity(
	ctx context.Context,
	vpc1, vpc2 *vpcapi.VPC,
	vpc1NATPool, vpc2NATPool []string,
) error {
	startTime := time.Now()
	slog.Info("Testing NAT gateway peering connectivity")

	// Validate NAT pool parameters - we only support a single CIDR per VPC for now
	if len(vpc1NATPool) > 1 || len(vpc2NATPool) > 1 {
		return fmt.Errorf("multiple NAT CIDRs per VPC not supported, got vpc1=%d vpc2=%d", len(vpc1NATPool), len(vpc2NATPool)) //nolint:goerr113
	}

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

		var eligibleAddrs []netip.Addr
		for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
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

			eligibleAddrs = append(eligibleAddrs, addr.Addr())
		}

		if len(eligibleAddrs) == 0 {
			return fmt.Errorf("no IP found for server %s", serverName) //nolint:err113
		}
		if len(eligibleAddrs) > 1 {
			return fmt.Errorf("server %s has multiple IPs %v, NAT test requires single IP", serverName, eligibleAddrs) //nolint:err113
		}
		serverIPs[serverName] = eligibleAddrs[0]
		slog.Debug("Discovered server IP", "server", serverName, "ip", eligibleAddrs[0].String())
	}

	// Parse NAT pools - use Masked() to get the network address
	var vpc1NATPoolStart, vpc2NATPoolStart netip.Addr
	if len(vpc1NATPool) > 0 {
		prefix, err := netip.ParsePrefix(vpc1NATPool[0])
		if err != nil {
			return fmt.Errorf("parsing vpc1 NAT pool: %w", err)
		}
		vpc1NATPoolStart = prefix.Masked().Addr()
	}
	if len(vpc2NATPool) > 0 {
		prefix, err := netip.ParsePrefix(vpc2NATPool[0])
		if err != nil {
			return fmt.Errorf("parsing vpc2 NAT pool: %w", err)
		}
		vpc2NATPoolStart = prefix.Masked().Addr()
	}

	// Get VPC subnet starts for offset calculation
	// NAT test requires exactly one subnet per VPC to avoid ambiguity in offset calculation
	if len(vpc1.Spec.Subnets) != 1 {
		return fmt.Errorf("VPC %s has %d subnets, NAT test requires exactly one", vpc1.Name, len(vpc1.Spec.Subnets)) //nolint:err113
	}
	if len(vpc2.Spec.Subnets) != 1 {
		return fmt.Errorf("VPC %s has %d subnets, NAT test requires exactly one", vpc2.Name, len(vpc2.Spec.Subnets)) //nolint:err113
	}
	var vpc1SubnetStart, vpc2SubnetStart netip.Addr
	for _, subnet := range vpc1.Spec.Subnets {
		prefix, err := netip.ParsePrefix(subnet.Subnet)
		if err != nil {
			return fmt.Errorf("parsing VPC %s subnet %s: %w", vpc1.Name, subnet.Subnet, err)
		}
		vpc1SubnetStart = prefix.Masked().Addr()
	}
	for _, subnet := range vpc2.Spec.Subnets {
		prefix, err := netip.ParsePrefix(subnet.Subnet)
		if err != nil {
			return fmt.Errorf("parsing VPC %s subnet %s: %w", vpc2.Name, subnet.Subnet, err)
		}
		vpc2SubnetStart = prefix.Masked().Addr()
	}

	// Helper to calculate destination IP (NAT IP if pool configured, real IP otherwise)
	getDestIP := func(serverName string, destSubnetStart, natPoolStart netip.Addr) (netip.Addr, error) {
		realIP := serverIPs[serverName]
		if natPoolStart.IsValid() {
			natIP, err := calculateStaticNATIP(realIP, destSubnetStart, natPoolStart)
			if err != nil {
				return netip.Addr{}, fmt.Errorf("calculating NAT IP for %s: %w", serverName, err)
			}
			slog.Debug("Using NAT IP", "server", serverName, "real", realIP, "nat", natIP)

			return natIP, nil
		}

		return realIP, nil
	}

	// Helper to test connectivity in one direction
	testDirection := func(label string, fromServers, toServers []string, toSubnetStart, toNATPoolStart netip.Addr) error {
		slog.Debug("Testing NAT connectivity", "direction", label)

		// Ping tests
		var pingErrors []*PingError
		for _, serverA := range fromServers {
			for _, serverB := range toServers {
				destIP, err := getDestIP(serverB, toSubnetStart, toNATPoolStart)
				if err != nil {
					return err
				}
				if pe := checkPing(ctx, testCtx.tcOpts.PingsCount, nil, serverA, serverB, sshConfigs[serverA], destIP, nil, true); pe != nil {
					pingErrors = append(pingErrors, pe)
				}
			}
		}
		if len(pingErrors) > 0 {
			var errMsgs []string
			for _, pe := range pingErrors {
				errMsgs = append(errMsgs, pe.Error())
			}

			return fmt.Errorf("NAT ping test (%s) failed with %d errors: %s", label, len(pingErrors), strings.Join(errMsgs, "; ")) //nolint:goerr113
		}

		// Iperf tests
		slog.Debug("NAT ping tests completed, starting iperf3 tests", "direction", label)
		reachability := Reachability{Reachable: true, Reason: ReachabilityReasonGatewayPeering}
		var iperfErrors []*IperfError
		for _, serverA := range fromServers {
			for _, serverB := range toServers {
				destIP, err := getDestIP(serverB, toSubnetStart, toNATPoolStart)
				if err != nil {
					return err
				}
				if ie := checkIPerf(ctx, testCtx.tcOpts, serverA, serverB, sshConfigs[serverA], sshConfigs[serverB], destIP, reachability); ie != nil {
					iperfErrors = append(iperfErrors, ie)
				}
			}
		}
		if len(iperfErrors) > 0 {
			var errMsgs []string
			for _, ie := range iperfErrors {
				errMsgs = append(errMsgs, ie.Error())
			}

			return fmt.Errorf("NAT iperf3 test (%s) failed with %d errors: %s", label, len(iperfErrors), strings.Join(errMsgs, "; ")) //nolint:goerr113
		}

		return nil
	}

	// Test vpc1 -> vpc2 direction (always)
	if err := testDirection("vpc1->vpc2", vpc1Servers, vpc2Servers, vpc2SubnetStart, vpc2NATPoolStart); err != nil {
		return err
	}

	// Test vpc2 -> vpc1 direction (only if vpc1 has static NAT - masquerade doesn't support being a destination)
	if vpc1NATPoolStart.IsValid() {
		if err := testDirection("vpc2->vpc1", vpc2Servers, vpc1Servers, vpc1SubnetStart, vpc1NATPoolStart); err != nil {
			return err
		}
	}

	slog.Info("NAT connectivity test (ping+iperf3) completed successfully", "took", time.Since(startTime))

	return nil
}

// Test gateway peering with masquerade source NAT (only VPC1 has masquerade NAT configured)
// Example:
//
//		Spec:
//	  Gateway Group:  default
//		  Peering:
//		    vpc-01:
//		      Expose:
//		        As:
//		          Cidr:  192.168.11.0/24
//		        Ips:
//		          Cidr:  10.50.1.0/24
//		        Nat:
//		          Masquerade:
//		            Idle Timeout:  5m0s
//		    vpc-02:
//		      Expose:
//		        Ips:
//		          Cidr:  10.50.2.0/24
//
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
	// Note: For masquerade NAT, we only test VPC1 -> VPC2 direction (pass nil for vpc1NATPool)
	// because masquerade is stateful and doesn't support VPC2 initiating connections to VPC1's NAT IPs
	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, nil, nil); err != nil {
		return false, nil, fmt.Errorf("testing NAT gateway peering connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with static source NAT (only VPC1 has NAT configured)
// Example:
//
// Spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.21.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Static:
//	  vpc-02:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.2.0/24
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

// Test gateway peering with bidirectional static NAT (both VPCs have NAT configured)
// Example:
//
// Spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.31.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Static:
//	  vpc-02:
//	    Expose:
//	      As:
//	        Cidr:  192.168.32.0/24
//	      Ips:
//	        Cidr:  10.50.2.0/24
//	      Nat:
//	        Static:
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

// Test gateway peering with overlapping VPC subnets resolved via NAT.
// This test creates a new IPv4Namespace with the same subnet range as the default namespace,
// creates a new VPC with a subnet that overlaps an existing VPC, moves a server to the new VPC,
// and tests connectivity via gateway peering with NAT on both sides.
// Example:
//
// Spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.71.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Static:
//	  Vpc - Overlap:
//	    Expose:
//	      As:
//	        Cidr:  192.168.72.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Static:
func (testCtx *VPCPeeringTestCtx) gatewayPeeringOverlapNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	const (
		overlapNSName  = "overlap-ns"  // max 11 chars for IPv4Namespace name
		overlapVPCName = "vpc-overlap" // max 11 chars for VPC name (VRF interface limit)
		overlapSubnet  = "overlap-sub" // subnet name within the new VPC
	)

	// Get existing VPCs
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		slog.Info("Not enough VPCs for overlap NAT test", "have", len(vpcs.Items), "need", 2)

		return true, nil, nil
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	// Use VPC1 as the "existing" VPC we'll peer with
	existingVPC := &vpcs.Items[0]

	// Get the first subnet from the existing VPC - we'll create an overlapping one
	var existingSubnetCIDR string
	for _, subnet := range existingVPC.Spec.Subnets {
		existingSubnetCIDR = subnet.Subnet

		break
	}
	if existingSubnetCIDR == "" {
		return false, nil, fmt.Errorf("existing VPC %s has no subnets", existingVPC.Name) //nolint:goerr113
	}

	// Get the IPv4Namespace from the existing VPC to create an overlapping namespace
	existingNS := &vpcapi.IPv4Namespace{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
		Name:      existingVPC.Spec.IPv4Namespace,
		Namespace: kmetav1.NamespaceDefault,
	}, existingNS); err != nil {
		return false, nil, fmt.Errorf("getting IPv4Namespace %s: %w", existingVPC.Spec.IPv4Namespace, err)
	}
	if len(existingNS.Spec.Subnets) == 0 {
		return false, nil, fmt.Errorf("IPv4Namespace %s has no subnets", existingNS.Name) //nolint:goerr113
	}
	// Use the same subnet range as the existing namespace to create overlap
	namespaceSubnet := existingNS.Spec.Subnets[0]

	// Find a server from VPC2 that we can move to the new overlapping VPC
	donorVPC := &vpcs.Items[1]
	attachments := &vpcapi.VPCAttachmentList{}
	if err := testCtx.kube.List(ctx, attachments); err != nil {
		return false, nil, fmt.Errorf("listing VPC attachments: %w", err)
	}

	var targetAttachment *vpcapi.VPCAttachment
	var targetServer string
	var targetConn *wiringapi.Connection
	var originalSubnetName string
	for i := range attachments.Items {
		att := &attachments.Items[i]
		if att.Spec.VPCName() == donorVPC.Name {
			// Get the connection for this attachment
			conn := &wiringapi.Connection{}
			if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
				Namespace: kmetav1.NamespaceDefault,
				Name:      att.Spec.Connection,
			}, conn); err != nil {
				continue
			}
			// Get the server name from the connection using Endpoints()
			_, serverNames, _, _, err := conn.Spec.Endpoints()
			if err != nil || len(serverNames) != 1 {
				continue
			}
			targetAttachment = att
			targetServer = serverNames[0]
			targetConn = conn
			originalSubnetName = att.Spec.SubnetName()

			break
		}
	}
	if targetAttachment == nil {
		return false, nil, fmt.Errorf("no suitable server found to move to overlap VPC") //nolint:goerr113
	}

	slog.Debug("Found target server for overlap test",
		"server", targetServer,
		"currentVPC", donorVPC.Name,
		"attachment", targetAttachment.Name)

	// Store original values for cleanup
	originalAttachmentName := targetAttachment.Name
	originalConnection := targetAttachment.Spec.Connection
	originalSubnet := targetAttachment.Spec.Subnet
	originalVLAN := donorVPC.Spec.Subnets[originalSubnetName].VLAN

	// Create the new IPv4Namespace with the same subnet range as the existing VPC's namespace
	overlapNS := &vpcapi.IPv4Namespace{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       vpcapi.KindIPv4Namespace,
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      overlapNSName,
			Namespace: kmetav1.NamespaceDefault,
		},
		Spec: vpcapi.IPv4NamespaceSpec{
			Subnets: []string{namespaceSubnet}, // Same range as the existing VPC's namespace
		},
	}
	if err := testCtx.kube.Create(ctx, overlapNS); err != nil {
		return false, nil, fmt.Errorf("creating overlap IPv4Namespace: %w", err)
	}
	slog.Debug("Created overlap IPv4Namespace", "name", overlapNSName)

	// Use individual reverts - each step adds its own cleanup function
	reverts := []RevertFunc{}

	// Revert: delete overlap namespace
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Reverting: deleting overlap namespace", "name", overlapNSName)
		if err := testCtx.kube.Delete(ctx, overlapNS); err != nil {
			return fmt.Errorf("deleting overlap namespace: %w", err)
		}

		return nil
	})

	// Create the new VPC in the overlap namespace with the SAME subnet CIDR as existingVPC
	newVLAN := originalVLAN + 100 // Use different VLAN to avoid conflicts
	overlapVPC := &vpcapi.VPC{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       vpcapi.KindVPC,
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      overlapVPCName,
			Namespace: kmetav1.NamespaceDefault,
		},
		Spec: vpcapi.VPCSpec{
			Mode:          existingVPC.Spec.Mode, // Use same VPC mode as existing VPCs
			IPv4Namespace: overlapNSName,
			VLANNamespace: "default",
			Subnets: map[string]*vpcapi.VPCSubnet{
				overlapSubnet: {
					Subnet: existingSubnetCIDR, // Same CIDR as existing VPC!
					VLAN:   newVLAN,
					DHCP: vpcapi.VPCDHCP{
						Enable: true,
					},
				},
			},
		},
	}
	if err := testCtx.kube.Create(ctx, overlapVPC); err != nil {
		return false, reverts, fmt.Errorf("creating overlap VPC: %w", err)
	}
	slog.Debug("Created overlap VPC", "name", overlapVPCName, "subnet", existingSubnetCIDR, "vlan", newVLAN)

	// Revert: delete overlap VPC
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Reverting: deleting overlap VPC", "name", overlapVPCName)
		if err := testCtx.kube.Delete(ctx, overlapVPC); err != nil {
			return fmt.Errorf("deleting overlap VPC: %w", err)
		}

		return nil
	})

	// Delete the old attachment
	if err := testCtx.kube.Delete(ctx, targetAttachment); err != nil {
		return false, reverts, fmt.Errorf("deleting original attachment: %w", err)
	}
	slog.Debug("Deleted original attachment", "attachment", targetAttachment.Name)

	// Revert: restore original attachment and reconfigure server network
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Reverting: restoring original attachment", "name", originalAttachmentName)
		// First delete any existing attachment with this name
		existingAtt := &vpcapi.VPCAttachment{}
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
			Namespace: kmetav1.NamespaceDefault,
			Name:      originalAttachmentName,
		}, existingAtt); err == nil {
			if err := testCtx.kube.Delete(ctx, existingAtt); err != nil {
				slog.Warn("Failed to delete existing attachment during revert", "error", err)
			}
		}
		// Restore the original attachment
		restoredAttachment := &vpcapi.VPCAttachment{
			TypeMeta: kmetav1.TypeMeta{
				Kind:       vpcapi.KindVPCAttachment,
				APIVersion: vpcapi.GroupVersion.String(),
			},
			ObjectMeta: kmetav1.ObjectMeta{
				Name:      originalAttachmentName,
				Namespace: kmetav1.NamespaceDefault,
			},
			Spec: vpcapi.VPCAttachmentSpec{
				Connection: originalConnection,
				Subnet:     originalSubnet,
			},
		}
		if err := testCtx.kube.Create(ctx, restoredAttachment); err != nil {
			return fmt.Errorf("restoring original attachment: %w", err)
		}

		// Wait for fabric to configure the original VLAN on the switch
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			return fmt.Errorf("waiting for fabric ready after restoring attachment: %w", err)
		}

		// Reconfigure server network back to original VLAN
		slog.Debug("Reverting: reconfiguring server network", "server", targetServer, "vlan", originalVLAN)
		sshCfg, err := testCtx.getSSH(ctx, targetServer)
		if err != nil {
			return fmt.Errorf("getting SSH config for %s: %w", targetServer, err)
		}
		netconfCmd, err := GetServerNetconfCmd(targetConn, ServerNetconfOpts{
			VLAN:       originalVLAN,
			HashPolicy: testCtx.setupOpts.HashPolicy,
		})
		if err != nil {
			return fmt.Errorf("getting netconf command for %s: %w", targetServer, err)
		}
		if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
			slog.Warn("Failed to cleanup server network during revert", "error", err, "stderr", stderr)
		}
		if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet "+netconfCmd); err != nil {
			return fmt.Errorf("reconfiguring server %s network: %w: %s", targetServer, err, stderr)
		}

		return nil
	})

	// Create new attachment to the overlap VPC
	newAttachment := &vpcapi.VPCAttachment{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       vpcapi.KindVPCAttachment,
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      originalAttachmentName,
			Namespace: kmetav1.NamespaceDefault,
		},
		Spec: vpcapi.VPCAttachmentSpec{
			Connection: originalConnection,
			Subnet:     overlapVPCName + "/" + overlapSubnet,
		},
	}
	if err := testCtx.kube.Create(ctx, newAttachment); err != nil {
		return false, reverts, fmt.Errorf("creating new attachment to overlap VPC: %w", err)
	}
	slog.Debug("Created new attachment to overlap VPC", "attachment", newAttachment.Name)

	// Wait for fabric to be ready with new configuration
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for switches to be ready after VPC changes: %w", err)
	}

	// Configure the server's network for the new VPC
	sshCfg, err := testCtx.getSSH(ctx, targetServer)
	if err != nil {
		return false, reverts, fmt.Errorf("getting SSH config for server %s: %w", targetServer, err)
	}

	netconfCmd, err := GetServerNetconfCmd(targetConn, ServerNetconfOpts{
		VLAN:       newVLAN,
		HashPolicy: testCtx.setupOpts.HashPolicy,
	})
	if err != nil {
		return false, reverts, fmt.Errorf("getting netconf command for server %s: %w", targetServer, err)
	}

	// Cleanup and reconfigure server network
	slog.Debug("Reconfiguring server network", "server", targetServer, "vlan", newVLAN)
	if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
		return false, reverts, fmt.Errorf("cleaning up server %s network: %w: %s", targetServer, err, stderr)
	}
	if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet "+netconfCmd); err != nil {
		return false, reverts, fmt.Errorf("configuring server %s network: %w: %s", targetServer, err, stderr)
	}

	// Give DHCP time to assign an address
	time.Sleep(5 * time.Second)

	// Set up gateway peering with NAT on both sides to resolve the overlap
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	// NAT pools - these translate the overlapping 10.x addresses to unique addresses
	existingVPCNATCIDR := []string{"192.168.71.0/24"}
	overlapVPCNATCIDR := []string{"192.168.72.0/24"}

	appendGwPeeringSpec(gwPeerings, existingVPC, overlapVPC, &GwPeeringOptions{
		VPC1NATCIDR: existingVPCNATCIDR,
		VPC2NATCIDR: overlapVPCNATCIDR,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, reverts, fmt.Errorf("setting up overlap NAT gateway peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for switches to be ready after peering: %w", err)
	}

	// Test connectivity - both VPCs have overlapping subnets, NAT resolves them
	slog.Info("Testing connectivity between VPCs with overlapping subnets via NAT",
		"existingVPC", existingVPC.Name, "existingCIDR", existingSubnetCIDR,
		"overlapVPC", overlapVPC.Name, "overlapCIDR", existingSubnetCIDR,
		"existingNAT", existingVPCNATCIDR, "overlapNAT", overlapVPCNATCIDR)

	if err := testCtx.testNATGatewayConnectivity(ctx, existingVPC, overlapVPC, existingVPCNATCIDR, overlapVPCNATCIDR); err != nil {
		return false, reverts, fmt.Errorf("testing overlap NAT connectivity: %w", err)
	}

	slog.Info("Overlap NAT test completed successfully")

	return false, reverts, nil
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
	}
}
