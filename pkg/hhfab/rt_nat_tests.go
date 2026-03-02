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

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
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

	// Check if all switches are virtual and skip iperf min speed check if so
	switches := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}
	allVS := len(switches.Items) > 0
	for _, sw := range switches.Items {
		if sw.Spec.Profile != meta.SwitchProfileVS {
			allVS = false
			break
		}
	}
	if allVS {
		slog.Warn("All switches are virtual, ignoring IPerf min speed for NAT tests")
		testCtx.tcOpts.IPerfsMinSpeed = 0
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
	var vpc1NATPoolStart, vpc2NATPoolStart netip.Addr
	if len(vpc1NATPool) > 0 {
		prefix, err := netip.ParsePrefix(vpc1NATPool[0])
		if err != nil {
			return fmt.Errorf("parsing vpc1 NAT pool: %w", err)
		}
		vpc1NATPoolStart = prefix.Addr()
	}
	if len(vpc2NATPool) > 0 {
		prefix, err := netip.ParsePrefix(vpc2NATPool[0])
		if err != nil {
			return fmt.Errorf("parsing vpc2 NAT pool: %w", err)
		}
		vpc2NATPoolStart = prefix.Addr()
	}

	// Get VPC subnet starts for offset calculation
	vpc1SubnetStart := netip.Addr{}
	for _, subnet := range vpc1.Spec.Subnets {
		if prefix, err := netip.ParsePrefix(subnet.Subnet); err == nil {
			vpc1SubnetStart = prefix.Addr()

			break
		}
	}
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

		return fmt.Errorf("NAT iperf3 test (vpc1->vpc2) failed with %d errors: %s", len(iperfErrors), strings.Join(errMsgs, "; ")) //nolint:err113
	}

	// Test reverse direction: vpc2 -> vpc1 (only when vpc1 has NAT configured)
	if len(vpc1NATPool) > 0 && !vpc1NATPoolStart.IsUnspecified() {
		slog.Debug("Testing reverse direction (vpc2 -> vpc1) NAT connectivity")
		pingErrors = nil

		for _, serverA := range vpc2Servers {
			for _, serverB := range vpc1Servers {
				destIP := serverIPs[serverB]

				// Calculate expected NAT IP for destination (vpc1's NAT IP)
				natDestIP, err := calculateStaticNATIP(destIP, vpc1SubnetStart, vpc1NATPoolStart)
				if err != nil {
					return fmt.Errorf("calculating NAT IP for %s: %w", serverB, err)
				}
				slog.Debug("Calculated reverse NAT IP", "server", serverB, "real", destIP, "nat", natDestIP)

				// Use checkPing from testing.go
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

			return fmt.Errorf("NAT ping test (vpc2->vpc1) failed with %d errors: %s", len(pingErrors), strings.Join(errMsgs, "; ")) //nolint:err113
		}

		slog.Debug("Reverse NAT ping tests completed successfully, starting iperf3 tests")

		// Test iperf3: vpc2 -> vpc1
		iperfErrors = nil

		for _, serverA := range vpc2Servers {
			for _, serverB := range vpc1Servers {
				destIP := serverIPs[serverB]

				natDestIP, err := calculateStaticNATIP(destIP, vpc1SubnetStart, vpc1NATPoolStart)
				if err != nil {
					return fmt.Errorf("calculating NAT IP for %s: %w", serverB, err)
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

			return fmt.Errorf("NAT iperf3 test (vpc2->vpc1) failed with %d errors: %s", len(iperfErrors), strings.Join(errMsgs, "; ")) //nolint:err113
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

// Test gateway peering with static destination NAT (only VPC2 has NAT configured)
// Example:
//
// Spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	  vpc-02:
//	    Expose:
//	      As:
//	        Cidr:  192.168.22.0/24
//	      Ips:
//	        Cidr:  10.50.2.0/24
//	      Nat:
//	        Static:
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

	// Parse the existing subnet to determine the /16 range for the new namespace
	existingPrefix, err := netip.ParsePrefix(existingSubnetCIDR)
	if err != nil {
		return false, nil, fmt.Errorf("parsing existing subnet CIDR %s: %w", existingSubnetCIDR, err)
	}
	// Get the /16 containing this subnet (e.g., 10.50.1.0/24 -> 10.50.0.0/16)
	addr := existingPrefix.Addr().As4()
	namespaceSubnet := fmt.Sprintf("%d.%d.0.0/16", addr[0], addr[1])

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
		_ = testCtx.kube.Delete(ctx, overlapNS)

		return false, nil, fmt.Errorf("creating overlap VPC: %w", err)
	}
	slog.Debug("Created overlap VPC", "name", overlapVPCName, "subnet", existingSubnetCIDR, "vlan", newVLAN)

	// Define cleanup function to restore the server to the original VPC
	cleanup := func() error {
		slog.Debug("Cleaning up overlap NAT test resources")

		// Delete the new attachment first
		newAtt := &vpcapi.VPCAttachment{}
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
			Namespace: kmetav1.NamespaceDefault,
			Name:      originalAttachmentName,
		}, newAtt); err == nil {
			if err := testCtx.kube.Delete(ctx, newAtt); err != nil {
				slog.Warn("Failed to delete new attachment during cleanup", "error", err)
			}
		}

		// Restore original attachment
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
			slog.Warn("Failed to restore original attachment during cleanup", "error", err)
		}

		// Delete the overlap VPC
		if err := testCtx.kube.Delete(ctx, overlapVPC); err != nil {
			slog.Warn("Failed to delete overlap VPC during cleanup", "error", err)
		}

		// Delete the overlap namespace
		if err := testCtx.kube.Delete(ctx, overlapNS); err != nil {
			slog.Warn("Failed to delete overlap namespace during cleanup", "error", err)
		}

		// Wait for fabric to converge
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			slog.Warn("Failed to wait for ready during cleanup", "error", err)
		}

		// Reconfigure server network back to original VPC
		sshCfg, err := testCtx.getSSH(ctx, targetServer)
		if err != nil {
			slog.Warn("Failed to get SSH config during cleanup", "error", err)

			return nil
		}

		netconfCmd, err := GetServerNetconfCmd(targetConn, originalVLAN, testCtx.setupOpts.HashPolicy)
		if err != nil {
			slog.Warn("Failed to get netconf command during cleanup", "error", err)

			return nil
		}

		if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
			slog.Warn("Failed to cleanup server network during cleanup", "error", err, "stderr", stderr)
		}
		if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet "+netconfCmd); err != nil {
			slog.Warn("Failed to reconfigure server network during cleanup", "error", err, "stderr", stderr)
		}

		slog.Debug("Overlap NAT test cleanup completed")

		return nil
	}

	// Delete the old attachment
	if err := testCtx.kube.Delete(ctx, targetAttachment); err != nil {
		_ = cleanup()

		return false, nil, fmt.Errorf("deleting original attachment: %w", err)
	}
	slog.Debug("Deleted original attachment", "attachment", targetAttachment.Name)

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
		_ = cleanup()

		return false, nil, fmt.Errorf("creating new attachment to overlap VPC: %w", err)
	}
	slog.Debug("Created new attachment to overlap VPC", "attachment", newAttachment.Name)

	// Wait for fabric to be ready with new configuration
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		_ = cleanup()

		return false, nil, fmt.Errorf("waiting for switches to be ready after VPC changes: %w", err)
	}

	// Configure the server's network for the new VPC
	sshCfg, err := testCtx.getSSH(ctx, targetServer)
	if err != nil {
		_ = cleanup()

		return false, nil, fmt.Errorf("getting SSH config for server %s: %w", targetServer, err)
	}

	netconfCmd, err := GetServerNetconfCmd(targetConn, newVLAN, testCtx.setupOpts.HashPolicy)
	if err != nil {
		_ = cleanup()

		return false, nil, fmt.Errorf("getting netconf command for server %s: %w", targetServer, err)
	}

	// Cleanup and reconfigure server network
	slog.Debug("Reconfiguring server network", "server", targetServer, "vlan", newVLAN)
	if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
		_ = cleanup()

		return false, nil, fmt.Errorf("cleaning up server %s network: %w: %s", targetServer, err, stderr)
	}
	if _, stderr, err := sshCfg.Run(ctx, "/opt/bin/hhnet "+netconfCmd); err != nil {
		_ = cleanup()

		return false, nil, fmt.Errorf("configuring server %s network: %w: %s", targetServer, err, stderr)
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
		_ = cleanup()

		return false, nil, fmt.Errorf("setting up overlap NAT gateway peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		_ = cleanup()

		return false, nil, fmt.Errorf("waiting for switches to be ready after peering: %w", err)
	}

	// Test connectivity - both VPCs have overlapping subnets, NAT resolves them
	slog.Info("Testing connectivity between VPCs with overlapping subnets via NAT",
		"existingVPC", existingVPC.Name, "existingCIDR", existingSubnetCIDR,
		"overlapVPC", overlapVPC.Name, "overlapCIDR", existingSubnetCIDR,
		"existingNAT", existingVPCNATCIDR, "overlapNAT", overlapVPCNATCIDR)

	testErr := testCtx.testNATGatewayConnectivity(ctx, existingVPC, overlapVPC, existingVPCNATCIDR, overlapVPCNATCIDR)

	// If pauseOnFail is set and test failed, pause before cleanup for manual debugging
	if testCtx.pauseOnFail && testErr != nil {
		slog.Warn("Test failed, pausing before cleanup for manual debugging")
		if err := pauseOnFailure(ctx); err != nil {
			slog.Warn("Pause failed, ignoring", "err", err.Error())
		}
	}

	// Always cleanup, regardless of test result
	_ = cleanup()

	if testErr != nil {
		return false, nil, fmt.Errorf("testing overlap NAT connectivity: %w", testErr)
	}

	slog.Info("Overlap NAT test completed successfully")

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
	}
}
