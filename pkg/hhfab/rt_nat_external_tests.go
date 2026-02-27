// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
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
	"golang.org/x/sync/semaphore"
)

// gwNATConvergenceTimeout is the maximum time to wait for the gateway NAT dataplane to
// become active after a peering is established. WaitReady confirms the fabric switches are
// ready, but the gateway still needs to program NAT rules (and, for BGP externals, advertise
// routes to the upstream peer). Both take additional time that varies by environment.
const gwNATConvergenceTimeout = 2 * time.Minute

// gwNATConvergenceInterval is the polling interval between retries while waiting for the
// gateway NAT dataplane to converge.
const gwNATConvergenceInterval = 5 * time.Second

// testNATExternalConnectivity tests outbound connectivity from a VPC through a NAT gateway peering
// by curling 1.0.0.1 directly, bypassing the standard peering check that does not understand NAT
// expose CIDRs. Pass waitForBGPConvergence=true for BGP externals: WaitReady only confirms the
// fabric switches are programmed, but the gateway still needs to advertise the NAT pool to DS2000
// via BGP before return traffic can flow. Static externals have pre-configured routes so no wait
// is needed.
func (testCtx *VPCPeeringTestCtx) testNATExternalConnectivity(ctx context.Context, vpc *vpcapi.VPC, waitForBGPConvergence bool) error {
	servers := &wiringapi.ServerList{}
	if err := testCtx.kube.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}

	curlSem := semaphore.NewWeighted(1)

	var tested int
	for _, server := range servers.Items {
		attachedSubnets, err := apiutil.GetAttachedSubnets(ctx, testCtx.kube, server.Name)
		if err != nil {
			continue
		}

		inVPC := false
		for subnetName := range attachedSubnets {
			if strings.HasPrefix(subnetName, vpc.Name+"/") {
				inVPC = true

				break
			}
		}
		if !inVPC {
			continue
		}

		sshCfg, err := testCtx.getSSH(ctx, server.Name)
		if err != nil {
			return fmt.Errorf("getting ssh config for %s: %w", server.Name, err)
		}

		slog.Debug("Testing NAT external connectivity via curl", "server", server.Name)
		curlErr := checkCurl(ctx, testCtx.tcOpts, curlSem, server.Name, sshCfg, "1.0.0.1", true)
		if curlErr != nil && waitForBGPConvergence {
			deadline := time.Now().Add(gwNATConvergenceTimeout)
			for curlErr != nil {
				if time.Now().After(deadline) {
					break
				}
				slog.Debug("BGP routes not yet propagated, retrying", "server", server.Name, "error", curlErr, "retryIn", gwNATConvergenceInterval)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(gwNATConvergenceInterval):
				}
				curlErr = checkCurl(ctx, testCtx.tcOpts, curlSem, server.Name, sshCfg, "1.0.0.1", true)
			}
			if curlErr != nil {
				return fmt.Errorf("NAT external connectivity check (BGP not converged after %s): %w", gwNATConvergenceTimeout, curlErr)
			}
		} else if curlErr != nil {
			return fmt.Errorf("NAT external connectivity check: %w", curlErr)
		}

		tested++
	}

	if tested == 0 {
		return fmt.Errorf("no servers found in VPC %s for NAT external connectivity test", vpc.Name) //nolint:goerr113
	}

	return nil
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
func (testCtx *VPCPeeringTestCtx) bgpExternalNoNatTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
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
func (testCtx *VPCPeeringTestCtx) bgpExternalStaticNATTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testNATExternalConnectivity(ctx, vpc, true); err != nil {
		return false, nil, fmt.Errorf("testing BGP external static NAT connectivity: %w", err)
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
func (testCtx *VPCPeeringTestCtx) bgpExternalMasqueradeNATTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testNATExternalConnectivity(ctx, vpc, true); err != nil {
		return false, nil, fmt.Errorf("testing BGP external masquerade NAT connectivity: %w", err)
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
func (testCtx *VPCPeeringTestCtx) bgpExternalPortForwardNATTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testIperfToExternal(ctx, vpc, bgpInvertedNATCIDR, true); err != nil {
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
func (testCtx *VPCPeeringTestCtx) bgpExternalMasqueradePortForwardNATTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testNATExternalConnectivity(ctx, vpc, true); err != nil {
		return false, nil, fmt.Errorf("testing BGP external masquerade+port-forward NAT connectivity: %w", err)
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
func (testCtx *VPCPeeringTestCtx) staticExternalNoNATGatewayTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
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
func (testCtx *VPCPeeringTestCtx) staticExternalStaticNATGatewayTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testNATExternalConnectivity(ctx, vpc, false); err != nil {
		return false, nil, fmt.Errorf("testing static external static NAT connectivity: %w", err)
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
func (testCtx *VPCPeeringTestCtx) staticExternalMasqueradeNATGatewayTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testNATExternalConnectivity(ctx, vpc, false); err != nil {
		return false, nil, fmt.Errorf("testing static external masquerade NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// testIperfToExternal runs iperf3 from a VPC server to invertedNATCIDR:15201, testing
// connectivity through the gateway's inverted port-forward NAT (external side has NAT).
// One server in the VPC is sufficient.
// waitForBGPConvergence should be true for BGP externals where the port-forward target IP is
// reachable only after BGP route propagation; false for static externals with pre-configured routes.
func (testCtx *VPCPeeringTestCtx) testIperfToExternal(ctx context.Context, vpc *vpcapi.VPC, invertedNATCIDR string, waitForBGPConvergence bool) error {
	servers := &wiringapi.ServerList{}
	if err := testCtx.kube.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}

	extNATIP := strings.SplitN(invertedNATCIDR, "/", 2)[0]
	secs := testCtx.tcOpts.IPerfsSeconds
	if secs <= 0 {
		secs = 5
	}

	for _, server := range servers.Items {
		attachedSubnets, err := apiutil.GetAttachedSubnets(ctx, testCtx.kube, server.Name)
		if err != nil {
			continue
		}

		inVPC := false
		for subnetName := range attachedSubnets {
			if strings.HasPrefix(subnetName, vpc.Name+"/") {
				inVPC = true

				break
			}
		}
		if !inVPC {
			continue
		}

		sshCfg, err := testCtx.getSSH(ctx, server.Name)
		if err != nil {
			return fmt.Errorf("getting ssh config for %s: %w", server.Name, err)
		}

		cmd := fmt.Sprintf("toolbox -E LD_PRELOAD=/lib/x86_64-linux-gnu/libgcc_s.so.1 -q timeout %d iperf3 -J -c %s -p 15201 -t %d",
			secs+25, extNATIP, secs)
		slog.Debug("Testing iperf3 through inverted port-forward NAT", "server", server.Name, "target", extNATIP+":15201")
		_, _, iperfErr := retrySSHCmd(ctx, sshCfg, cmd, server.Name)
		if iperfErr != nil && waitForBGPConvergence {
			deadline := time.Now().Add(gwNATConvergenceTimeout)
			for iperfErr != nil {
				if time.Now().After(deadline) {
					break
				}
				slog.Debug("BGP port-forward route not yet propagated, retrying", "server", server.Name, "error", iperfErr, "retryIn", gwNATConvergenceInterval)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(gwNATConvergenceInterval):
				}
				_, _, iperfErr = retrySSHCmd(ctx, sshCfg, cmd, server.Name)
			}
			if iperfErr != nil {
				return fmt.Errorf("iperf3 from %s to %s:15201 (BGP not converged after %s): %w", server.Name, extNATIP, gwNATConvergenceTimeout, iperfErr)
			}
		} else if iperfErr != nil {
			return fmt.Errorf("iperf3 from %s to %s:15201: %w", server.Name, extNATIP, iperfErr)
		}

		return nil
	}

	return fmt.Errorf("no servers found in VPC %s for iperf3 test", vpc.Name) //nolint:goerr113
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
func (testCtx *VPCPeeringTestCtx) staticExternalPortForwardNATGatewayTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testIperfToExternal(ctx, vpc, staticInvertedNATCIDR, false); err != nil {
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
func (testCtx *VPCPeeringTestCtx) staticExternalMasqueradePortForwardNATGatewayTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	if err := testCtx.testNATExternalConnectivity(ctx, vpc, false); err != nil {
		return false, nil, fmt.Errorf("testing static external masquerade+port-forward NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// getExternalNATTestCases returns the external NAT test cases
func getExternalNATTestCases(testCtx *VPCPeeringTestCtx) []JUnitTestCase {
	return []JUnitTestCase{
		{
			Name: "Gateway Peering BGP External No NAT",
			F:    testCtx.bgpExternalNoNatTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Static NAT",
			F:    testCtx.bgpExternalStaticNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Masquerade NAT",
			F:    testCtx.bgpExternalMasqueradeNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Port Forward NAT",
			F:    testCtx.bgpExternalPortForwardNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering BGP External Masquerade and Port Forward NAT",
			F:    testCtx.bgpExternalMasqueradePortForwardNATTest,
			SkipFlags: SkipFlags{
				NoGateway:      true,
				NoBGPExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External No NAT",
			F:    testCtx.staticExternalNoNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Static NAT",
			F:    testCtx.staticExternalStaticNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Masquerade NAT",
			F:    testCtx.staticExternalMasqueradeNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Port Forward NAT",
			F:    testCtx.staticExternalPortForwardNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
		{
			Name: "Gateway Peering Static External Masquerade and Port Forward NAT",
			F:    testCtx.staticExternalMasqueradePortForwardNATGatewayTest,
			SkipFlags: SkipFlags{
				NoGateway:         true,
				NoStaticExternals: true,
			},
		},
	}
}
