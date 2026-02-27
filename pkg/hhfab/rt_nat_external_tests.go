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

// Test gateway external peering with no NAT (baseline)
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

	// No NAT - baseline test
	appendGwExtPeeringSpec(gwPeerings, vpc, nil, testCtx.extName)

	slog.Info("Testing BGP external peering with no NAT", "vpc", vpc.Name, "external", testCtx.extName)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up BGP external peering: %w", err)
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing BGP external connectivity: %w", err)
	}

	return false, nil, nil
}

// getExternalNATTestCases returns the external NAT test cases
func getExternalNATTestCases(testCtx *VPCPeeringTestCtx) []JUnitTestCase {
	return []JUnitTestCase{
		{
			Name: "BGP External No NAT",
			F:    testCtx.bgpExternalNoNatTest,
			SkipFlags: SkipFlags{
				NoGateway:   true,
				NoExternals: true,
			},
		},
	}
}
