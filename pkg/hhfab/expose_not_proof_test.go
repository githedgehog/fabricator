// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// Companion proof for review finding 8.2 (TEST-CONNECTIVITY-MATRIX-REVIEW.md):
// isVPCSubnetPresentInPeering's handling of expose `Not` exclusion entries is
// order-dependent. Per the API (gatewaypeering_types.go: "only one of cidr,
// not, vpcSubnet can be set"), a Not exclusion is a standalone entry in
// expose.IPs alongside include entries. The function returns on the first
// include entry that matches the queried subnet, so:
//   - include-before-Not  -> (true, nil): the exclusion is silently ignored
//   - Not-before-include  -> (false, reachCheckUnsupported error)
//
// Same semantic configuration, opposite verdicts, decided by list order.
// Not a test of desired behavior — a pin of current (buggy) behavior.
package hhfab

import (
	"errors"
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

func TestExposeNotExclusionIgnoredWhenIncludeComesFirst(t *testing.T) {
	peering, vpc := exposeNotFixture([]gwapi.PeeringEntryIP{
		{VPCSubnet: "default"},
		{Not: "10.0.1.128/25"}, // excludes half the subnet — never consulted
	})

	present, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !present {
		t.Fatalf("expected present=true (current behavior)")
	}
	// present=true with zero acknowledgement of the Not entry: every address in
	// 10.0.1.128/25 is expected reachable even though the expose excludes it.
}

func TestExposeNotExclusionRejectedWhenItComesFirst(t *testing.T) {
	peering, vpc := exposeNotFixture([]gwapi.PeeringEntryIP{
		{Not: "10.0.1.128/25"}, // same config, Not listed first
		{VPCSubnet: "default"},
	})

	_, err := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")
	if err == nil {
		t.Fatalf("expected reachCheckUnsupported error (current behavior), got nil")
	}
	if !errors.Is(err, reachCheckUnsupported) {
		t.Fatalf("expected reachCheckUnsupported, got: %v", err)
	}
	// Identical semantics to the test above, opposite outcome: order decides.
}
