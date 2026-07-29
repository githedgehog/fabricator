// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"net/netip"
	"testing"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
)

func exposeNotFixture(ips []gwapi.PeeringEntryIP) (*gwapi.PeeringEntry, gwapi.VPCInfo) {
	peering := &gwapi.PeeringEntry{
		Expose: []gwapi.PeeringEntryExpose{{IPs: ips}},
	}
	vpc := gwapi.VPCInfo{
		Spec: gwapi.VPCInfoSpec{
			Subnets: map[string]*gwapi.VPCInfoSubnet{
				"default": {CIDR: "10.0.1.0/24"},
			},
		},
	}

	return peering, vpc
}

// Exclusions land in expose.IPs as standalone Not entries, after the includes.
func TestBuildExposesAppendsNotEntries(t *testing.T) {
	exposes, err := buildExposes([]string{"10.0.1.0/24"}, nil, []string{"10.0.1.5/32"}, NATModeStatic, nil)
	if err != nil {
		t.Fatalf("buildExposes: %v", err)
	}
	if len(exposes) != 1 {
		t.Fatalf("expected 1 expose, got %d", len(exposes))
	}
	want := []gwapi.PeeringEntryIP{
		{CIDR: "10.0.1.0/24"},
		{Not: "10.0.1.5/32"},
	}
	got := exposes[0].IPs
	if len(got) != len(want) {
		t.Fatalf("expected IPs %+v, got %+v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IPs[%d]: expected %+v, got %+v", i, want[i], got[i])
		}
	}
	if exposes[0].NAT != nil {
		t.Fatalf("expected no NAT block without natCIDRs, got %+v", exposes[0].NAT)
	}
}

// An exclusion entry and an include entry describe the same configuration
// whichever way round they are listed, so the subnet-level verdict must not
// depend on their order.
func TestExposeNotOrderInvariance(t *testing.T) {
	orders := map[string][]gwapi.PeeringEntryIP{
		"include first": {
			{VPCSubnet: "default"},
			{Not: "10.0.1.128/25"},
		},
		"not first": {
			{Not: "10.0.1.128/25"},
			{VPCSubnet: "default"},
		},
	}

	for name, ips := range orders {
		t.Run(name, func(t *testing.T) {
			peering, vpc := exposeNotFixture(ips)
			present, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if !present {
				t.Fatalf("expected the subnet to be reported as exposed")
			}
		})
	}
}

// A peering entry carrying only an exclusion exposes nothing.
func TestExposeNotAloneExposesNothing(t *testing.T) {
	peering, vpc := exposeNotFixture([]gwapi.PeeringEntryIP{
		{Not: "10.0.1.128/25"},
	})

	present, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if present {
		t.Fatalf("expected the subnet not to be reported as exposed")
	}
}

func exclusionMatrixFixture() (*ConnectivityMatrix, map[string]*Endpoint) {
	eps := map[string]*Endpoint{
		"server-3": {Server: &ServerEndpoint{
			Name: "server-3", VPC: "vpc-03", Subnet: "default", IP: netip.MustParseAddr("10.10.3.2"),
		}},
		"server-4": {Server: &ServerEndpoint{
			Name: "server-4", VPC: "vpc-04", Subnet: "default", IP: netip.MustParseAddr("10.10.4.2"),
		}},
		"server-5": {Server: &ServerEndpoint{
			Name: "server-5", VPC: "vpc-04", Subnet: "default", IP: netip.MustParseAddr("10.10.4.3"),
		}},
	}

	matrix := NewConnectivityMatrix()
	for _, ep := range eps {
		matrix.AllEndpoints = append(matrix.AllEndpoints, ep)
	}
	for _, src := range matrix.AllEndpoints {
		for _, dst := range matrix.AllEndpoints {
			if src == dst || src.Server.VPC == dst.Server.VPC {
				continue
			}
			matrix.Add(ConnectivityExpectation{
				Pair:    EndpointPair{Source: src, Destination: dst},
				Verdict: VerdictAllow,
				Reason:  ReachabilityReasonGatewayPeering,
				Peering: "vpc-03--vpc-04",
			})
		}
	}

	return matrix, eps
}

// Excluding a host address denies that host in both directions across the
// peering, and leaves every other pair alone.
func TestApplyGatewayExposeExclusionsIsHostScoped(t *testing.T) {
	matrix, eps := exclusionMatrixFixture()

	applyGatewayExposeExclusions(matrix, map[string]map[string][]netip.Prefix{
		"vpc-03--vpc-04": {"vpc-04": {netip.MustParsePrefix("10.10.4.2/32")}},
	})

	for _, tc := range []struct {
		src, dst string
		want     ConnectivityVerdict
	}{
		{"server-3", "server-4", VerdictDeny},
		{"server-4", "server-3", VerdictDeny},
		{"server-3", "server-5", VerdictAllow},
		{"server-5", "server-3", VerdictAllow},
	} {
		got := matrix.Lookup(eps[tc.src], eps[tc.dst], ProtoPort{}).Verdict
		if got != tc.want {
			t.Errorf("%s -> %s: expected %s, got %s", tc.src, tc.dst, tc.want, got)
		}
	}
}

// An exclusion covering no assigned address changes nothing.
func TestApplyGatewayExposeExclusionsUnusedAddress(t *testing.T) {
	matrix, _ := exclusionMatrixFixture()

	applyGatewayExposeExclusions(matrix, map[string]map[string][]netip.Prefix{
		"vpc-03--vpc-04": {"vpc-04": {netip.MustParsePrefix("10.10.4.99/32")}},
	})

	for _, src := range matrix.AllEndpoints {
		for _, dst := range matrix.AllEndpoints {
			if src == dst || src.Server.VPC == dst.Server.VPC {
				continue
			}
			if got := matrix.Lookup(src, dst, ProtoPort{}).Verdict; got != VerdictAllow {
				t.Errorf("%s -> %s: expected %s, got %s", src.Server.Name, dst.Server.Name, VerdictAllow, got)
			}
		}
	}
}

// Exclusions only apply to the entries produced by their own peering.
func TestApplyGatewayExposeExclusionsScopedToPeering(t *testing.T) {
	matrix, eps := exclusionMatrixFixture()

	applyGatewayExposeExclusions(matrix, map[string]map[string][]netip.Prefix{
		"some-other-peering": {"vpc-04": {netip.MustParsePrefix("10.10.4.2/32")}},
	})

	if got := matrix.Lookup(eps["server-3"], eps["server-4"], ProtoPort{}).Verdict; got != VerdictAllow {
		t.Errorf("server-3 -> server-4: expected %s, got %s", VerdictAllow, got)
	}
}
