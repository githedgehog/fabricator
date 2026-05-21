// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
)

// overlayExternalSNAT marks every (server-in-vpcName → extName) Allow entry
// in the matrix with the given SNAT pool.
func overlayExternalSNAT(matrix *ConnectivityMatrix, vpcName, extName, sourcePoolCIDR string) error {
	pool, err := netip.ParsePrefix(sourcePoolCIDR)
	if err != nil {
		return fmt.Errorf("parsing SNAT pool %s: %w", sourcePoolCIDR, err)
	}

	return OverlayMatrixNAT(matrix, ServerInVPC(vpcName), ExternalNamed(extName), func(_, _ *Endpoint, nat *TranslatedAddress) error {
		nat.SourcePool = pool

		return nil
	})
}

// pingExternalStability runs a 10-ping probe from every server in vpcName to
// the external's BGP-neighbor IP (the actual remote device, not 1.0.0.1).
func (testCtx *VPCPeeringTestCtx) pingExternalStability(ctx context.Context, matrix *ConnectivityMatrix, vpcName, extName string) error {
	remoteIP, err := getExternalRemoteIP(ctx, testCtx.kube, extName)
	if err != nil {
		return fmt.Errorf("getting external remote IP for ping: %w", err)
	}
	if remoteIP == "" {
		return nil
	}
	remoteAddr, err := netip.ParseAddr(remoteIP)
	if err != nil {
		return fmt.Errorf("parsing external remote IP %s: %w", remoteIP, err)
	}

	seen := map[string]bool{}
	var tested int
	for _, ep := range matrix.AllEndpoints {
		if ep.Server == nil || ep.Server.VPC != vpcName {
			continue
		}
		if seen[ep.Server.Name] {
			continue
		}
		seen[ep.Server.Name] = true

		sshCfg, err := testCtx.getSSH(ctx, ep.Server.Name)
		if err != nil {
			return fmt.Errorf("getting ssh config for %s: %w", ep.Server.Name, err)
		}
		slog.Debug("Testing NAT external connectivity stability via ping", "server", ep.Server.Name, "target", remoteIP)
		if pingErr := checkPing(ctx, 10, nil, ep.Server.Name, remoteIP, sshCfg, remoteAddr, nil, true); pingErr != nil {
			return fmt.Errorf("NAT external connectivity ping stability check: %w", pingErr)
		}
		tested++
	}

	if tested == 0 {
		return fmt.Errorf("no servers found in VPC %s for ping stability check", vpcName) //nolint:goerr113
	}

	return nil
}

// overlayExternalPortForward marks every (server-in-vpcName → extName) Allow
// entry with a DNAT to destIP:destPort, telling the matrix-driven tester to
// exercise iperf3 against that virtual endpoint. Without any SNAT companion,
// the curl-to-external check correctly expects failure: the peering routes
// only the port-forward target, not arbitrary outbound.
func overlayExternalPortForward(matrix *ConnectivityMatrix, vpcName, extName string, destIP netip.Addr, destPort uint16) error {
	if destPort == 0 {
		return fmt.Errorf("destPort must be non-zero for port-forward overlay") //nolint:goerr113
	}
	if !destIP.IsValid() {
		return fmt.Errorf("destIP must be valid for port-forward overlay") //nolint:goerr113
	}

	return OverlayMatrixNAT(matrix, ServerInVPC(vpcName), ExternalNamed(extName), func(_, _ *Endpoint, nat *TranslatedAddress) error {
		nat.DestinationIP = destIP
		nat.DestinationPort = destPort

		return nil
	})
}

// Test gateway external peering with no NAT (baseline)
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func bgpExternalNoNatTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.extName == "" {
		return true, nil, fmt.Errorf("no BGP external available for testing") //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for external NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	appendGwExtPeeringSpec(gwPeerings, vpc, nil, testCtx.extName)

	slog.Info("Testing BGP external peering with no NAT", "vpc", vpc.Name, "external", testCtx.extName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up BGP external peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing BGP external connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway external peering with static NAT
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.81.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Static:
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func bgpExternalStaticNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.extName == "" {
		return true, nil, fmt.Errorf("no BGP external available for testing") //nolint:goerr113
	}

	bgpNATCIDR, err := getExternalBGPNATCIDR(ctx, testCtx.kube, testCtx.extName)
	if err != nil {
		return false, nil, fmt.Errorf("getting BGP NAT CIDR for external %s: %w", testCtx.extName, err)
	}
	if bgpNATCIDR == "" {
		return true, nil, fmt.Errorf("no BGP NAT pool annotation (%s) on external %s", extBGPNATAnnotation, testCtx.extName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for external static NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	if err := appendGwExtPeeringSpecWithNAT(gwPeerings, vpc, testCtx.extName, &GwExtPeeringOptions{
		VPCNATCIDR: []string{bgpNATCIDR},
		VPCNATMode: NATModeStatic,
	}); err != nil {
		return false, nil, fmt.Errorf("setting up gateway external peering: %w", err)
	}

	slog.Info("Testing BGP external peering with static NAT", "vpc", vpc.Name, "external", testCtx.extName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up BGP external static NAT peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, bgpNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	if err := overlayExternalSNAT(matrix, vpc.Name, testCtx.extName, bgpNATCIDR); err != nil {
		return false, nil, fmt.Errorf("annotating matrix with BGP static SNAT pool: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing BGP external static NAT connectivity: %w", err)
	}
	if err := testCtx.pingExternalStability(ctx, matrix, vpc.Name, testCtx.extName); err != nil {
		return false, nil, fmt.Errorf("BGP external static NAT ping stability: %w", err)
	}

	return false, nil, nil
}

// Test gateway external peering with masquerade NAT
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.81.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Masquerade:
//	          Idle Timeout:  5m0s
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func bgpExternalMasqueradeNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.extName == "" {
		return true, nil, fmt.Errorf("no BGP external available for testing") //nolint:goerr113
	}

	bgpNATCIDR, err := getExternalBGPNATCIDR(ctx, testCtx.kube, testCtx.extName)
	if err != nil {
		return false, nil, fmt.Errorf("getting BGP NAT CIDR for external %s: %w", testCtx.extName, err)
	}
	if bgpNATCIDR == "" {
		return true, nil, fmt.Errorf("no BGP NAT pool annotation (%s) on external %s", extBGPNATAnnotation, testCtx.extName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for external masquerade NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	if err := appendGwExtPeeringSpecWithNAT(gwPeerings, vpc, testCtx.extName, &GwExtPeeringOptions{
		VPCNATCIDR: []string{bgpNATCIDR},
		VPCNATMode: NATModeMasquerade,
	}); err != nil {
		return false, nil, fmt.Errorf("setting up gateway external peering: %w", err)
	}

	slog.Info("Testing BGP external peering with masquerade NAT", "vpc", vpc.Name, "external", testCtx.extName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up BGP external masquerade NAT peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, bgpNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	if err := overlayExternalSNAT(matrix, vpc.Name, testCtx.extName, bgpNATCIDR); err != nil {
		return false, nil, fmt.Errorf("annotating matrix with BGP masquerade SNAT pool: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing BGP external masquerade NAT connectivity: %w", err)
	}
	if err := testCtx.pingExternalStability(ctx, matrix, vpc.Name, testCtx.extName); err != nil {
		return false, nil, fmt.Errorf("BGP external masquerade NAT ping stability: %w", err)
	}

	return false, nil, nil
}

// Test gateway external peering with port-forward NAT (inverted: VPC→external).
// The peering is set up with port-forward NAT on the external side, so the VPC can initiate
// connections to the external device's iperf3 server through the gateway. The external's BGP
// neighbor IP is NAT'd to .200/32 within the BGP NAT pool CIDR (from the extBGPNATAnnotation),
// with port-forward 5201→15201. VPC connects to that IP:15201, gateway forwards to external:5201.
//
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.1.0/24  (no NAT — VPC IPs visible)
//	  ext-<name>:
//	    Expose:
//	      As:
//	        Cidr:  <bgpNATCIDR .200>/32
//	      Ips:
//	        Cidr:  <ext neighbor IP>/32
//	      Nat:
//	        Port Forward:
//	          Rules:
//	          - Protocol: TCP
//	            Port: 5201
//	            As: 15201
func bgpExternalPortForwardNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.extName == "" {
		return true, nil, fmt.Errorf("no BGP external available for testing") //nolint:goerr113
	}

	bgpNATCIDR, err := getExternalBGPNATCIDR(ctx, testCtx.kube, testCtx.extName)
	if err != nil {
		return false, nil, fmt.Errorf("getting BGP NAT CIDR for external %s: %w", testCtx.extName, err)
	}
	if bgpNATCIDR == "" {
		return true, nil, fmt.Errorf("no BGP NAT pool annotation (%s) on external %s", extBGPNATAnnotation, testCtx.extName) //nolint:goerr113
	}

	prefix, err := netip.ParsePrefix(bgpNATCIDR)
	if err != nil {
		return false, nil, fmt.Errorf("parsing BGP NAT CIDR %s: %w", bgpNATCIDR, err)
	}
	if prefix.Bits() != 24 {
		return false, nil, fmt.Errorf("BGP NAT CIDR %s must be a /24 for the .200 inverted NAT address to be valid", bgpNATCIDR) //nolint:goerr113
	}
	// .200 within the NAT pool is the virtual IP representing the external device; traffic to it
	// is port-forwarded to the real neighbor IP. Chosen to avoid VPC server IPs (low end of
	// subnet) while staying within the /24 covered by the BGP advertisement.
	b := prefix.Masked().Addr().As4()
	b[3] = 200
	bgpInvertedNATCIDR := netip.AddrFrom4(b).String() + "/32"

	extRemoteIP, err := getExternalRemoteIP(ctx, testCtx.kube, testCtx.extName)
	if err != nil {
		return false, nil, fmt.Errorf("getting remote IP for external %s: %w", testCtx.extName, err)
	}
	if extRemoteIP == "" {
		return true, nil, fmt.Errorf("no remote IP found in ExternalAttachment for %s", testCtx.extName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for BGP external port-forward NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	portForwardRules := []gwapi.PeeringNATPortForwardEntry{
		{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"},
	}
	appendGwExtInvertedPortForwardPeeringSpec(gwPeerings, vpc, testCtx.extName, extRemoteIP, bgpInvertedNATCIDR, portForwardRules)

	slog.Info("Testing BGP external port-forward (inverted: VPC→ext)", "vpc", vpc.Name, "external", testCtx.extName, "extIP", extRemoteIP, "natCIDR", bgpInvertedNATCIDR)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up BGP external inverted port-forward peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, bgpNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	if err := overlayExternalPortForward(matrix, vpc.Name, testCtx.extName, netip.AddrFrom4(b), 15201); err != nil {
		return false, nil, fmt.Errorf("overlaying BGP external port-forward DNAT: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing BGP external port-forward via iperf3: %w", err)
	}

	return false, nil, nil
}

// Test gateway external peering with combined masquerade and port-forward NAT
// Masquerade enables outbound NAT from vpc to external; port-forward enables inbound on 15201→5201.
// The outbound direction (vpc→external via masquerade) is tested with curl.
// The inbound port-forward from external is not tested (no SSH access to external).
//
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.81.0/24
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
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func bgpExternalMasqueradePortForwardNATTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.extName == "" {
		return true, nil, fmt.Errorf("no BGP external available for testing") //nolint:goerr113
	}

	bgpNATCIDR, err := getExternalBGPNATCIDR(ctx, testCtx.kube, testCtx.extName)
	if err != nil {
		return false, nil, fmt.Errorf("getting BGP NAT CIDR for external %s: %w", testCtx.extName, err)
	}
	if bgpNATCIDR == "" {
		return true, nil, fmt.Errorf("no BGP NAT pool annotation (%s) on external %s", extBGPNATAnnotation, testCtx.extName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for external masquerade+port-forward NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	portForwardRules := []gwapi.PeeringNATPortForwardEntry{
		{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"},
	}
	if err := appendGwExtPeeringSpecWithNAT(gwPeerings, vpc, testCtx.extName, &GwExtPeeringOptions{
		VPCNATCIDR:          []string{bgpNATCIDR},
		VPCNATMode:          NATModeMasqueradePortForward,
		VPCPortForwardRules: portForwardRules,
	}); err != nil {
		return false, nil, fmt.Errorf("setting up gateway external peering: %w", err)
	}

	slog.Info("Testing BGP external peering with masquerade+port-forward NAT", "vpc", vpc.Name, "external", testCtx.extName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up BGP external masquerade+port-forward NAT peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, bgpNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	// Only the outbound (VPC→ext via masquerade) direction is exercised
	// here. The inbound port-forward (ext→VPC on 15201→5201) requires SSH
	// to the external device, which the matrix doesn't model.
	if err := overlayExternalSNAT(matrix, vpc.Name, testCtx.extName, bgpNATCIDR); err != nil {
		return false, nil, fmt.Errorf("annotating matrix with BGP masquerade SNAT pool: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing BGP external masquerade+port-forward NAT connectivity: %w", err)
	}
	if err := testCtx.pingExternalStability(ctx, matrix, vpc.Name, testCtx.extName); err != nil {
		return false, nil, fmt.Errorf("BGP external masquerade+port-forward NAT ping stability: %w", err)
	}

	return false, nil, nil
}

// Test gateway static external peering with no NAT
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func staticExternalNoNATGatewayTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.staticExtName == "" {
		return true, nil, fmt.Errorf("no static external available for testing") //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for static external no NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	appendGwExtPeeringSpec(gwPeerings, vpc, nil, testCtx.staticExtName)

	slog.Info("Testing static external peering with no NAT", "vpc", vpc.Name, "external", testCtx.staticExtName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up static external peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing static external connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway static external peering with static NAT
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.81.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Static:
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func staticExternalStaticNATGatewayTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.staticExtName == "" {
		return true, nil, fmt.Errorf("no static external available for testing") //nolint:goerr113
	}

	staticNATCIDR, err := getExternalStaticNATCIDR(ctx, testCtx.kube, testCtx.staticExtName)
	if err != nil {
		return false, nil, fmt.Errorf("getting static NAT CIDR for external %s: %w", testCtx.staticExtName, err)
	}
	if staticNATCIDR == "" {
		return true, nil, fmt.Errorf("no static NAT pool annotation (%s) on external %s", extStaticNATPoolAnnotation, testCtx.staticExtName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for static external static NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	if err := appendGwExtPeeringSpecWithNAT(gwPeerings, vpc, testCtx.staticExtName, &GwExtPeeringOptions{
		VPCNATCIDR: []string{staticNATCIDR},
		VPCNATMode: NATModeStatic,
	}); err != nil {
		return false, nil, fmt.Errorf("setting up gateway external peering: %w", err)
	}

	slog.Info("Testing static external peering with static NAT", "vpc", vpc.Name, "external", testCtx.staticExtName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up static external static NAT peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, staticNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	if err := overlayExternalSNAT(matrix, vpc.Name, testCtx.staticExtName, staticNATCIDR); err != nil {
		return false, nil, fmt.Errorf("annotating matrix with static SNAT pool: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing static external static NAT connectivity: %w", err)
	}
	if err := testCtx.pingExternalStability(ctx, matrix, vpc.Name, testCtx.staticExtName); err != nil {
		return false, nil, fmt.Errorf("static external static NAT ping stability: %w", err)
	}

	return false, nil, nil
}

// Test gateway static external peering with masquerade NAT
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.81.0/24
//	      Ips:
//	        Cidr:  10.50.1.0/24
//	      Nat:
//	        Masquerade:
//	          Idle Timeout:  5m0s
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func staticExternalMasqueradeNATGatewayTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.staticExtName == "" {
		return true, nil, fmt.Errorf("no static external available for testing") //nolint:goerr113
	}

	staticNATCIDR, err := getExternalStaticNATCIDR(ctx, testCtx.kube, testCtx.staticExtName)
	if err != nil {
		return false, nil, fmt.Errorf("getting static NAT CIDR for external %s: %w", testCtx.staticExtName, err)
	}
	if staticNATCIDR == "" {
		return true, nil, fmt.Errorf("no static NAT pool annotation (%s) on external %s", extStaticNATPoolAnnotation, testCtx.staticExtName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for static external masquerade NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	if err := appendGwExtPeeringSpecWithNAT(gwPeerings, vpc, testCtx.staticExtName, &GwExtPeeringOptions{
		VPCNATCIDR: []string{staticNATCIDR},
		VPCNATMode: NATModeMasquerade,
	}); err != nil {
		return false, nil, fmt.Errorf("setting up gateway external peering: %w", err)
	}

	slog.Info("Testing static external peering with masquerade NAT", "vpc", vpc.Name, "external", testCtx.staticExtName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up static external masquerade NAT peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, staticNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	if err := overlayExternalSNAT(matrix, vpc.Name, testCtx.staticExtName, staticNATCIDR); err != nil {
		return false, nil, fmt.Errorf("annotating matrix with static masquerade SNAT pool: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing static external masquerade NAT connectivity: %w", err)
	}
	if err := testCtx.pingExternalStability(ctx, matrix, vpc.Name, testCtx.staticExtName); err != nil {
		return false, nil, fmt.Errorf("static external masquerade NAT ping stability: %w", err)
	}

	return false, nil, nil
}

// Test gateway static external peering with port-forward NAT (inverted: VPC→external).
// The peering is set up with port-forward NAT on the external side, so VPC can initiate
// connections to the external device's iperf3 server through the gateway.
//
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      Ips:
//	        Cidr:  10.50.1.0/24   (no NAT — VPC IPs visible; DS2000 has static return routes)
//	  ext-<name>:
//	    Expose:
//	      As:
//	        Cidr:  192.168.81.200/32
//	      Ips:
//	        Cidr:  <DS2000 remoteIP>/32
//	      Nat:
//	        Port Forward:
//	          Rules:
//	          - Protocol: TCP
//	            Port: 5201
//	            As: 15201
func staticExternalPortForwardNATGatewayTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.staticExtName == "" {
		return true, nil, fmt.Errorf("no static external available for testing") //nolint:goerr113
	}

	staticNATCIDR, err := getExternalStaticNATCIDR(ctx, testCtx.kube, testCtx.staticExtName)
	if err != nil {
		return false, nil, fmt.Errorf("getting static NAT CIDR for external %s: %w", testCtx.staticExtName, err)
	}
	if staticNATCIDR == "" {
		return true, nil, fmt.Errorf("no static NAT pool annotation (%s) on external %s", extStaticNATPoolAnnotation, testCtx.staticExtName) //nolint:goerr113
	}

	prefix, err := netip.ParsePrefix(staticNATCIDR)
	if err != nil {
		return false, nil, fmt.Errorf("parsing static NAT CIDR %s: %w", staticNATCIDR, err)
	}
	if prefix.Bits() != 24 {
		return false, nil, fmt.Errorf("static NAT CIDR %s must be a /24 for the .200 inverted NAT address to be valid", staticNATCIDR) //nolint:goerr113
	}
	// .200 within the NAT pool is the virtual IP representing the external device; traffic to it
	// is port-forwarded to the real remote IP. DS2000 has a static route for the whole /24 so
	// any address in the pool is reachable. Chosen to avoid VPC server IPs (low end of subnet).
	b := prefix.Masked().Addr().As4()
	b[3] = 200
	staticInvertedNATCIDR := netip.AddrFrom4(b).String() + "/32"

	extRemoteIP, err := getExternalRemoteIP(ctx, testCtx.kube, testCtx.staticExtName)
	if err != nil {
		return false, nil, fmt.Errorf("getting remote IP for external %s: %w", testCtx.staticExtName, err)
	}
	if extRemoteIP == "" {
		return true, nil, fmt.Errorf("no remote IP found in ExternalAttachment for %s", testCtx.staticExtName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for static external port-forward test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	portForwardRules := []gwapi.PeeringNATPortForwardEntry{
		{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"},
	}
	appendGwExtInvertedPortForwardPeeringSpec(gwPeerings, vpc, testCtx.staticExtName, extRemoteIP, staticInvertedNATCIDR, portForwardRules)

	slog.Info("Testing static external port-forward (inverted: VPC→ext)", "vpc", vpc.Name, "external", testCtx.staticExtName, "extIP", extRemoteIP)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up inverted port-forward peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, staticNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	if err := overlayExternalPortForward(matrix, vpc.Name, testCtx.staticExtName, netip.AddrFrom4(b), 15201); err != nil {
		return false, nil, fmt.Errorf("overlaying static external port-forward DNAT: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing static external port-forward via iperf3: %w", err)
	}

	return false, nil, nil
}

// Test gateway static external peering with combined masquerade and port-forward NAT
// Masquerade enables outbound NAT from vpc to external; port-forward enables inbound on 15201→5201.
// The outbound direction (vpc→external via masquerade) is tested with curl.
// The inbound port-forward from external is not tested (no SSH access to external).
//
// Peering spec:
//
//	Gateway Group:  default
//	Peering:
//	  vpc-01:
//	    Expose:
//	      As:
//	        Cidr:  192.168.81.0/24
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
//	  ext-<name>:
//	    Expose:
//	      Ips:
//	        Cidr:  0.0.0.0/0
func staticExternalMasqueradePortForwardNATGatewayTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if testCtx.staticExtName == "" {
		return true, nil, fmt.Errorf("no static external available for testing") //nolint:goerr113
	}

	staticNATCIDR, err := getExternalStaticNATCIDR(ctx, testCtx.kube, testCtx.staticExtName)
	if err != nil {
		return false, nil, fmt.Errorf("getting static NAT CIDR for external %s: %w", testCtx.staticExtName, err)
	}
	if staticNATCIDR == "" {
		return true, nil, fmt.Errorf("no static NAT pool annotation (%s) on external %s", extStaticNATPoolAnnotation, testCtx.staticExtName) //nolint:goerr113
	}

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 1 {
		return true, nil, fmt.Errorf("no VPCs available for static external masquerade+port-forward NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	gwPeerings := make(map[string]*gwapi.PeeringSpec)

	vpc := &vpcs.Items[0]
	portForwardRules := []gwapi.PeeringNATPortForwardEntry{
		{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"},
	}
	if err := appendGwExtPeeringSpecWithNAT(gwPeerings, vpc, testCtx.staticExtName, &GwExtPeeringOptions{
		VPCNATCIDR:          []string{staticNATCIDR},
		VPCNATMode:          NATModeMasqueradePortForward,
		VPCPortForwardRules: portForwardRules,
	}); err != nil {
		return false, nil, fmt.Errorf("setting up gateway external peering: %w", err)
	}

	slog.Info("Testing static external peering with masquerade+port-forward NAT", "vpc", vpc.Name, "external", testCtx.staticExtName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up static external masquerade+port-forward NAT peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("refreshing matrix after peerings: %w", err)
	}

	if err := testCtx.waitForNATPoolInLeaves(ctx, vpc, staticNATCIDR); err != nil {
		return false, nil, fmt.Errorf("waiting for NAT pool route to propagate: %w", err)
	}

	// Only the outbound (VPC→ext via masquerade) direction is exercised
	// here. The inbound port-forward (ext→VPC on 15201→5201) requires SSH
	// to the external device, which the matrix doesn't model.
	if err := overlayExternalSNAT(matrix, vpc.Name, testCtx.staticExtName, staticNATCIDR); err != nil {
		return false, nil, fmt.Errorf("annotating matrix with static masquerade SNAT pool: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return false, nil, fmt.Errorf("testing static external masquerade+port-forward NAT connectivity: %w", err)
	}
	if err := testCtx.pingExternalStability(ctx, matrix, vpc.Name, testCtx.staticExtName); err != nil {
		return false, nil, fmt.Errorf("static external masquerade+port-forward NAT ping stability: %w", err)
	}

	return false, nil, nil
}

// getExternalNATTestCases returns the external NAT test cases
func getExternalNATTestCases() []JUnitTestCase {
	return []JUnitTestCase{
		{
			Name: "Gateway Peering BGP External No NAT",
			F:    bgpExternalNoNatTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Static NAT",
			F:    bgpExternalStaticNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Masquerade NAT",
			F:    bgpExternalMasqueradeNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Port Forward NAT",
			F:    bgpExternalPortForwardNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Masquerade and Port Forward NAT",
			F:    bgpExternalMasqueradePortForwardNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External No NAT",
			F:    staticExternalNoNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Static NAT",
			F:    staticExternalStaticNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Masquerade NAT",
			F:    staticExternalMasqueradeNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Port Forward NAT",
			F:    staticExternalPortForwardNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Masquerade and Port Forward NAT",
			F:    staticExternalMasqueradePortForwardNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
	}
}
