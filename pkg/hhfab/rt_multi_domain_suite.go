// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

var errNoDomains = errors.New("not enough domains or VPCs")

// makeMultiDomainSuite is the suite for clusters where a Fabric has several domains: a VPC peers
// only with the VPCs and Externals that share a domain with it, and a leaf shared by two domains
// is not a path between them. It runs only on such clusters and every test asserts the
// connectivity matrix.
func makeMultiDomainSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name:        "Multi-Domain Suite",
		MultiDomain: true,
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Domain Isolation",
			F:    domainIsolationTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Domain Shared Leaf Peering",
			F:    domainSharedLeafPeeringTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Domain Full Mesh Peering",
			F:    domainFullMeshPeeringTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Domain External Peering",
			F:    domainExternalPeeringTest,
			SkipFlags: SkipFlags{
				SubInterfaces: true,
				NoServers:     true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// domainScenario is the VPCs of the first Fabric with several domains, told apart by the first two
// domains of that Fabric in name order: the VPCs of only the first, of only the second, and those
// of both, which sit on a leaf the two domains share. Each list is in name order.
type domainScenario struct {
	vpcs       []*vpcapi.VPC
	firstOnly  []*vpcapi.VPC
	secondOnly []*vpcapi.VPC
	shared     []*vpcapi.VPC
}

func loadDomainScenario(ctx context.Context, testCtx *VPCPeeringTestCtx, needShared bool) (*domainScenario, error) {
	fabrics := &wiringapi.FabricList{}
	if err := testCtx.kube.List(ctx, fabrics); err != nil {
		return nil, fmt.Errorf("listing Fabrics: %w", err)
	}
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return nil, fmt.Errorf("listing VPCs: %w", err)
	}

	return buildDomainScenario(fabrics.Items, vpcs.Items, needShared)
}

func buildDomainScenario(fabrics []wiringapi.Fabric, vpcItems []vpcapi.VPC, needShared bool) (*domainScenario, error) {
	slices.SortFunc(fabrics, func(a, b wiringapi.Fabric) int { return strings.Compare(a.Name, b.Name) })
	idx := slices.IndexFunc(fabrics, func(fabric wiringapi.Fabric) bool { return len(fabric.Spec.Domains) > 1 })
	if idx < 0 {
		return nil, fmt.Errorf("%w: no Fabric with several domains", errNoDomains)
	}
	fabric := &fabrics[idx]
	domains := make([]string, 0, len(fabric.Spec.Domains))
	for domain := range fabric.Spec.Domains {
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	first, second := domains[0], domains[1]

	sc := &domainScenario{}
	for idx := range vpcItems {
		vpc := &vpcItems[idx]
		vpcFabric, vpcDomains := vpcGroupKey(vpc)
		if vpcFabric != fabric.Name {
			continue
		}
		sc.vpcs = append(sc.vpcs, vpc)

		inFirst, inSecond := slices.Contains(vpcDomains, first), slices.Contains(vpcDomains, second)
		switch {
		case inFirst && inSecond:
			sc.shared = append(sc.shared, vpc)
		case inFirst && len(vpcDomains) == 1:
			sc.firstOnly = append(sc.firstOnly, vpc)
		case inSecond && len(vpcDomains) == 1:
			sc.secondOnly = append(sc.secondOnly, vpc)
		}
	}
	for _, list := range [][]*vpcapi.VPC{sc.vpcs, sc.firstOnly, sc.secondOnly, sc.shared} {
		slices.SortFunc(list, func(a, b *vpcapi.VPC) int { return strings.Compare(a.Name, b.Name) })
	}

	if len(sc.firstOnly) == 0 || len(sc.secondOnly) == 0 {
		return nil, fmt.Errorf("%w: %d VPCs of only domain %s and %d of only domain %s in Fabric %s, need one of each", errNoDomains, len(sc.firstOnly), first, len(sc.secondOnly), second, fabric.Name)
	}
	if needShared && len(sc.shared) == 0 {
		return nil, fmt.Errorf("%w: no VPC of both domains %s and %s in Fabric %s", errNoDomains, first, second, fabric.Name)
	}

	return sc, nil
}

// skipOnNoDomains turns the missing domains or VPCs of the topology into a skip
func skipOnNoDomains(err error) (bool, []RevertFunc, error) {
	return errors.Is(err, errNoDomains), nil, err
}

// No peerings: the VPCs of different domains, and of the same one, reach nothing of each other.
func domainIsolationTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if _, err := loadDomainScenario(ctx, testCtx, false); err != nil {
		return skipOnNoDomains(err)
	}

	return false, nil, testCtx.runPeeringMatrix(ctx, matrix, map[string]*vpcapi.VPCPeeringSpec{}, nil)
}

// A VPC of each domain is peered with a VPC on the shared leaf: each reaches the shared VPC, and
// the two do not reach each other, since the leaf they both reach is not a path between them.
func domainSharedLeafPeeringTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, err := loadDomainScenario(ctx, testCtx, true)
	if err != nil {
		return skipOnNoDomains(err)
	}

	vpcPeerings := map[string]*vpcapi.VPCPeeringSpec{}
	appendVpcPeeringSpecByName(vpcPeerings, sc.firstOnly[0].Name, sc.shared[0].Name, []string{}, []string{})
	appendVpcPeeringSpecByName(vpcPeerings, sc.secondOnly[0].Name, sc.shared[0].Name, []string{}, []string{})

	return false, nil, testCtx.runPeeringMatrix(ctx, matrix, vpcPeerings, nil)
}

// Every pair of VPCs that share a domain is peered, which is all the fabric accepts: the VPCs of
// different domains reach each other only through the shared VPCs they are peered with, and the
// VPCs with no domain in common stay isolated.
func domainFullMeshPeeringTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	sc, err := loadDomainScenario(ctx, testCtx, false)
	if err != nil {
		return skipOnNoDomains(err)
	}

	vpcPeerings := map[string]*vpcapi.VPCPeeringSpec{}
	for idx, vpc1 := range sc.vpcs {
		_, domains1 := vpcGroupKey(vpc1)
		for _, vpc2 := range sc.vpcs[idx+1:] {
			_, domains2 := vpcGroupKey(vpc2)
			if slices.ContainsFunc(domains1, func(domain string) bool { return slices.Contains(domains2, domain) }) {
				appendVpcPeeringSpecByName(vpcPeerings, vpc1.Name, vpc2.Name, []string{}, []string{})
			}
		}
	}

	return false, nil, testCtx.runPeeringMatrix(ctx, matrix, vpcPeerings, nil)
}

// Every VPC is peered with the Externals of its own domains: it reaches them, and the VPCs of the
// other domains, which the fabric does not let peer with those Externals, do not.
func domainExternalPeeringTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	if _, err := loadDomainScenario(ctx, testCtx, false); err != nil {
		return skipOnNoDomains(err)
	}

	extPeerings := map[string]*vpcapi.ExternalPeeringSpec{}
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, extPeerings); err != nil {
		return false, nil, fmt.Errorf("populating external peerings: %w", err)
	}
	if len(extPeerings) == 0 {
		return true, nil, nil
	}

	return false, nil, testCtx.runPeeringMatrix(ctx, matrix, nil, extPeerings)
}
