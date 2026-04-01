// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func makeHostBGPSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Host-BGP Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "HostBGP VPC peered with regular VPC",
			F:    testCtx.hostBGPVpcPeeringTest,
			SkipFlags: SkipFlags{
				NoUnbundledNonMclag: true,
				NoServers:           true,
			},
		},
		{
			Name: "HostBGP VPC peered with BGP external",
			F:    testCtx.hostBGPExternalPeeringTest,
			SkipFlags: SkipFlags{
				NoUnbundledNonMclag: true,
				NoExternals:         true,
				NoServers:           true,
			},
		},
		{
			Name: "HostBGP VPC in full mesh with other VPCs",
			F:    testCtx.hostBGPFullMeshTest,
			SkipFlags: SkipFlags{
				NoUnbundledNonMclag: true,
				NoServers:           true,
			},
		},
		{
			Name: "HostBGP VPC with gateway peering",
			F:    testCtx.hostBGPGatewayPeeringTest,
			SkipFlags: SkipFlags{
				NoUnbundledNonMclag: true,
				NoGateway:           true,
				NoServers:           true,
			},
		},
		{
			Name: "HostBGP VPC peering removal and re-add",
			F:    testCtx.hostBGPPeeringToggleTest,
			SkipFlags: SkipFlags{
				NoUnbundledNonMclag: true,
				NoServers:           true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// findHostBGPVPC finds the first VPC that has at least one hostBGP subnet.
// Returns the VPC, the name of the hostBGP subnet, and an error.
func findHostBGPVPC(ctx context.Context, kube kclient.Client) (*vpcapi.VPC, string, error) {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return nil, "", fmt.Errorf("listing VPCs: %w", err)
	}
	// sort for determinism
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})
	for i := range vpcs.Items {
		for subnetName, subnet := range vpcs.Items[i].Spec.Subnets {
			if subnet != nil && subnet.HostBGP {
				return &vpcs.Items[i], subnetName, nil
			}
		}
	}

	return nil, "", errors.New("no VPC with a hostBGP subnet found") //nolint:goerr113
}

// findRegularVPC finds the first VPC (other than excludeName) that has at least one
// non-hostBGP subnet. This intentionally accepts VPCs that also contain hostBGP subnets,
// since the test setup creates one hostBGP subnet per VPC alongside regular subnets.
func findRegularVPC(ctx context.Context, kube kclient.Client, excludeName string) (*vpcapi.VPC, error) {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return nil, fmt.Errorf("listing VPCs: %w", err)
	}
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})
	for i := range vpcs.Items {
		if vpcs.Items[i].Name == excludeName {
			continue
		}
		hasRegularSubnet := false
		for _, subnet := range vpcs.Items[i].Spec.Subnets {
			if subnet != nil && !subnet.HostBGP {
				hasRegularSubnet = true

				break
			}
		}
		if hasRegularSubnet {
			return &vpcs.Items[i], nil
		}
	}

	return nil, errors.New("no VPC with a regular (non-hostBGP) subnet found") //nolint:goerr113
}

// hostBGPVpcPeeringTest peers the hostBGP VPC with a regular VPC and tests connectivity.
func (testCtx *VPCPeeringTestCtx) hostBGPVpcPeeringTest(ctx context.Context) (bool, []RevertFunc, error) {
	hbVPC, subnetName, err := findHostBGPVPC(ctx, testCtx.kube)
	if err != nil {
		return false, nil, err
	}
	peerVPC, err := findRegularVPC(ctx, testCtx.kube, hbVPC.Name)
	if err != nil {
		return true, nil, err
	}
	slog.Debug("HostBGP VPC peering test", "hostBGPVPC", hbVPC.Name, "hostBGPSubnet", subnetName, "peerVPC", peerVPC.Name)

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	appendVpcPeeringSpecByName(vpcPeerings, hbVPC.Name, peerVPC.Name, "", []string{}, []string{})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, nil, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// hostBGPExternalPeeringTest peers the hostBGP VPC with the BGP external and tests connectivity.
func (testCtx *VPCPeeringTestCtx) hostBGPExternalPeeringTest(ctx context.Context) (bool, []RevertFunc, error) {
	hbVPC, subnetName, err := findHostBGPVPC(ctx, testCtx.kube)
	if err != nil {
		return false, nil, err
	}
	slog.Debug("HostBGP external peering test", "hostBGPVPC", hbVPC.Name, "hostBGPSubnet", subnetName, "external", testCtx.extName)

	extPeerings := make(map[string]*vpcapi.ExternalPeeringSpec)
	appendExtPeeringSpecByName(extPeerings, hbVPC.Name, testCtx.extName, []string{subnetName}, AllZeroPrefix)

	if err := DoSetupPeerings(ctx, testCtx.kube, nil, extPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// hostBGPFullMeshTest creates a full-mesh VPC peering including the hostBGP VPC and tests connectivity.
func (testCtx *VPCPeeringTestCtx) hostBGPFullMeshTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	if err := populateFullMeshVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, nil, fmt.Errorf("populating full mesh VPC peerings: %w", err)
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, nil, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// hostBGPGatewayPeeringTest creates a gateway peering between the hostBGP VPC and a regular VPC
// and tests connectivity.
func (testCtx *VPCPeeringTestCtx) hostBGPGatewayPeeringTest(ctx context.Context) (bool, []RevertFunc, error) {
	hbVPC, subnetName, err := findHostBGPVPC(ctx, testCtx.kube)
	if err != nil {
		return false, nil, err
	}
	peerVPC, err := findRegularVPC(ctx, testCtx.kube, hbVPC.Name)
	if err != nil {
		return true, nil, err
	}
	slog.Debug("HostBGP gateway peering test", "hostBGPVPC", hbVPC.Name, "hostBGPSubnet", subnetName, "peerVPC", peerVPC.Name)

	gwPeerings := make(map[string]*gwapi.PeeringSpec)
	appendGwPeeringSpec(gwPeerings, hbVPC, peerVPC, nil)

	if err := DoSetupPeerings(ctx, testCtx.kube, nil, nil, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up gateway peering: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// hostBGPPeeringToggleTest adds a VPC peering to the hostBGP VPC, tests connectivity,
// then removes it and verifies the hostBGP VPC is isolated.
func (testCtx *VPCPeeringTestCtx) hostBGPPeeringToggleTest(ctx context.Context) (bool, []RevertFunc, error) {
	hbVPC, subnetName, err := findHostBGPVPC(ctx, testCtx.kube)
	if err != nil {
		return false, nil, err
	}
	peerVPC, err := findRegularVPC(ctx, testCtx.kube, hbVPC.Name)
	if err != nil {
		return true, nil, err
	}
	slog.Debug("HostBGP peering toggle test", "hostBGPVPC", hbVPC.Name, "hostBGPSubnet", subnetName, "peerVPC", peerVPC.Name)

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec)
	appendVpcPeeringSpecByName(vpcPeerings, hbVPC.Name, peerVPC.Name, "", []string{}, []string{})

	// Add peering and verify connectivity
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, nil, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("connectivity with peering: %w", err)
	}

	// Remove peering and verify isolation
	if err := DoSetupPeerings(ctx, testCtx.kube, nil, nil, nil, true); err != nil {
		return false, nil, fmt.Errorf("removing peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("connectivity without peering: %w", err)
	}

	return false, nil, nil
}
