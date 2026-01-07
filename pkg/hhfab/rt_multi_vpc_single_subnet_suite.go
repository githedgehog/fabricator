// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
)

func makeMultiVPCSingleSubnetSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Multi-VPC Single-Subnet Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Starter Test",
			F:    testCtx.vpcPeeringsStarterTest,
			SkipFlags: SkipFlags{
				NoExternals:   true,
				SubInterfaces: true,
				NoServers:     true,
			},
		},
		{
			Name: "Only Externals",
			F:    testCtx.vpcPeeringsOnlyExternalsTest,
			SkipFlags: SkipFlags{
				NoExternals:   true,
				SubInterfaces: true,
				NoServers:     true,
			},
		},
		{
			Name: "Full Mesh All Externals",
			F:    testCtx.vpcPeeringsFullMeshAllExternalsTest,
			SkipFlags: SkipFlags{
				SubInterfaces: true,
				NoServers:     true,
			},
		},
		{
			Name: "Full Loop All Externals",
			F:    testCtx.vpcPeeringsFullLoopAllExternalsTest,
			SkipFlags: SkipFlags{
				SubInterfaces: true,
				NoServers:     true,
			},
		},
		{
			Name: "Sergei's Special Test",
			F:    testCtx.vpcPeeringsSergeisSpecialTest,
			SkipFlags: SkipFlags{
				NoExternals:   true,
				SubInterfaces: true,
				NoServers:     true,
			},
		},
		{
			Name: "Gateway Peering",
			F:    testCtx.gatewayPeeringTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
				NoServers: true,
			},
		},
		{
			Name: "Gateway Peering Loop",
			F:    testCtx.gatewayPeeringLoopTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
				NoServers: true,
			},
		},
		{
			Name: "Mixed VPC and Gateway Peering Loop",
			F:    testCtx.gatewayMixedPeeringLoopTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
				NoServers: true,
			},
		},
		{
			Name: "Mixed Gateway and Fabric External Peering",
			F:    testCtx.mixedGatewayAndFabricExternals,
			SkipFlags: SkipFlags{
				NoExternals: true,
				NoGateway:   true,
				NoServers:   true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// The starter test is presumably an arbitrary point in the space of possible VPC peering configurations.
// It was presumably chosen because going from this to a full mesh configuration could trigger
// the gNMI bug. Note that in order to reproduce it one should disable the forced cleanup between
// tests.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsStarterTest(ctx context.Context) (bool, []RevertFunc, error) {
	// 1+2:r=border 1+3 3+5 2+4 4+6 5+6 6+7 7+8 8+9  5~default--5835:s=subnet-01 6~default--5835:s=subnet-01  1~default--5835:s=subnet-01  2~default--5835:s=subnet-01  9~default--5835:s=subnet-01  7~default--5835:s=subnet-01
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 9 {
		return true, nil, errNotEnoughVPCs
	}

	// check whether border switchgroup exists
	remote := "border"
	if err := checkRemotePeering(ctx, testCtx.kube, remote, 1, 2); err != nil {
		slog.Warn("Remote peering not viable, skipping it", "error", err)
		remote = ""
	}
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 9)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, remote, []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 1, 3, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 3, 5, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 4, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 4, 6, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 5, 6, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 6, 7, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 7, 8, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 8, 9, "", []string{}, []string{})

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	appendExtPeeringSpec(externalPeerings, 5, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 6, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 1, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 2, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 9, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 7, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// Test connectivity between all VPCs in a full mesh configuration, including all externals
// Then, remove one external peering and test connectivity again.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullMeshAllExternalsTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 15)
	if err := populateFullMeshVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, nil, fmt.Errorf("populating full mesh VPC peerings: %w", err)
	}

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, nil, fmt.Errorf("populating all external VPC peerings: %w", err)
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	// bonus: remove one external to make sure we do not leek access to it, test again
	if len(externalPeerings) < 2 {
		slog.Warn("Not enough external peerings to remove one and check for leaks, skipping next step")
	} else {
		slog.Debug("Removing one external peering...")
		for key := range externalPeerings {
			delete(externalPeerings, key)

			break
		}
		if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
			return false, nil, fmt.Errorf("setting up peerings: %w", err)
		}
		if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
			return false, nil, err
		}
	}

	return false, nil, nil
}

// Test connectivity between all VPCs with no peering except of the external ones.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsOnlyExternalsTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, nil, fmt.Errorf("populating all external VPC peerings: %w", err)
	}
	if len(externalPeerings) == 0 {
		slog.Info("No external peerings found, skipping test")

		return true, nil, errNoExternals
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// Test connectivity between all VPCs in a full loop configuration, including all externals.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullLoopAllExternalsTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 6)
	if err := populateFullLoopVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, nil, fmt.Errorf("populating full loop VPC peerings: %w", err)
	}
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, nil, fmt.Errorf("populating all external VPC peerings: %w", err)
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// Arbitrary configuration which again was shown to occasionally trigger the gNMI bug.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsSergeisSpecialTest(ctx context.Context) (bool, []RevertFunc, error) {
	// 1+2 2+3 2+4:r=border 6+5 1~default--5835:s=subnet-01
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 6 {
		return true, nil, errNotEnoughVPCs
	}

	// check whether border switchgroup exists
	remote := "border"
	if err := checkRemotePeering(ctx, testCtx.kube, remote, 2, 3); err != nil {
		slog.Warn("Remote peering not viable, skipping it", "error", err)
		remote = ""
	}

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 4)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, remote, []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 4, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 6, 5, "", []string{}, []string{})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	appendExtPeeringSpec(externalPeerings, 1, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// Test basic gateway peering connectivity between two VPCs.
// Creates a gateway peering between the first two VPCs found, exposing all subnets
// from each VPC, then tests connectivity to ensure traffic flows through the gateway.
func (testCtx *VPCPeeringTestCtx) gatewayPeeringTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for gateway peering test") //nolint:goerr113
	}

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 1)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Use all subnets from both VPCs by passing empty subnet lists
	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, nil, nil)

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up gateway peerings: %w", err)
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing gateway peering connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering in a loop configuration where each VPC peers with the next one.
// VPC1↔VPC2↔VPC3↔...↔VPCn↔VPC1. Test connectivity in a complete loop.
func (testCtx *VPCPeeringTestCtx) gatewayPeeringLoopTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 3 {
		return true, nil, fmt.Errorf("not enough VPCs for gateway peering loop test (need at least 3)") //nolint:goerr113
	}

	// Sort VPCs by name to ensure proper loop order
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, len(vpcs.Items))

	// Create loop: VPC0↔VPC1↔VPC2↔...↔VPCn-1↔VPC0
	for i := 0; i < len(vpcs.Items); i++ {
		vpc1 := &vpcs.Items[i]
		vpc2 := &vpcs.Items[(i+1)%len(vpcs.Items)] // wrap around to create loop

		// Use all subnets from both VPCs by passing empty subnet lists
		appendGwPeeringSpec(gwPeerings, vpc1, vpc2, nil, nil)
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up gateway loop peerings: %w", err)
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing gateway loop connectivity: %w", err)
	}

	return false, nil, nil
}

// Test combining VPC peering and gateway peering in an alternating loop configuration.
// Create alternating VPC and gateway peerings to form a complete loop through all VPCs.
func (testCtx *VPCPeeringTestCtx) gatewayMixedPeeringLoopTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 4 {
		return true, nil, fmt.Errorf("not enough VPCs for mixed peering loop test (need at least 4)") //nolint:goerr113
	}

	// Sort VPCs by name to ensure consistent ordering
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 0)

	// Create alternating VPC and Gateway peerings to form a complete loop
	for i := 0; i < len(vpcs.Items); i++ {
		vpc1 := &vpcs.Items[i]
		vpc2 := &vpcs.Items[(i+1)%len(vpcs.Items)] // wrap around to create loop

		if i%2 == 0 {
			// Even-indexed connections use VPC peering
			appendVpcPeeringSpecByName(vpcPeerings, vpc1.Name, vpc2.Name, "", []string{}, []string{})
		} else {
			// Odd-indexed connections use Gateway peering
			// Use all subnets from both VPCs by passing empty subnet lists
			appendGwPeeringSpec(gwPeerings, vpc1, vpc2, nil, nil)
		}
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up mixed peering loop: %w", err)
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing mixed peering loop connectivity: %w", err)
	}

	return false, nil, nil
}

// Test combining external peering via fabric and via gateway.
func (testCtx *VPCPeeringTestCtx) mixedGatewayAndFabricExternals(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 4 {
		return true, nil, fmt.Errorf("need at least 4 VPCs: %w", errNotEnoughVPCs)
	}

	// Sort VPCs by name to ensure consistent ordering
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 0)

	// Create alternating VPC and Gateway peerings to form a complete loop
	for i, vpc := range vpcs.Items {
		if i%2 == 0 {
			// Even-indexed connections use fabric peering
			appendExtPeeringSpecByName(externalPeerings, vpc.Name, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
		} else {
			// Odd-indexed connections use Gateway peering
			appendGwExtPeeringSpec(gwPeerings, &vpc, nil, testCtx.extName)
		}
	}

	slog.Debug("Creating external peering via the fabric (even VPCs) and via gateway (odd VPCs)")
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up mixed peering loop: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing mixed peering loop connectivity: %w", err)
	}

	// now peer some VPCs to make sure we are not blocking traffic via the filters
	slog.Debug("Creating VPC peering between some VPCs to verify connectivity is not affected by mixed external peerings")
	appendVpcPeeringSpecByName(vpcPeerings, vpcs.Items[0].Name, vpcs.Items[1].Name, "", []string{}, []string{})
	appendGwPeeringSpec(gwPeerings, &vpcs.Items[2], &vpcs.Items[3], nil, nil)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up mixed peering loop: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing mixed peering loop connectivity: %w", err)
	}

	return false, nil, nil
}
