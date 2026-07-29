// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// Bug reproduction for review finding 8.2 (TEST-CONNECTIVITY-MATRIX-REVIEW.md).
// isVPCSubnetPresentInPeering must not let the order of entries in expose.IPs
// change its answer: an exclusion entry and an include entry describe the same
// configuration whichever way round they are listed. Today the function returns
// on the first matching include entry, so include-then-exclusion yields
// (true, nil) while exclusion-then-include yields reachCheckUnsupported.
//
// This test is skipped so CI stays green. To run it:
//
//	go test ./pkg/hhfab/ -run TestExposeNotOrderInvariance -exposenot-order
//
// It fails today and passes once the function treats both orders alike. The
// pinned current behavior lives in expose_not_proof_test.go.
package hhfab

import (
	"flag"
	"testing"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
)

var runExposeNotOrder = flag.Bool("exposenot-order", false, "run the expose Not order-invariance bug reproduction")

// Wiring check for GwPeeringOptions.VPCxNotCIDRs: exclusions land in expose.IPs as
// standalone Not entries, after the include entries.
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

func TestExposeNotOrderInvariance(t *testing.T) {
	if !*runExposeNotOrder {
		t.Skip("known bug (review finding 8.2): pass -exposenot-order to run this reproduction")
	}

	includeFirst := []gwapi.PeeringEntryIP{
		{VPCSubnet: "default"},
		{Not: "10.0.1.128/25"},
	}
	notFirst := []gwapi.PeeringEntryIP{
		{Not: "10.0.1.128/25"},
		{VPCSubnet: "default"},
	}

	peering, vpc := exposeNotFixture(includeFirst)
	presentA, errA := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")

	peering, vpc = exposeNotFixture(notFirst)
	presentB, errB := isVPCSubnetPresentInPeering(peering, vpc, "vpc-1", "default")

	if (errA == nil) != (errB == nil) {
		t.Fatalf("order changed the error outcome: include-first err=%v, not-first err=%v", errA, errB)
	}
	if presentA != presentB {
		t.Fatalf("order changed the verdict: include-first present=%v, not-first present=%v", presentA, presentB)
	}
}
