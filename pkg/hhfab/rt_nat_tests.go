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
	"time"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// peeringSpecs bundles the three peering kinds a NAT test may need to
// install. Returned by natTestSpec.BuildSpec.
type peeringSpecs struct {
	VPC      map[string]*vpcapi.VPCPeeringSpec
	External map[string]*vpcapi.ExternalPeeringSpec
	Gateway  map[string]*gwapi.PeeringSpec
}

// emptyPeeringSpecs returns an initialized peeringSpecs that BuildSpec
// callbacks can populate without manually constructing each map.
func emptyPeeringSpecs() peeringSpecs {
	return peeringSpecs{
		VPC:      make(map[string]*vpcapi.VPCPeeringSpec),
		External: make(map[string]*vpcapi.ExternalPeeringSpec),
		Gateway:  make(map[string]*gwapi.PeeringSpec),
	}
}

// natTestSpec describes a VPC-to-VPC NAT test that follows the standard
// driver shape: pick the first two VPCs (sorted alphabetically), build
// peerings, refresh the matrix, optionally overlay NAT info, then run the
// matrix-driven connectivity test. Tests that need a server move (overlap)
// or external CRD annotation lookup hand-roll instead.
type natTestSpec struct {
	Name      string
	BuildSpec func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error)
	Overlay   func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error
}

// runNATTest executes the standard NAT-test sequence defined by spec.
// Returns (skip=true) when fewer than two VPCs are available so the suite
// can mark the case as skipped.
func (testCtx *VPCPeeringTestCtx) runNATTest(ctx context.Context, matrix *ConnectivityMatrix, spec natTestSpec) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for %s test", spec.Name) //nolint:goerr113
	}
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})
	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	specs, err := spec.BuildSpec(vpc1, vpc2)
	if err != nil {
		return false, nil, fmt.Errorf("%s: building peering spec: %w", spec.Name, err)
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, specs.VPC, specs.External, specs.Gateway, true); err != nil {
		return false, nil, fmt.Errorf("%s: setting up peerings: %w", spec.Name, err)
	}
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("%s: waiting for switches: %w", spec.Name, err)
	}
	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("%s: refreshing matrix: %w", spec.Name, err)
	}

	if spec.Overlay != nil {
		if err := spec.Overlay(vpc1, vpc2, matrix); err != nil {
			return false, nil, fmt.Errorf("%s: applying NAT overlay: %w", spec.Name, err)
		}
	}

	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("%s: testing connectivity: %w", spec.Name, err)
	}

	return false, nil, nil
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

// overlayVPCToVPCStaticDNAT annotates every (server-in-srcVPCName →
// server-in-dstVPCName) matrix entry with the destination's static-NAT
// pool IP (computed from dst.Server.IP, dstSubnetCIDR, and dstNATPoolCIDR).
// The matrix-driven runner then targets the NAT IP instead of the real
// one for both ping and iperf3, matching what the gateway expects to see.
func overlayVPCToVPCStaticDNAT(matrix *ConnectivityMatrix, srcVPCName, dstVPCName, dstSubnetCIDR, dstNATPoolCIDR string) error {
	subnetPrefix, err := netip.ParsePrefix(dstSubnetCIDR)
	if err != nil {
		return fmt.Errorf("parsing dst subnet %s: %w", dstSubnetCIDR, err)
	}
	poolPrefix, err := netip.ParsePrefix(dstNATPoolCIDR)
	if err != nil {
		return fmt.Errorf("parsing dst NAT pool %s: %w", dstNATPoolCIDR, err)
	}
	subnetStart := subnetPrefix.Masked().Addr()
	poolStart := poolPrefix.Masked().Addr()

	return OverlayMatrixNAT(matrix, ServerInVPC(srcVPCName), ServerInVPC(dstVPCName), func(_, dst *Endpoint, nat *TranslatedAddress) error {
		if !dst.Server.IP.IsValid() {
			return fmt.Errorf("matrix endpoint for %s has no IP", dst.Server.Name) //nolint:goerr113
		}
		natIP, err := calculateStaticNATIP(dst.Server.IP, subnetStart, poolStart)
		if err != nil {
			return fmt.Errorf("calculating NAT IP for %s: %w", dst.Server.Name, err)
		}
		nat.DestinationIP = natIP

		return nil
	})
}

// overlayVPCToVPCPortForwardDNAT marks every (server-in-srcVPCName →
// server-in-dstVPCName) entry with a port-forward DNAT to the destination's
// NAT IP on destPort. The matrix tester then runs iperf3 against
// (NAT IP, destPort) without ping for those pairs.
func overlayVPCToVPCPortForwardDNAT(matrix *ConnectivityMatrix, srcVPCName, dstVPCName, dstSubnetCIDR, dstNATPoolCIDR string, destPort uint16) error {
	if destPort == 0 {
		return fmt.Errorf("destPort must be non-zero for port-forward overlay") //nolint:goerr113
	}
	subnetPrefix, err := netip.ParsePrefix(dstSubnetCIDR)
	if err != nil {
		return fmt.Errorf("parsing dst subnet %s: %w", dstSubnetCIDR, err)
	}
	poolPrefix, err := netip.ParsePrefix(dstNATPoolCIDR)
	if err != nil {
		return fmt.Errorf("parsing dst NAT pool %s: %w", dstNATPoolCIDR, err)
	}
	subnetStart := subnetPrefix.Masked().Addr()
	poolStart := poolPrefix.Masked().Addr()

	return OverlayMatrixNAT(matrix, ServerInVPC(srcVPCName), ServerInVPC(dstVPCName), func(_, dst *Endpoint, nat *TranslatedAddress) error {
		if !dst.Server.IP.IsValid() {
			return fmt.Errorf("matrix endpoint for %s has no IP", dst.Server.Name) //nolint:goerr113
		}
		natIP, err := calculateStaticNATIP(dst.Server.IP, subnetStart, poolStart)
		if err != nil {
			return fmt.Errorf("calculating NAT IP for %s: %w", dst.Server.Name, err)
		}
		nat.DestinationIP = natIP
		nat.DestinationPort = destPort

		return nil
	})
}

// overrideVPCToVPCVerdict forces every (server-in-srcVPCName →
// server-in-dstVPCName) entry to the given verdict.
// Used to mark direction-asymmetric paths.
func overrideVPCToVPCVerdict(matrix *ConnectivityMatrix, srcVPCName, dstVPCName string, verdict ConnectivityVerdict) {
	srcPred := ServerInVPC(srcVPCName)
	dstPred := ServerInVPC(dstVPCName)
	for _, src := range matrix.AllEndpoints {
		if !srcPred(src) {
			continue
		}
		for _, dst := range matrix.AllEndpoints {
			if !dstPred(dst) {
				continue
			}
			existing := matrix.Lookup(src, dst, ProtoPort{})
			matrix.Add(ConnectivityExpectation{
				Pair:    EndpointPair{Source: src, Destination: dst},
				Verdict: verdict,
				Reason:  ReachabilityReasonGatewayPeering,
				Peering: existing.Peering,
				NAT:     existing.NAT,
			})
		}
	}
}

// rebindMatrixServerEndpoint updates the matrix endpoint for serverName to
// reflect a new (VPC, Subnet) attachment and re-discovers its IP over SSH.
// Used by the overlap NAT test where one server is moved to a freshly
// created overlap VPC after the matrix's endpoints were captured at suite
// setup; without rebinding, VPC-keyed overlays wouldn't match the moved
// server.
func (testCtx *VPCPeeringTestCtx) rebindMatrixServerEndpoint(ctx context.Context, matrix *ConnectivityMatrix, serverName, newVPC, newSubnet string) error {
	var endpoint *Endpoint
	for _, ep := range matrix.AllEndpoints {
		if ep.Server != nil && ep.Server.Name == serverName {
			endpoint = ep

			break
		}
	}
	if endpoint == nil {
		return fmt.Errorf("server %s not found in matrix", serverName) //nolint:goerr113
	}

	sshCfg, err := testCtx.getSSH(ctx, serverName)
	if err != nil {
		return fmt.Errorf("getting ssh config for %s: %w", serverName, err)
	}
	ip, err := discoverServerIP(ctx, sshCfg, serverName)
	if err != nil {
		return fmt.Errorf("rediscovering IP after rebind: %w", err)
	}

	endpoint.Server.VPC = newVPC
	endpoint.Server.Subnet = newSubnet
	endpoint.Server.IP = ip

	return nil
}

// vpcFirstSubnetCIDR returns the (only) subnet CIDR for a VPC used in the
// NAT tests. The NAT overlays require exactly one subnet per VPC because
// the static-NAT offset algorithm is unambiguous only then.
func vpcFirstSubnetCIDR(vpc *vpcapi.VPC) (string, error) {
	if len(vpc.Spec.Subnets) != 1 {
		return "", fmt.Errorf("VPC %s has %d subnets, NAT test requires exactly one", vpc.Name, len(vpc.Spec.Subnets)) //nolint:goerr113
	}
	for _, subnet := range vpc.Spec.Subnets {
		return subnet.Subnet, nil
	}

	return "", fmt.Errorf("VPC %s has empty subnet map", vpc.Name) //nolint:goerr113
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
func gatewayPeeringMasqueradeSourceNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway masquerade source NAT",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR: []string{"192.168.11.0/24"},
				VPC1NATMode: NATModeMasquerade,
			})

			return specs, err
		},
		// vpc1→vpc2 works via masquerade SNAT (vpc2 sees real IPs as dst,
		// no DNAT overlay needed — we just assert Allow). vpc2→vpc1 is
		// blocked: masquerade is stateful and doesn't accept unsolicited
		// inbound traffic on the NAT pool.
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			overrideVPCToVPCVerdict(matrix, vpc1.Name, vpc2.Name, VerdictAllow)
			overrideVPCToVPCVerdict(matrix, vpc2.Name, vpc1.Name, VerdictDeny)

			return nil
		},
	})
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
func gatewayPeeringStaticSourceNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	vpc1NATCIDR := "192.168.21.0/24"

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway static source NAT",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR: []string{vpc1NATCIDR},
			})

			return specs, err
		},
		// vpc1→vpc2 uses vpc2's real IPs (no NAT on vpc2) — populate
		// can't see this Allow because the peering carries 'As', so we
		// assert it explicitly. vpc2→vpc1 must target vpc1's NAT pool
		// addresses: static NAT is bidirectional and the gateway only
		// knows vpc1 by its NAT IPs from vpc2's side.
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			overrideVPCToVPCVerdict(matrix, vpc1.Name, vpc2.Name, VerdictAllow)
			vpc1SubnetCIDR, err := vpcFirstSubnetCIDR(vpc1)
			if err != nil {
				return err
			}

			return overlayVPCToVPCStaticDNAT(matrix, vpc2.Name, vpc1.Name, vpc1SubnetCIDR, vpc1NATCIDR)
		},
	})
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
func gatewayPeeringBidirectionalStaticNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	vpc1NATCIDR := "192.168.31.0/24"
	vpc2NATCIDR := "192.168.32.0/24"

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway bidirectional static NAT",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR: []string{vpc1NATCIDR},
				VPC2NATCIDR: []string{vpc2NATCIDR},
			})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			vpc1SubnetCIDR, err := vpcFirstSubnetCIDR(vpc1)
			if err != nil {
				return err
			}
			vpc2SubnetCIDR, err := vpcFirstSubnetCIDR(vpc2)
			if err != nil {
				return err
			}
			if err := overlayVPCToVPCStaticDNAT(matrix, vpc2.Name, vpc1.Name, vpc1SubnetCIDR, vpc1NATCIDR); err != nil {
				return fmt.Errorf("overlaying vpc1 static DNAT: %w", err)
			}

			return overlayVPCToVPCStaticDNAT(matrix, vpc1.Name, vpc2.Name, vpc2SubnetCIDR, vpc2NATCIDR)
		},
	})
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
func gatewayPeeringOverlapNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
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

	// Preserve the fabric-advertised MTU across the overlap VPC reattach so the
	// server doesn't fall back to the DHCP default and cap iperf throughput.
	preservedMTU := testCtx.setupOpts.InterfaceMTU

	newVLAN := originalVLAN + 100 // Use different VLAN to avoid conflicts
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

	// Capture the moved server's pre-overlap matrix endpoint state so the
	// revert below can restore it. Subsequent tests in the same suite reuse
	// the matrix and would otherwise see vpc-overlap metadata stuck on a
	// server that is, by then, back in donorVPC.
	var origMatrixVPC, origMatrixSubnet string
	var origMatrixIP netip.Addr
	for _, ep := range matrix.AllEndpoints {
		if ep.Server != nil && ep.Server.Name == targetServer {
			origMatrixVPC = ep.Server.VPC
			origMatrixSubnet = ep.Server.Subnet
			origMatrixIP = ep.Server.IP

			break
		}
	}

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
			MTU:        preservedMTU,
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

		// Restore the matrix endpoint to its pre-overlap state so the next
		// test in the suite sees the server back in donorVPC with its
		// original IP. The actual server IP will be re-issued by DHCP on
		// the original VLAN; the cached IP captured at suite setup is the
		// authoritative source for matrix-driven tests.
		for _, ep := range matrix.AllEndpoints {
			if ep.Server != nil && ep.Server.Name == targetServer {
				ep.Server.VPC = origMatrixVPC
				ep.Server.Subnet = origMatrixSubnet
				ep.Server.IP = origMatrixIP

				break
			}
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
		MTU:        preservedMTU,
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

	if err := appendGwPeeringSpec(gwPeerings, existingVPC, overlapVPC, &GwPeeringOptions{
		VPC1NATCIDR: existingVPCNATCIDR,
		VPC2NATCIDR: overlapVPCNATCIDR,
	}); err != nil {
		return false, reverts, fmt.Errorf("setting up gateway peering: %w", err)
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, reverts, fmt.Errorf("setting up overlap NAT gateway peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for switches to be ready after peering: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, reverts, fmt.Errorf("refreshing matrix after overlap peerings: %w", err)
	}

	// Test connectivity - both VPCs have overlapping subnets, NAT resolves them
	slog.Info("Testing connectivity between VPCs with overlapping subnets via NAT",
		"existingVPC", existingVPC.Name, "existingCIDR", existingSubnetCIDR,
		"overlapVPC", overlapVPC.Name, "overlapCIDR", existingSubnetCIDR,
		"existingNAT", existingVPCNATCIDR, "overlapNAT", overlapVPCNATCIDR)

	// Tell the matrix that the moved server now lives in the overlap VPC
	// and pick up its DHCP-assigned IP from the new subnet.
	if err := testCtx.rebindMatrixServerEndpoint(ctx, matrix, targetServer, overlapVPCName, overlapSubnet); err != nil {
		return false, reverts, fmt.Errorf("rebinding moved server endpoint to overlap VPC: %w", err)
	}

	if err := overlayVPCToVPCStaticDNAT(matrix, overlapVPCName, existingVPC.Name, existingSubnetCIDR, existingVPCNATCIDR[0]); err != nil {
		return false, reverts, fmt.Errorf("overlaying existing VPC DNAT: %w", err)
	}
	if err := overlayVPCToVPCStaticDNAT(matrix, existingVPC.Name, overlapVPCName, existingSubnetCIDR, overlapVPCNATCIDR[0]); err != nil {
		return false, reverts, fmt.Errorf("overlaying overlap VPC DNAT: %w", err)
	}

	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, reverts, fmt.Errorf("testing overlap NAT connectivity: %w", err)
	}

	slog.Info("Overlap NAT test completed successfully")

	return false, reverts, nil
}

// Test gateway peering with port-forwarding NAT only
// Port-forward NAT is INBOUND: vpc2 connects to vpc1's NAT IP:15201, gateway forwards to vpc1's real IP:5201.
// vpc1 cannot initiate connections to vpc2 (port-forward does not enable outbound NAT).
//
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.52.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Port Forward:
//	          Rules:
//	          - Protocol: TCP
//	            Port: 5201
//	            As: 15201
//	  vpc-02:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.2.0/24
func gatewayPeeringPortForwardNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	const vpc1NATCIDR = "192.168.52.0/24"
	portForwardRules := []gwapi.PeeringNATPortForwardEntry{
		{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"},
	}

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway port-forward NAT",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR:          []string{vpc1NATCIDR},
				VPC1NATMode:          NATModePortForward,
				VPC1PortForwardRules: portForwardRules,
			})

			return specs, err
		},
		// Port-forward is INBOUND only: vpc2 connects to vpc1's NAT IP on
		// the forwarded external port. vpc1 cannot initiate connections
		// to vpc2 (no outbound NAT), so we force that direction to Deny.
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			overrideVPCToVPCVerdict(matrix, vpc1.Name, vpc2.Name, VerdictDeny)
			vpc1SubnetCIDR, err := vpcFirstSubnetCIDR(vpc1)
			if err != nil {
				return err
			}

			return overlayVPCToVPCPortForwardDNAT(matrix, vpc2.Name, vpc1.Name, vpc1SubnetCIDR, vpc1NATCIDR, 15201)
		},
	})
}

// Test gateway peering with combined masquerade and port-forwarding NAT
// Masquerade enables outbound NAT from vpc1 to vpc2; port-forward enables inbound on port 15201→5201.
// Both directions are tested: outbound via masquerade (vpc1→vpc2) and inbound via port-forward (vpc2→vpc1).
//
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.51.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Masquerade:
//	          Idle Timeout:  5m0s
//	        Port Forward:
//	          Rules:
//	          - Protocol: TCP
//	            Port: 5201
//	            As: 15201
//	  vpc-02:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.2.0/24
func gatewayPeeringMasqueradePortForwardNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	const vpc1NATCIDR = "192.168.51.0/24"
	portForwardRules := []gwapi.PeeringNATPortForwardEntry{
		{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"},
	}

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway masquerade+port-forward NAT",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR:          []string{vpc1NATCIDR},
				VPC1NATMode:          NATModeMasqueradePortForward,
				VPC1PortForwardRules: portForwardRules,
			})

			return specs, err
		},
		// vpc1→vpc2 rides masquerade SNAT against vpc2's real IPs;
		// populate can't see this Allow because the peering carries 'As',
		// so we assert it explicitly. vpc2→vpc1 must hit the port-forward
		// virtual (NAT IP, 15201); the matrix runner treats DNAT+port as
		// L4-only and skips ping for that direction.
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			overrideVPCToVPCVerdict(matrix, vpc1.Name, vpc2.Name, VerdictAllow)
			vpc1SubnetCIDR, err := vpcFirstSubnetCIDR(vpc1)
			if err != nil {
				return err
			}

			return overlayVPCToVPCPortForwardDNAT(matrix, vpc2.Name, vpc1.Name, vpc1SubnetCIDR, vpc1NATCIDR, 15201)
		},
	})
}

// getNATTestCases returns the NAT test cases to be added to the multi-VPC single-subnet suite
func getNATTestCases() []JUnitTestCase {
	return []JUnitTestCase{
		{
			Name: "Gateway Peering Masquerade Source NAT",
			F:    gatewayPeeringMasqueradeSourceNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Static Source NAT",
			F:    gatewayPeeringStaticSourceNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Bidirectional Static NAT",
			F:    gatewayPeeringBidirectionalStaticNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Overlap NAT",
			F:    gatewayPeeringOverlapNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Port Forward NAT",
			F:    gatewayPeeringPortForwardNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Masquerade and Port Forward NAT",
			F:    gatewayPeeringMasqueradePortForwardNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
	}
}
