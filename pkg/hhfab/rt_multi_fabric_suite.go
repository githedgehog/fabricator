// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
)

var errNoInterconnect = errors.New("not enough interconnect pairs or VPCs")

// makeMultiFabricSuite is the suite for clusters with several Fabrics: the VPCs of different
// Fabrics reach each other only over an interconnect pair both have an External peering to. It
// runs only on such clusters and every test asserts the connectivity matrix.
func makeMultiFabricSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name:        "Multi-Fabric Suite",
		MultiFabric: true,
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Interconnect Allow",
			F:    interconnectAllowTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Interconnect One-Sided Peering",
			F:    interconnectOneSidedTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Interconnect No Peering",
			F:    interconnectNoPeeringTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Interconnect Shared Pair",
			F:    interconnectSharedPairTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Interconnect Separate Pairs",
			F:    interconnectSeparatePairsTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Interconnect Transit Through Gateway",
			F:    interconnectTransitTest,
			SkipFlags: SkipFlags{
				NoServers: true,
				NoGateway: true,
			},
		},
		{
			Name: "Interconnect Transit Without Gateway Peering",
			F:    interconnectTransitNoGatewayPeeringTest,
			SkipFlags: SkipFlags{
				NoServers: true,
				NoGateway: true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// rtInterconnectPair is the two Externals of an interconnect pair, one in each Fabric, with the
// 0-based number of the pair
type rtInterconnectPair struct {
	ordinal int
	sides   [2]*vpcapi.External
}

// findRTInterconnectPairs are the interconnect pairs between two Fabrics, ordered by number, from
// the annotation that names the peer External
func findRTInterconnectPairs(externals []vpcapi.External) []rtInterconnectPair {
	byName := map[string]*vpcapi.External{}
	for idx := range externals {
		byName[externals[idx].Name] = &externals[idx]
	}

	var pairs []rtInterconnectPair
	for idx := range externals {
		ext := &externals[idx]
		peer := byName[ext.Annotations[VLABInterconnectAnnotation]]
		if peer == nil || peer.Annotations[VLABInterconnectAnnotation] != ext.Name {
			continue
		}
		// each pair once, from the side in the Fabric that sorts first
		if fabricOrDefault(ext.Spec.Topology.Fabric) >= fabricOrDefault(peer.Spec.Topology.Fabric) {
			continue
		}

		pairs = append(pairs, rtInterconnectPair{ordinal: interconnectOrdinal(ext.Name), sides: [2]*vpcapi.External{ext, peer}})
	}
	slices.SortFunc(pairs, func(a, b rtInterconnectPair) int { return a.ordinal - b.ordinal })

	return pairs
}

// interconnectScenario is what a test needs of the cluster: the pairs, and the VPCs of each Fabric
// of the first pair in name order, so the k-th VPC of a side is the same VPC in every test
type interconnectScenario struct {
	externals  []vpcapi.External
	pairs      []rtInterconnectPair
	vpcs       [2][]*vpcapi.VPC
	namespaces []vpcapi.IPv4Namespace
}

func loadInterconnectScenario(ctx context.Context, testCtx *VPCPeeringTestCtx, minPairs, minVPCs int) (*interconnectScenario, error) {
	externals := &vpcapi.ExternalList{}
	if err := testCtx.kube.List(ctx, externals); err != nil {
		return nil, fmt.Errorf("listing externals: %w", err)
	}
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return nil, fmt.Errorf("listing VPCs: %w", err)
	}
	namespaces := &vpcapi.IPv4NamespaceList{}
	if err := testCtx.kube.List(ctx, namespaces); err != nil {
		return nil, fmt.Errorf("listing IPv4 namespaces: %w", err)
	}

	sc := &interconnectScenario{externals: externals.Items, pairs: findRTInterconnectPairs(externals.Items), namespaces: namespaces.Items}
	if len(sc.pairs) < minPairs {
		return nil, fmt.Errorf("%w: %d pairs, need %d", errNoInterconnect, len(sc.pairs), minPairs)
	}

	for idx := range vpcs.Items {
		vpc := &vpcs.Items[idx]
		for side := range sc.vpcs {
			if fabricOrDefault(vpc.Spec.Topology.Fabric) == fabricOrDefault(sc.pairs[0].sides[side].Spec.Topology.Fabric) {
				sc.vpcs[side] = append(sc.vpcs[side], vpc)
			}
		}
	}
	for side := range sc.vpcs {
		slices.SortFunc(sc.vpcs[side], func(a, b *vpcapi.VPC) int { return strings.Compare(a.Name, b.Name) })
		if len(sc.vpcs[side]) < minVPCs {
			return nil, fmt.Errorf("%w: %d VPCs in Fabric %s, need %d", errNoInterconnect, len(sc.vpcs[side]), sc.pairs[0].sides[side].Spec.Topology.Fabric, minVPCs)
		}
	}

	return sc, nil
}

// peer adds an External peering of the VPC with the External of the pair on its side, permitting
// every subnet of the VPC and the address space of the External on the other side
func (sc *interconnectScenario) peer(extPeerings map[string]*vpcapi.ExternalPeeringSpec, pair, side int, vpc *vpcapi.VPC) {
	other := sc.pairs[pair].sides[1-side]
	appendExtPeeringSpecByName(extPeerings, vpc.Name, sc.pairs[pair].sides[side].Name,
		slices.Sorted(maps.Keys(vpc.Spec.Subnets)), namespaceSubnets(sc.namespaces, other))
}

// runInterconnectMatrix applies the External peerings and asserts the matrix: the unpeered pairs of VPCs must be isolated
// too, so a path the model does not expect fails the test
func (testCtx *VPCPeeringTestCtx) runInterconnectMatrix(ctx context.Context, matrix *ConnectivityMatrix, extPeerings map[string]*vpcapi.ExternalPeeringSpec) error {
	return testCtx.runPeeringMatrix(ctx, matrix, nil, extPeerings, nil)
}

// runPeeringMatrix applies the peerings, refreshes the matrix and asserts it
func (testCtx *VPCPeeringTestCtx) runPeeringMatrix(ctx context.Context, matrix *ConnectivityMatrix, vpcPeerings map[string]*vpcapi.VPCPeeringSpec,
	extPeerings map[string]*vpcapi.ExternalPeeringSpec, gwPeerings map[string]*gwapi.PeeringSpec,
) error {
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, extPeerings, gwPeerings, true); err != nil {
		return fmt.Errorf("setting up peerings: %w", err)
	}
	if err := matrix.Repopulate(ctx, testCtx.kube); err != nil {
		return fmt.Errorf("refreshing matrix after peerings: %w", err)
	}
	if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts, matrix); err != nil {
		return fmt.Errorf("testing connectivity: %w", err)
	}

	return nil
}

// skipOnNoInterconnect turns the missing redundancy of the topology into a skip
func skipOnNoInterconnect(err error) (bool, []RevertFunc, error) {
	return errors.Is(err, errNoInterconnect), nil, err
}

// The first VPC of each Fabric is peered with its side of the first pair: they reach each other,
// and nothing else crosses.
func interconnectAllowTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, err := loadInterconnectScenario(ctx, testCtx, 1, 1)
	if err != nil {
		return skipOnNoInterconnect(err)
	}

	extPeerings := map[string]*vpcapi.ExternalPeeringSpec{}
	for side := range sc.vpcs {
		sc.peer(extPeerings, 0, side, sc.vpcs[side][0])
	}

	return false, nil, testCtx.runInterconnectMatrix(ctx, matrix, extPeerings)
}

// Only the VPC of the first Fabric is peered with the interconnect: the other side has no path
// back, so nothing crosses in either direction.
func interconnectOneSidedTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, err := loadInterconnectScenario(ctx, testCtx, 1, 1)
	if err != nil {
		return skipOnNoInterconnect(err)
	}

	extPeerings := map[string]*vpcapi.ExternalPeeringSpec{}
	sc.peer(extPeerings, 0, 0, sc.vpcs[0][0])

	return false, nil, testCtx.runInterconnectMatrix(ctx, matrix, extPeerings)
}

// No peering to the interconnect Externals: the Fabrics are isolated from each other.
func interconnectNoPeeringTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if _, err := loadInterconnectScenario(ctx, testCtx, 1, 1); err != nil {
		return skipOnNoInterconnect(err)
	}

	return false, nil, testCtx.runInterconnectMatrix(ctx, matrix, map[string]*vpcapi.ExternalPeeringSpec{})
}

// Two VPCs of each Fabric are peered with the same pair: every VPC of one Fabric reaches both
// VPCs of the other, since the interconnect does not tell VPCs of a pair apart.
func interconnectSharedPairTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, err := loadInterconnectScenario(ctx, testCtx, 1, 2)
	if err != nil {
		return skipOnNoInterconnect(err)
	}

	extPeerings := map[string]*vpcapi.ExternalPeeringSpec{}
	for side := range sc.vpcs {
		for _, vpc := range sc.vpcs[side][:2] {
			sc.peer(extPeerings, 0, side, vpc)
		}
	}

	return false, nil, testCtx.runInterconnectMatrix(ctx, matrix, extPeerings)
}

// The k-th VPC of each Fabric is peered with its side of the k-th pair: the VPCs of the same
// number reach each other and the VPCs on different pairs do not (cross talk).
func interconnectSeparatePairsTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, err := loadInterconnectScenario(ctx, testCtx, 2, 2)
	if err != nil {
		return skipOnNoInterconnect(err)
	}

	extPeerings := map[string]*vpcapi.ExternalPeeringSpec{}
	for pair := range sc.pairs[:2] {
		for side := range sc.vpcs {
			sc.peer(extPeerings, pair, side, sc.vpcs[side][pair])
		}
	}

	return false, nil, testCtx.runInterconnectMatrix(ctx, matrix, extPeerings)
}

// loadTransitScenario is the scenario of the first pair together with the Fabric behind it that
// reaches its internet through a Gateway: side is the Fabric whose VPCs go through the pair, and
// transit is the gateway peering in the other one that joins the pair to that Fabric's External.
func loadTransitScenario(ctx context.Context, testCtx *VPCPeeringTestCtx) (*interconnectScenario, int, *transitPeering, error) {
	sc, err := loadInterconnectScenario(ctx, testCtx, 1, 1)
	if err != nil {
		return nil, 0, nil, err
	}

	gateways := &gwapi.GatewayList{}
	if err := testCtx.kube.List(ctx, gateways); err != nil {
		return nil, 0, nil, fmt.Errorf("listing gateways: %w", err)
	}
	gatewayFabrics := map[string]bool{}
	for _, gw := range gateways.Items {
		gatewayFabrics[fabricOrDefault(gw.Spec.Topology.Fabric)] = true
	}

	for side := range sc.vpcs {
		transit, err := buildTransitPeering(sc.externals, sc.namespaces, sc.pairs[0].sides[side], sc.pairs[0].sides[1-side], gatewayFabrics)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("building transit gateway peering: %w", err)
		}
		if transit != nil {
			return sc, side, transit, nil
		}
	}

	return nil, 0, nil, fmt.Errorf("%w: no Fabric behind the first pair has a gateway and an External with a NAT pool", errNoInterconnect)
}

// transitExtPeerings peers the first VPC of the Fabric that goes through the pair with its
// interconnect External, for the address space of the other Fabric and for the internet
func (sc *interconnectScenario) transitExtPeerings(side int) map[string]*vpcapi.ExternalPeeringSpec {
	vpc := sc.vpcs[side][0]
	other := sc.pairs[0].sides[1-side]
	extPeerings := map[string]*vpcapi.ExternalPeeringSpec{}
	appendExtPeeringSpecByName(extPeerings, vpc.Name, sc.pairs[0].sides[side].Name,
		slices.Sorted(maps.Keys(vpc.Spec.Subnets)), append(namespaceSubnets(sc.namespaces, other), AllZeroPrefix...))

	return extPeerings
}

// The first VPC of the Fabric with no Gateway is peered with its side of the first pair for the
// internet, and the other Fabric's Gateway joins its side of the pair to its External: the VPC
// reaches that External through the Gateway, and no other VPC does.
func interconnectTransitTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, side, transit, err := loadTransitScenario(ctx, testCtx)
	if err != nil {
		return skipOnNoInterconnect(err)
	}

	gwPeerings := map[string]*gwapi.PeeringSpec{transit.name: transit.spec}

	return false, nil, testCtx.runPeeringMatrix(ctx, matrix, nil, sc.transitExtPeerings(side), gwPeerings)
}

// The same External peering with no gateway peering in the other Fabric: the VPC has a route to the
// interconnect and nothing past it, so it does not reach the External.
func interconnectTransitNoGatewayPeeringTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, side, _, err := loadTransitScenario(ctx, testCtx)
	if err != nil {
		return skipOnNoInterconnect(err)
	}

	return false, nil, testCtx.runPeeringMatrix(ctx, matrix, nil, sc.transitExtPeerings(side), nil)
}
