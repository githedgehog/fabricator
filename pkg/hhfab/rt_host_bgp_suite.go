// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"sort"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
)

// makeHostBGPSuite builds the release-test suite for multihomed HostBGP
// scenarios. The suite assumes setup-vpcs has been run with
// HostBGPSubnet: true on a VLAB that includes multihomed servers (two
// Unbundled connections to two distinct orphan leaves), which is how
// Hedgehog's customers deploy HostBGP on TH5.
//
// Every case consumes the connectivity matrix through the on-ready
// test-connectivity path: setup installs the topology + any per-case
// peerings, then DoVLABTestConnectivity walks the matrix and asserts
// reachability per (server, subnet) pair. HostBGP VIPs are discovered
// via hhnet getvips; regular servers via ip addr show. No per-pair
// assertion is needed in the test — the matrix + executor is the
// source of truth.
//
// Out of scope for Phase 1: per-path reachability (each leaf
// independently), ECMP traffic-distribution validation, and leaf
// failover simulation. These require interface manipulation and
// leaf-side counter inspection, tracked as Phase 2+ work.
func makeHostBGPSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "HostBGP Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Multihomed HostBGP intra-VPC reachability",
			F:    testCtx.hostBGPIntraVPCTest,
			SkipFlags: SkipFlags{
				NoMultihomedHostBGP: true,
				NoServers:           true,
			},
		},
		{
			Name: "Multihomed HostBGP gateway peering",
			F:    testCtx.hostBGPGatewayPeeringTest,
			SkipFlags: SkipFlags{
				NoMultihomedHostBGP: true,
				NoGateway:           true,
				NoServers:           true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// hostBGPIntraVPCTest verifies intra-VPC reachability when the VPC contains
// a multihomed HostBGP server. Suite setup has already provisioned the
// topology; this case only waits for readiness and runs the matrix-driven
// test-connectivity. A pass proves the matrix correctly discovered the /32
// VIP on lo and that other servers in the same VPC can reach it via ECMP
// across the two leaves.
func (testCtx *VPCPeeringTestCtx) hostBGPIntraVPCTest(ctx context.Context) (bool, []RevertFunc, error) {
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for readiness: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("test-connectivity: %w", err)
	}

	return false, nil, nil
}

// hostBGPGatewayPeeringTest picks two HostBGP VPCs, installs a default
// gateway peering between them (no NAT), and verifies cross-VPC reachability
// to each HostBGP VIP via the matrix. Skips if fewer than two VPCs carry
// HostBGP subnets (i.e., the topology doesn't have enough multihomed
// servers to produce more than one HostBGP VPC).
func (testCtx *VPCPeeringTestCtx) hostBGPGatewayPeeringTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	var vpc1, vpc2 *vpcapi.VPC
	for i := range vpcs.Items {
		v := &vpcs.Items[i]
		hasHostBGP := false
		for _, s := range v.Spec.Subnets {
			if s != nil && s.HostBGP {
				hasHostBGP = true

				break
			}
		}
		if !hasHostBGP {
			continue
		}
		switch {
		case vpc1 == nil:
			vpc1 = v
		case vpc2 == nil:
			vpc2 = v
		}
		if vpc1 != nil && vpc2 != nil {
			break
		}
	}
	if vpc1 == nil || vpc2 == nil {
		return true, nil, fmt.Errorf("need at least two HostBGP VPCs for gateway peering test") //nolint:err113
	}

	gwPeerings := make(map[string]*gwapi.PeeringSpec)
	if err := appendGwPeeringSpec(gwPeerings, vpc1, vpc2, &GwPeeringOptions{}); err != nil {
		return false, nil, fmt.Errorf("building gateway peering spec: %w", err)
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, nil, nil, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("applying gateway peering: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for readiness after peering: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("test-connectivity: %w", err)
	}

	return false, nil, nil
}
