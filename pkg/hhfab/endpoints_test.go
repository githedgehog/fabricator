// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// serverEP is a small helper for building a server-only Endpoint.
func serverEP(name, vpc, subnet, addr string) *Endpoint {
	return &Endpoint{
		Server: &ServerEndpoint{
			Name:   name,
			VPC:    vpc,
			Subnet: subnet,
			IP:     netip.MustParseAddr(addr),
		},
	}
}

func TestReplaceServerEndpoints_InPlaceUpdate(t *testing.T) {
	// Same (vpc, subnet) → mutate IP in place; pointer identity preserved
	// so matrix entries referencing it stay valid (the DHCP-test case).
	m := NewConnectivityMatrix()
	ep := serverEP("server-5", "vpc-1", "default", "10.0.1.5")
	other := serverEP("server-3", "vpc-1", "default", "10.0.1.3")
	m.AllEndpoints = []*Endpoint{ep, other}

	allow := ConnectivityExpectation{
		Pair:    EndpointPair{Source: other, Destination: ep},
		Verdict: VerdictAllow,
		Reason:  ReachabilityReasonIntraVPC,
	}
	m.Add(allow)

	newEP := serverEP("server-5", "vpc-1", "default", "10.0.1.99")
	m.ReplaceServerEndpoints("server-5", []*Endpoint{newEP})

	require.Len(t, m.AllEndpoints, 2)
	require.Same(t, ep, m.AllEndpoints[0], "existing pointer should be preserved")
	require.Equal(t, netip.MustParseAddr("10.0.1.99"), ep.Server.IP, "IP should be updated in place")

	got := m.Lookup(other, ep, ProtoPort{})
	require.Equal(t, VerdictAllow, got.Verdict, "entry keyed on the preserved pointer should still resolve")
}

func TestReplaceServerEndpoints_VPCMove(t *testing.T) {
	// (vpc, subnet) changed → old pointer dropped, new appended; entries
	// involving the old pointer are wiped (the overlap-NAT case).
	m := NewConnectivityMatrix()
	donor := serverEP("server-1", "vpc-donor", "default", "10.0.1.1")
	other := serverEP("server-2", "vpc-x", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{donor, other}
	m.Add(ConnectivityExpectation{
		Pair:    EndpointPair{Source: donor, Destination: other},
		Verdict: VerdictAllow,
	})

	overlap := serverEP("server-1", "vpc-overlap", "overlap-sub", "10.99.0.1")
	m.ReplaceServerEndpoints("server-1", []*Endpoint{overlap})

	require.Len(t, m.AllEndpoints, 2)
	require.NotContains(t, m.AllEndpoints, donor, "donor pointer should be dropped")
	require.Contains(t, m.AllEndpoints, overlap, "overlap pointer should be appended")

	// Entry referenced the dropped pointer → should be gone.
	got := m.Lookup(donor, other, ProtoPort{})
	require.Equal(t, VerdictDeny, got.Verdict, "default-deny after stale entry pruned")
}

func TestReplaceServerEndpoints_PreservesOtherServerEntries(t *testing.T) {
	m := NewConnectivityMatrix()
	moved := serverEP("server-1", "vpc-donor", "default", "10.0.1.1")
	a := serverEP("server-2", "vpc-x", "default", "10.0.2.2")
	b := serverEP("server-3", "vpc-x", "default", "10.0.2.3")
	m.AllEndpoints = []*Endpoint{moved, a, b}

	m.Add(ConnectivityExpectation{
		Pair:    EndpointPair{Source: a, Destination: b},
		Verdict: VerdictAllow,
		Reason:  ReachabilityReasonIntraVPC,
	})

	overlap := serverEP("server-1", "vpc-overlap", "overlap-sub", "10.99.0.1")
	m.ReplaceServerEndpoints("server-1", []*Endpoint{overlap})

	got := m.Lookup(a, b, ProtoPort{})
	require.Equal(t, VerdictAllow, got.Verdict, "unrelated entries should be untouched")
}

func TestReplaceServerEndpoints_MultiAttachment(t *testing.T) {
	// Server with two attachments: vpc-a unchanged → mutate in place;
	// vpc-b dropped from cluster → prune; vpc-c new → append.
	m := NewConnectivityMatrix()
	epA := serverEP("server-1", "vpc-a", "default", "10.0.1.1")
	epB := serverEP("server-1", "vpc-b", "default", "10.0.2.1")
	peer := serverEP("server-2", "vpc-a", "default", "10.0.1.2")
	m.AllEndpoints = []*Endpoint{epA, epB, peer}

	m.Add(ConnectivityExpectation{
		Pair: EndpointPair{Source: epA, Destination: peer}, Verdict: VerdictAllow,
	})
	m.Add(ConnectivityExpectation{
		Pair: EndpointPair{Source: epB, Destination: peer}, Verdict: VerdictAllow,
	})

	newA := serverEP("server-1", "vpc-a", "default", "10.0.1.99")
	newC := serverEP("server-1", "vpc-c", "default", "10.0.3.1")
	m.ReplaceServerEndpoints("server-1", []*Endpoint{newA, newC})

	require.Same(t, epA, m.AllEndpoints[0], "vpc-a pointer kept")
	require.Equal(t, netip.MustParseAddr("10.0.1.99"), epA.Server.IP)
	require.NotContains(t, m.AllEndpoints, epB, "vpc-b dropped")
	require.Contains(t, m.AllEndpoints, newC, "vpc-c appended")

	require.Equal(t, VerdictAllow, m.Lookup(epA, peer, ProtoPort{}).Verdict, "epA's entry preserved")
	require.Equal(t, VerdictDeny, m.Lookup(epB, peer, ProtoPort{}).Verdict, "epB's entry pruned")
}

func TestReplaceServerEndpoints_EmptyNewEPsRemovesAll(t *testing.T) {
	m := NewConnectivityMatrix()
	a := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	b := serverEP("server-2", "vpc-1", "default", "10.0.1.2")
	m.AllEndpoints = []*Endpoint{a, b}
	m.Add(ConnectivityExpectation{
		Pair: EndpointPair{Source: a, Destination: b}, Verdict: VerdictAllow,
	})

	m.ReplaceServerEndpoints("server-1", nil)

	require.Len(t, m.AllEndpoints, 1)
	require.Same(t, b, m.AllEndpoints[0])
	require.Equal(t, VerdictDeny, m.Lookup(a, b, ProtoPort{}).Verdict)
}

func TestReplaceServerEndpoints_AppendOnEmptyMatrix(t *testing.T) {
	m := NewConnectivityMatrix()
	newEP := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	m.ReplaceServerEndpoints("server-1", []*Endpoint{newEP})
	require.Equal(t, []*Endpoint{newEP}, m.AllEndpoints)
}

func TestReplaceServerEndpoints_NilMatrix(t *testing.T) {
	var m *ConnectivityMatrix
	require.NotPanics(t, func() {
		m.ReplaceServerEndpoints("server-1", []*Endpoint{serverEP("server-1", "vpc-1", "default", "10.0.1.1")})
	})
}

func TestReplaceServerEndpoints_IgnoresOtherServerNamesInNewEPs(t *testing.T) {
	// Defensive: if the caller mixes endpoints for a different server,
	// they should not match against existing entries for `name`.
	m := NewConnectivityMatrix()
	a := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	m.AllEndpoints = []*Endpoint{a}

	stray := serverEP("server-2", "vpc-1", "default", "10.0.1.2")
	m.ReplaceServerEndpoints("server-1", []*Endpoint{stray})

	// server-1's only endpoint had no match → dropped. stray is appended
	// (the function's documented "no-op for the matching pass" behavior).
	require.NotContains(t, m.AllEndpoints, a)
	require.Contains(t, m.AllEndpoints, stray)
}

func TestReplaceServerDrops(t *testing.T) {
	m := NewConnectivityMatrix()
	m.dropped = []DroppedEndpoint{
		{Server: "server-1", VPC: "vpc-old", Subnet: "default", Reason: "stale"},
		{Server: "server-2", VPC: "vpc-x", Subnet: "default", Reason: "other server's drop"},
	}

	// Rebind of server-1 finds no drops → only server-2's survives.
	m.ReplaceServerDrops("server-1", nil)
	require.Len(t, m.dropped, 1)
	require.Equal(t, "server-2", m.dropped[0].Server)

	// A fresh drop for server-1 is recorded; entries for other servers in
	// the passed slice are ignored (they belong to their own sweep).
	m.ReplaceServerDrops("server-1", []DroppedEndpoint{
		{Server: "server-1", VPC: "vpc-new", Subnet: "default", Reason: "fresh"},
		{Server: "server-3", VPC: "vpc-y", Subnet: "default", Reason: "not ours"},
	})
	require.Len(t, m.dropped, 2)
	require.Equal(t, "vpc-new", m.dropped[1].VPC)
}

func TestValidate(t *testing.T) {
	a := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	b := serverEP("server-2", "vpc-2", "default", "10.0.2.1")
	ext := &Endpoint{External: &ExternalEndpoint{ExternalName: "ext-1"}}
	ext2 := &Endpoint{External: &ExternalEndpoint{ExternalName: "ext-2"}}

	newMatrix := func() *ConnectivityMatrix {
		m := NewConnectivityMatrix()
		m.AllEndpoints = []*Endpoint{a, b, ext, ext2}

		return m
	}

	t.Run("all-deny topology is valid", func(t *testing.T) {
		// Isolated VPCs with no peerings: no Allow entry anywhere is a
		// legitimate thing to assert, not a degenerate matrix.
		require.NoError(t, newMatrix().Validate())
	})

	t.Run("no endpoints", func(t *testing.T) {
		require.ErrorContains(t, NewConnectivityMatrix().Validate(), "no endpoints")
	})

	t.Run("nil matrix", func(t *testing.T) {
		var m *ConnectivityMatrix
		require.Error(t, m.Validate())
	})

	t.Run("discovery drop", func(t *testing.T) {
		m := newMatrix()
		m.dropped = []DroppedEndpoint{{
			Server: "server-3", VPC: "vpc-3", Subnet: "default", Reason: "attachment has no matching address",
		}}
		require.ErrorContains(t, m.Validate(), "server-3 (vpc-3/default): attachment has no matching address")
	})

	t.Run("unevaluated server pair", func(t *testing.T) {
		m := newMatrix()
		m.Add(ConnectivityExpectation{
			Pair:    EndpointPair{Source: a, Destination: b},
			Verdict: VerdictUnknown,
			Detail:  "gw peering with non-empty expose 'As'",
		})
		err := m.Validate()
		require.ErrorContains(t, err, "server-1(vpc-1/default) → server-2(vpc-2/default)")
		require.ErrorContains(t, err, "non-empty expose 'As'")

		// A test that overlays the real expectation clears it.
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: b}, Verdict: VerdictAllow,
		})
		require.NoError(t, m.Validate())
	})

	t.Run("unevaluated external is settled by another external's allow", func(t *testing.T) {
		// The external oracle ORs over every External in the cluster, so
		// one Allow decides the source's expectation for all of them.
		m := newMatrix()
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: ext}, Verdict: VerdictUnknown, Detail: "unsupported",
		})
		require.ErrorContains(t, m.Validate(), "external:ext-1")

		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: ext2}, Verdict: VerdictAllow,
		})
		require.NoError(t, m.Validate())

		// ...but only for that source.
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: b, Destination: ext}, Verdict: VerdictUnknown, Detail: "unsupported",
		})
		require.ErrorContains(t, m.Validate(), "server-2(vpc-2/default) → external:ext-1")
	})
}

func TestReplaceServerEndpoints_PreservesHostBGP(t *testing.T) {
	m := NewConnectivityMatrix()
	ep := &Endpoint{Server: &ServerEndpoint{
		Name: "server-1", VPC: "vpc-1", Subnet: "default",
		HostBGP: false, IP: netip.MustParseAddr("10.0.1.1"),
	}}
	m.AllEndpoints = []*Endpoint{ep}

	newEP := &Endpoint{Server: &ServerEndpoint{
		Name: "server-1", VPC: "vpc-1", Subnet: "default",
		HostBGP: true, IP: netip.MustParseAddr("10.0.1.99"),
	}}
	m.ReplaceServerEndpoints("server-1", []*Endpoint{newEP})

	require.Same(t, ep, m.AllEndpoints[0])
	require.True(t, ep.Server.HostBGP, "HostBGP should be copied across on in-place update")
}
