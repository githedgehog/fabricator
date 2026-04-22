// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package connmatrix

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func makeEndpoint(server, subnet, ip string, hostBGP bool) Endpoint {
	return Endpoint{
		Server:  server,
		Subnet:  subnet,
		IP:      netip.MustParseAddr(ip),
		HostBGP: hostBGP,
	}
}

func TestConnectivityMatrixGet(t *testing.T) {
	a := makeEndpoint("s1", "vpc-1/default", "10.0.1.1", false)
	b := makeEndpoint("s2", "vpc-2/default", "10.0.2.1", false)
	m := NewConnectivityMatrix([]Endpoint{a, b})

	require.Empty(t, m.Get(a.Key(), b.Key()), "empty matrix: no entries")

	allow := ConnectivityExpectation{
		Source: a.Key(), Destination: b.Key(),
		Verdict: VerdictAllow, Direction: DirectionForward,
		Reason: ReachabilityReasonSwitchPeering,
	}
	m.Add(allow)
	require.Len(t, m.Get(a.Key(), b.Key()), 1)
	require.Empty(t, m.Get(b.Key(), a.Key()), "other direction not populated")
}

func TestConnectivityMatrixGetForProtoFallback(t *testing.T) {
	a := makeEndpoint("s1", "vpc-1/default", "10.0.1.1", false)
	b := makeEndpoint("s2", "vpc-2/default", "10.0.2.1", false)
	m := NewConnectivityMatrix([]Endpoint{a, b})

	// No entry at all → implicit DENY.
	got := m.GetForProto(a.Key(), b.Key(), ProtoPort{Protocol: "tcp", Port: 80})
	require.Equal(t, VerdictDeny, got.Verdict)
	require.Equal(t, ReachabilityReasonImplicitDeny, got.Reason)

	// General ALLOW entry → falls back to it for any proto.
	general := ConnectivityExpectation{
		Source: a.Key(), Destination: b.Key(),
		Verdict: VerdictAllow, Reason: ReachabilityReasonSwitchPeering,
	}
	m.Add(general)
	got = m.GetForProto(a.Key(), b.Key(), ProtoPort{Protocol: "tcp", Port: 80})
	require.Equal(t, VerdictAllow, got.Verdict)
	require.Nil(t, got.ProtoPort, "fallback entry has nil ProtoPort")

	// Proto-specific DENY wins over general ALLOW for matching proto.
	pp := ProtoPort{Protocol: "tcp", Port: 22}
	specific := ConnectivityExpectation{
		Source: a.Key(), Destination: b.Key(),
		Verdict: VerdictDeny, ProtoPort: &pp,
	}
	m.Add(specific)
	got = m.GetForProto(a.Key(), b.Key(), pp)
	require.Equal(t, VerdictDeny, got.Verdict)
	require.NotNil(t, got.ProtoPort)
	require.Equal(t, pp, *got.ProtoPort)

	// Non-matching proto still hits the general ALLOW.
	got = m.GetForProto(a.Key(), b.Key(), ProtoPort{Protocol: "tcp", Port: 443})
	require.Equal(t, VerdictAllow, got.Verdict)
}

func TestConnectivityMatrixEndpointsSorted(t *testing.T) {
	c := makeEndpoint("s3", "vpc-3/default", "10.0.3.1", false)
	a := makeEndpoint("s1", "vpc-1/default", "10.0.1.1", false)
	b := makeEndpoint("s2", "vpc-2/default", "10.0.2.1", false)
	m := NewConnectivityMatrix([]Endpoint{c, a, b})
	got := m.Endpoints()
	require.Equal(t, []string{"s1@vpc-1/default", "s2@vpc-2/default", "s3@vpc-3/default"},
		[]string{got[0].Key().String(), got[1].Key().String(), got[2].Key().String()})
}

func TestPickExpectation(t *testing.T) {
	src := EndpointKey{Server: "s1", Subnet: "vpc-1/default"}
	dst := EndpointKey{Server: "s2", Subnet: "vpc-2/default"}

	t.Run("empty returns implicit deny", func(t *testing.T) {
		got := PickExpectation(nil)
		require.Equal(t, VerdictDeny, got.Verdict)
		require.Equal(t, ReachabilityReasonImplicitDeny, got.Reason)
	})

	t.Run("first proto-agnostic ALLOW wins", func(t *testing.T) {
		pp := ProtoPort{Protocol: "tcp", Port: 22}
		entries := []ConnectivityExpectation{
			{Source: src, Destination: dst, Verdict: VerdictAllow, ProtoPort: &pp},
			{Source: src, Destination: dst, Verdict: VerdictAllow, Reason: ReachabilityReasonSwitchPeering},
		}
		got := PickExpectation(entries)
		require.Equal(t, VerdictAllow, got.Verdict)
		require.Nil(t, got.ProtoPort, "proto-specific entries are skipped")
	})

	t.Run("no proto-agnostic ALLOW falls back to implicit deny", func(t *testing.T) {
		pp := ProtoPort{Protocol: "tcp", Port: 22}
		entries := []ConnectivityExpectation{
			{Source: src, Destination: dst, Verdict: VerdictAllow, ProtoPort: &pp},
		}
		got := PickExpectation(entries)
		require.Equal(t, VerdictDeny, got.Verdict)
	})
}

func TestEndpointKeyString(t *testing.T) {
	require.Equal(t, "s1@vpc-1/default", (EndpointKey{Server: "s1", Subnet: "vpc-1/default"}).String())
	require.Equal(t, "external-1", (EndpointKey{Server: "external-1"}).String())
}
