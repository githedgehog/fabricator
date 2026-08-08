// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"errors"
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
			present, excluded, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if !present {
				t.Fatalf("expected the subnet to be reported as exposed")
			}
			if len(excluded) != 1 || excluded[0] != netip.MustParsePrefix("10.0.1.128/25") {
				t.Fatalf("expected the exclusion to be reported, got %v", excluded)
			}
		})
	}
}

// An exclusion belongs to the expose that lists it, so it must not be reported
// for a subnet exposed by a different entry of the same peering.
func TestExposeNotScopedToItsOwnExpose(t *testing.T) {
	peering := &gwapi.PeeringEntry{
		Expose: []gwapi.PeeringEntryExpose{
			{IPs: []gwapi.PeeringEntryIP{
				{VPCSubnet: "other"},
				{Not: "10.0.2.5/32"},
			}},
			{IPs: []gwapi.PeeringEntryIP{
				{VPCSubnet: "default"},
			}},
		},
	}
	vpc := gwapi.VPCInfo{
		Spec: gwapi.VPCInfoSpec{
			Subnets: map[string]*gwapi.VPCInfoSubnet{
				"default": {CIDR: "10.0.1.0/24"},
				"other":   {CIDR: "10.0.2.0/24"},
			},
		},
	}

	present, excluded, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !present {
		t.Fatalf("expected the subnet to be reported as exposed")
	}
	if len(excluded) != 0 {
		t.Fatalf("expected no exclusion for subnet default, got %v", excluded)
	}
}

// An expose entry setting none of cidr/not/vpcSubnet is a malformed peering, not
// a check the helper does not support yet.
func TestExposeEntryWithoutAnyFieldIsAnError(t *testing.T) {
	peering, vpc := exposeNotFixture([]gwapi.PeeringEntryIP{{}})

	_, _, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
	if err == nil {
		t.Fatalf("expected an error for a malformed expose entry")
	}
	if errors.Is(err, reachCheckUnsupported) {
		t.Fatalf("expected a plain error, got %v", err)
	}
}

// A peering entry carrying only an exclusion exposes nothing.
func TestExposeNotAloneExposesNothing(t *testing.T) {
	peering, vpc := exposeNotFixture([]gwapi.PeeringEntryIP{
		{Not: "10.0.1.128/25"},
	})

	present, excluded, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if present {
		t.Fatalf("expected the subnet not to be reported as exposed")
	}
	if len(excluded) != 0 {
		t.Fatalf("expected no exclusion for an unexposed subnet, got %v", excluded)
	}
}

func TestPickUnusedHostAddress(t *testing.T) {
	for _, tc := range []struct {
		prefix  string
		used    []string
		want    string
		wantErr bool
	}{
		{prefix: "10.0.1.0/24", want: "10.0.1.1"},
		{prefix: "10.0.1.0/24", used: []string{"10.0.1.1", "10.0.1.2"}, want: "10.0.1.3"},
		// A /0 must stay bounded: the host count does not fit in a uint32.
		{prefix: "0.0.0.0/0", want: "0.0.0.1"},
		{prefix: "10.0.1.0/31", wantErr: true},
		{prefix: "2001:db8::/64", wantErr: true},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			used := map[netip.Addr]bool{}
			for _, addr := range tc.used {
				used[netip.MustParseAddr(addr)] = true
			}

			got, err := pickUnusedHostAddress(netip.MustParsePrefix(tc.prefix), used)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", got)
				}

				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tc.want {
				t.Errorf("expected %s, got %s", tc.want, got)
			}
		})
	}
}

func exclusionEndpoints() map[string]*Endpoint {
	return map[string]*Endpoint{
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
}

// vpc-04 excludes server-4's address from its own expose, so the reachability
// check reports it on whichever side of the pair vpc-04 sits.
func exclusionReachability(sourceVPC string) Reachability {
	r := Reachability{
		Reachable: true,
		Reason:    ReachabilityReasonGatewayPeering,
		Peering:   "vpc-03--vpc-04",
	}
	excluded := []netip.Prefix{netip.MustParsePrefix("10.10.4.2/32")}
	if sourceVPC == "vpc-04" {
		r.SourceExclusions = excluded
	} else {
		r.DestExclusions = excluded
	}

	return r
}

// Excluding a host address denies that host in both directions across the
// peering, and leaves every other pair alone.
func TestEndpointVerdictExclusionIsHostScoped(t *testing.T) {
	eps := exclusionEndpoints()

	for _, tc := range []struct {
		src, dst string
		want     ConnectivityVerdict
	}{
		{"server-3", "server-4", VerdictDeny},
		{"server-4", "server-3", VerdictDeny},
		{"server-3", "server-5", VerdictAllow},
		{"server-5", "server-3", VerdictAllow},
	} {
		src, dst := eps[tc.src], eps[tc.dst]
		got := endpointVerdict(exclusionReachability(src.Server.VPC), src, dst)
		if got != tc.want {
			t.Errorf("%s -> %s: expected %s, got %s", tc.src, tc.dst, tc.want, got)
		}
	}
}

// An exclusion covering no assigned address changes nothing.
func TestEndpointVerdictUnusedExclusion(t *testing.T) {
	eps := exclusionEndpoints()
	r := Reachability{
		Reachable:      true,
		Reason:         ReachabilityReasonGatewayPeering,
		Peering:        "vpc-03--vpc-04",
		DestExclusions: []netip.Prefix{netip.MustParsePrefix("10.10.4.99/32")},
	}

	for _, src := range eps {
		for _, dst := range eps {
			if src == dst || src.Server.VPC == dst.Server.VPC {
				continue
			}
			if got := endpointVerdict(r, src, dst); got != VerdictAllow {
				t.Errorf("%s -> %s: expected %s, got %s", src.Server.Name, dst.Server.Name, VerdictAllow, got)
			}
		}
	}
}

// An external destination has no single address to match, so only the source's
// own exclusions can decide the verdict.
func TestEndpointVerdictExternalDestination(t *testing.T) {
	src := exclusionEndpoints()["server-4"]
	ext := &Endpoint{External: &ExternalEndpoint{
		ExternalName: "default",
		Prefixes:     []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	}}

	denied := Reachability{
		Reachable:        true,
		Reason:           ReachabilityReasonGatewayPeering,
		Peering:          "vpc-04--external",
		SourceExclusions: []netip.Prefix{netip.MustParsePrefix("10.10.4.2/32")},
	}
	if got := endpointVerdict(denied, src, ext); got != VerdictDeny {
		t.Errorf("server-4 -> external: expected %s, got %s", VerdictDeny, got)
	}

	ignored := Reachability{
		Reachable:      true,
		Reason:         ReachabilityReasonGatewayPeering,
		Peering:        "vpc-04--external",
		DestExclusions: []netip.Prefix{netip.MustParsePrefix("10.10.4.2/32")},
	}
	if got := endpointVerdict(ignored, src, ext); got != VerdictAllow {
		t.Errorf("server-4 -> external: expected %s, got %s", VerdictAllow, got)
	}
}
