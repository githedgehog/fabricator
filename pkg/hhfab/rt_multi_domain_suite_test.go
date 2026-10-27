// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"errors"
	"testing"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

func rtDomainFabric(name string, domains ...string) wiringapi.Fabric {
	fabric := wiringapi.Fabric{
		Name: name,
	}
	fabric.Spec.Domains = map[string]wiringapi.FabricDomainSpec{}
	for _, domain := range domains {
		fabric.Spec.Domains[domain] = wiringapi.FabricDomainSpec{}
	}

	return fabric
}

func rtDomainVPC(name, fabric string, domains ...string) vpcapi.VPC {
	vpc := vpcapi.VPC{
		Spec: vpcapi.VPCSpec{Topology: vpcapi.VPCTopology{Fabric: fabric, Domains: domains}},
		Name: name,
	}

	return vpc
}

func names(vpcs []*vpcapi.VPC) []string {
	out := make([]string, 0, len(vpcs))
	for _, vpc := range vpcs {
		out = append(out, vpc.Name)
	}

	return out
}

func TestBuildDomainScenario(t *testing.T) {
	multi := rtDomainFabric(wiringapi.DefaultFabric, "default", "domain-b")
	vpcs := []vpcapi.VPC{
		rtDomainVPC("vpc-04", "", "default", "domain-b"),
		rtDomainVPC("vpc-02", "", "default"),
		rtDomainVPC("vpc-01", ""),
		rtDomainVPC("vpc-03", "", "domain-b"),
		rtDomainVPC("vpc-05", "fabric-b", "default"),
	}

	t.Run("classifies the VPCs of the first Fabric with several domains", func(t *testing.T) {
		sc, err := buildDomainScenario([]wiringapi.Fabric{rtDomainFabric("fabric-b", "default"), multi}, vpcs, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// no domains is the default domain, and the VPC of another Fabric is left out
		if got := names(sc.firstOnly); len(got) != 2 || got[0] != "vpc-01" || got[1] != "vpc-02" {
			t.Errorf("firstOnly = %v, want [vpc-01 vpc-02]", got)
		}
		if got := names(sc.secondOnly); len(got) != 1 || got[0] != "vpc-03" {
			t.Errorf("secondOnly = %v, want [vpc-03]", got)
		}
		if got := names(sc.shared); len(got) != 1 || got[0] != "vpc-04" {
			t.Errorf("shared = %v, want [vpc-04]", got)
		}
		if len(sc.vpcs) != 4 {
			t.Errorf("vpcs = %v, want the 4 of the default Fabric", names(sc.vpcs))
		}
	})

	t.Run("no Fabric with several domains", func(t *testing.T) {
		_, err := buildDomainScenario([]wiringapi.Fabric{rtDomainFabric(wiringapi.DefaultFabric, "default")}, vpcs, false)
		if err == nil || !errors.Is(err, errNoDomains) {
			t.Errorf("err = %v, want errNoDomains", err)
		}
	})

	t.Run("a domain with no VPC of its own", func(t *testing.T) {
		_, err := buildDomainScenario([]wiringapi.Fabric{multi}, []vpcapi.VPC{vpcs[1], vpcs[0]}, false)
		if err == nil || !errors.Is(err, errNoDomains) {
			t.Errorf("err = %v, want errNoDomains", err)
		}
	})

	t.Run("shared VPC needed and missing", func(t *testing.T) {
		_, err := buildDomainScenario([]wiringapi.Fabric{multi}, []vpcapi.VPC{vpcs[1], vpcs[3]}, true)
		if err == nil || !errors.Is(err, errNoDomains) {
			t.Errorf("err = %v, want errNoDomains", err)
		}
		if _, err := buildDomainScenario([]wiringapi.Fabric{multi}, []vpcapi.VPC{vpcs[1], vpcs[3]}, false); err != nil {
			t.Errorf("unexpected error without the shared VPC needed: %v", err)
		}
	})
}
