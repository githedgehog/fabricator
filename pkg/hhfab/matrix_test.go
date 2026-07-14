// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtoPortEntries_ExcludesDefaultAndSorts(t *testing.T) {
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	// A default (ProtoPort{}) entry must be excluded from the proto list.
	m.Add(ConnectivityExpectation{
		Pair:    EndpointPair{Source: src, Destination: dst},
		Verdict: VerdictAllow,
	})
	// Add out of order to prove sorting by (Protocol, Port).
	m.Add(ConnectivityExpectation{
		Pair:      EndpointPair{Source: src, Destination: dst},
		Verdict:   VerdictDeny,
		ProtoPort: ProtoPort{Protocol: "udp", Port: 5201},
	})
	m.Add(ConnectivityExpectation{
		Pair:      EndpointPair{Source: src, Destination: dst},
		Verdict:   VerdictAllow,
		ProtoPort: ProtoPort{Protocol: "tcp", Port: 6201},
	})
	m.Add(ConnectivityExpectation{
		Pair:      EndpointPair{Source: src, Destination: dst},
		Verdict:   VerdictAllow,
		ProtoPort: ProtoPort{Protocol: "tcp", Port: 5201},
	})

	got := m.ProtoPortEntries(src, dst)
	require.Len(t, got, 3, "default entry excluded")
	require.Equal(t, ProtoPort{Protocol: "tcp", Port: 5201}, got[0].ProtoPort)
	require.Equal(t, ProtoPort{Protocol: "tcp", Port: 6201}, got[1].ProtoPort)
	require.Equal(t, ProtoPort{Protocol: "udp", Port: 5201}, got[2].ProtoPort)
	require.Equal(t, VerdictDeny, got[2].Verdict)
}

func TestProtoPortEntries_NoneForUnknownPair(t *testing.T) {
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	require.Nil(t, m.ProtoPortEntries(src, dst))
	require.False(t, m.HasProtoPortEntries(src, dst))
}

func TestHasProtoPortEntries_IgnoresDefaultOnly(t *testing.T) {
	m := NewConnectivityMatrix()
	src := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	dst := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{src, dst}

	// A pair with only a default entry must NOT be treated as proto-scoped,
	// so the legacy server-server phase keeps owning it.
	m.Add(ConnectivityExpectation{
		Pair:    EndpointPair{Source: src, Destination: dst},
		Verdict: VerdictAllow,
	})
	require.False(t, m.HasProtoPortEntries(src, dst))

	// Once a non-zero ProtoPort entry lands, the pair is proto-scoped and the
	// legacy phase gate (which keys on this) routes it to the proto-port phase.
	m.Add(ConnectivityExpectation{
		Pair:      EndpointPair{Source: src, Destination: dst},
		Verdict:   VerdictAllow,
		ProtoPort: ProtoPort{Protocol: "tcp", Port: 5201},
	})
	require.True(t, m.HasProtoPortEntries(src, dst))
}

func TestParseNCReturnCode(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		wantRC int
		wantOk bool
	}{
		{"connect ok", "NCRC=0\n", 0, true},
		{"refused/timeout", "nc: connect failed\nNCRC=1\n", 1, true},
		{"not found", "bash: nc: command not found\nNCRC=127\n", 127, true},
		{"marker with spaces", "  NCRC=1  \n", 1, true},
		{"no marker (probe did not complete)", "some ssh noise\n", 0, false},
		{"empty", "", 0, false},
		{"non-numeric marker", "NCRC=oops\n", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc, ok := parseNCReturnCode(tc.stdout)
			require.Equal(t, tc.wantOk, ok)
			if tc.wantOk {
				require.Equal(t, tc.wantRC, rc)
			}
		})
	}
}

func TestSetVPCToVPCProtoVerdict_AccumulatesScopesAndPreservesPeering(t *testing.T) {
	m := NewConnectivityMatrix()
	a1 := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	a2 := serverEP("server-2", "vpc-1", "default", "10.0.1.2")
	b1 := serverEP("server-3", "vpc-2", "default", "10.0.2.1")
	c1 := serverEP("server-4", "vpc-3", "default", "10.0.3.1")
	m.AllEndpoints = []*Endpoint{a1, a2, b1, c1}

	// Seed a default entry so we can prove Peering is preserved onto the
	// proto entries.
	m.Add(ConnectivityExpectation{
		Pair:    EndpointPair{Source: a1, Destination: b1},
		Verdict: VerdictAllow,
		Peering: "vpc-1--vpc-2",
	})

	setVPCToVPCProtoVerdict(m, "vpc-1", "vpc-2", ProtoPort{Protocol: "tcp", Port: 5201}, VerdictAllow)
	setVPCToVPCProtoVerdict(m, "vpc-1", "vpc-2", ProtoPort{Protocol: "udp", Port: 5201}, VerdictDeny)

	// Both protocols coexist on the same pair.
	tcp := m.Lookup(a1, b1, ProtoPort{Protocol: "tcp", Port: 5201})
	udp := m.Lookup(a1, b1, ProtoPort{Protocol: "udp", Port: 5201})
	require.Equal(t, VerdictAllow, tcp.Verdict)
	require.Equal(t, VerdictDeny, udp.Verdict)
	require.Equal(t, "vpc-1--vpc-2", tcp.Peering, "existing peering preserved")
	require.Equal(t, ReachabilityReasonGatewayPeering, tcp.Reason)

	// Applied to every source in vpc-1 (a2 too), not just a1.
	require.Equal(t, VerdictAllow, m.Lookup(a2, b1, ProtoPort{Protocol: "tcp", Port: 5201}).Verdict)

	// Not applied to a server outside the destination VPC.
	require.False(t, m.HasProtoPortEntries(a1, c1), "vpc-3 destination untouched")
}
