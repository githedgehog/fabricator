// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"net/netip"
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

func TestCheckProbesEnabled(t *testing.T) {
	m := NewConnectivityMatrix()
	a := serverEP("server-1", "vpc-1", "default", "10.0.1.1")
	b := serverEP("server-2", "vpc-2", "default", "10.0.2.2")
	m.AllEndpoints = []*Endpoint{a, b}

	// populate stores nothing for a pair it denies, so an all-deny topology has
	// no entries at all — but the phase still probes every pair off the
	// synthesized Deny, and with both probes off every one would pass unasserted.
	require.Empty(t, m.entries)
	require.ErrorContains(t, m.checkProbesEnabled(TestConnectivityOpts{CurlsCount: 1}), "2 server-to-server entries")
	require.NoError(t, m.checkProbesEnabled(TestConnectivityOpts{PingsCount: 1}))
	require.NoError(t, m.checkProbesEnabled(TestConnectivityOpts{IPerfsSeconds: 3}))

	// The --source/--destination filters gate pairs on top of ownership: the
	// first leaves only server-1 → server-2, the second only the self-pair, which
	// no phase probes.
	require.ErrorContains(t, m.checkProbesEnabled(TestConnectivityOpts{
		CurlsCount: 1, Sources: []string{"server-1"},
	}), "1 server-to-server entries")
	require.NoError(t, m.checkProbesEnabled(TestConnectivityOpts{
		CurlsCount: 1, Sources: []string{"server-1"}, Destinations: []string{"server-1"},
	}))

	// An icmp proto-port entry has no probe but ping, so iperfs alone cannot
	// assert it.
	m.Add(ConnectivityExpectation{
		Pair:      EndpointPair{Source: a, Destination: b},
		Verdict:   VerdictAllow,
		ProtoPort: ProtoPort{Protocol: "icmp"},
	})
	require.ErrorContains(t, m.checkProbesEnabled(TestConnectivityOpts{IPerfsSeconds: 3}), "1 icmp proto-port entries")
	require.NoError(t, m.checkProbesEnabled(TestConnectivityOpts{PingsCount: 1}))

	// External destinations are only ever probed by curl.
	m.AllEndpoints = append(m.AllEndpoints, &Endpoint{External: &ExternalEndpoint{ExternalName: "ext-1"}})
	require.ErrorContains(t, m.checkProbesEnabled(TestConnectivityOpts{PingsCount: 1}), "2 external entries")
	require.NoError(t, m.checkProbesEnabled(TestConnectivityOpts{PingsCount: 1, CurlsCount: 1}))

	// an external destination carries no server name, so --destination cannot
	// exclude it — both sources still curl it
	require.ErrorContains(t, m.checkProbesEnabled(TestConnectivityOpts{
		PingsCount: 1, Destinations: []string{"server-2"},
	}), "2 external entries")
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

	t.Run("unevaluated default entry shadowed by proto-port entries", func(t *testing.T) {
		// Proto-scoped pairs are probed only by runMatrixProtoPortPhase, which
		// never reads the default entry, so an ACL test overlaying just the
		// proto verdicts leaves nothing unasserted.
		m := newMatrix()
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: b}, Verdict: VerdictUnknown, Detail: "unsupported",
		})
		require.ErrorContains(t, m.Validate(), "server-1(vpc-1/default) → server-2(vpc-2/default)")

		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: b}, Verdict: VerdictAllow,
			ProtoPort: ProtoPort{Protocol: "tcp", Port: 5301},
		})
		require.NoError(t, m.Validate())

		// ...unless the default entry is a port-forward, which the
		// port-forward phase does read.
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: b}, Verdict: VerdictUnknown, Detail: "unsupported",
			NAT: &TranslatedAddress{DestinationIP: netip.MustParseAddr("10.0.2.1"), DestinationPort: 15201},
		})
		require.ErrorContains(t, m.Validate(), "server-1(vpc-1/default) → server-2(vpc-2/default)")
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

		// A DNAT-only Allow grants no egress, so it doesn't settle the Unknown,
		// or Validate would wave through an entry the curl phase goes on to
		// assert as denied.
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: b, Destination: ext2}, Verdict: VerdictAllow,
			NAT: &TranslatedAddress{DestinationIP: netip.MustParseAddr("10.0.2.1"), DestinationPort: 8080},
		})
		require.ErrorContains(t, m.Validate(), "server-2(vpc-2/default) → external:ext-1")

		// An unscoped, non-DNAT Allow does.
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: b, Destination: ext2}, Verdict: VerdictAllow,
		})
		require.NoError(t, m.Validate())

		// ...but it settles only the curl expectation. A port-forward entry is
		// read by its own phase, which has its own verdict to assert.
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: b, Destination: ext}, Verdict: VerdictUnknown, Detail: "unsupported",
			NAT: &TranslatedAddress{DestinationIP: netip.MustParseAddr("10.99.0.1"), DestinationPort: 8080},
		})
		err := m.Validate()
		require.ErrorContains(t, err, "could not be evaluated")
		require.ErrorContains(t, err, "server-2(vpc-2/default) → external:ext-1")
	})

	t.Run("entries no phase can read", func(t *testing.T) {
		// runMatrixProtoPortPhase probes server destinations only and the curl
		// probe is untargeted, so this expectation asserts nothing.
		m := newMatrix()
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: ext}, Verdict: VerdictAllow,
			ProtoPort: ProtoPort{Protocol: "tcp", Port: 8080},
		})
		err := m.Validate()
		require.ErrorContains(t, err, "no probe phase can read")
		require.ErrorContains(t, err, "server-1(vpc-1/default) → external:ext-1 [tcp/8080]")

		// A translated port with no address to aim it at is unprobeable too.
		m = newMatrix()
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: b}, Verdict: VerdictAllow,
			NAT: &TranslatedAddress{DestinationPort: 15201},
		})
		require.ErrorContains(t, m.Validate(), "server-1(vpc-1/default) → server-2(vpc-2/default)")

		// So is a proto-scoped entry that inherited a port-forward: the proto
		// probe would dial 5301 on the NAT address instead of the mapped port.
		m = newMatrix()
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: b}, Verdict: VerdictAllow,
			ProtoPort: ProtoPort{Protocol: "tcp", Port: 5301},
			NAT:       &TranslatedAddress{DestinationIP: netip.MustParseAddr("10.99.0.1"), DestinationPort: 15201},
		})
		err = m.Validate()
		require.ErrorContains(t, err, "no probe phase can read")
		require.ErrorContains(t, err, "server-1(vpc-1/default) → server-2(vpc-2/default) [tcp/5301]")

		// An external Allow behind a NAT with no source pool is a contradiction:
		// the curl oracle discards it and asserts the source cannot get out.
		m = newMatrix()
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: ext}, Verdict: VerdictAllow,
			NAT: &TranslatedAddress{DestinationIP: netip.MustParseAddr("10.99.0.1")},
		})
		err = m.Validate()
		require.ErrorContains(t, err, "no probe phase can read")
		require.ErrorContains(t, err, "server-1(vpc-1/default) → external:ext-1")

		// A masquerade pool makes it assertable again.
		m.Add(ConnectivityExpectation{
			Pair: EndpointPair{Source: a, Destination: ext}, Verdict: VerdictAllow,
			NAT: &TranslatedAddress{SourcePool: netip.MustParsePrefix("10.99.0.0/24")},
		})
		require.NoError(t, m.Validate())
	})
}

func TestNATTestProbeServers(t *testing.T) {
	for _, test := range []struct {
		name     string
		eps      []*Endpoint
		expected []string
	}{
		{
			name: "no VPC outside the tested pair leaves no control server",
			eps: []*Endpoint{
				serverEP("server-1", "vpc-1", "default", "10.0.1.1"),
				serverEP("server-2", "vpc-2", "default", "10.0.2.2"),
			},
			expected: []string{"server-1", "server-2"},
		},
		{
			name: "control server does not depend on endpoint order",
			eps: []*Endpoint{
				serverEP("server-4", "vpc-4", "default", "10.0.4.4"),
				serverEP("server-2", "vpc-2", "default", "10.0.2.2"),
				serverEP("server-3", "vpc-3", "default", "10.0.3.3"),
				serverEP("server-1", "vpc-1", "default", "10.0.1.1"),
			},
			expected: []string{"server-1", "server-2", "server-3"},
		},
		{
			name: "a server also attached outside the tested pair is not the control",
			eps: []*Endpoint{
				serverEP("server-1", "vpc-1", "default", "10.0.1.1"),
				serverEP("server-2", "vpc-2", "default", "10.0.2.2"),
				serverEP("server-2", "vpc-3", "default", "10.0.3.2"),
				serverEP("server-3", "vpc-3", "default", "10.0.3.3"),
			},
			expected: []string{"server-1", "server-2", "server-3"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewConnectivityMatrix()
			m.AllEndpoints = test.eps
			require.Equal(t, test.expected, natTestProbeServers(m, "vpc-1", "vpc-2"))
		})
	}
}
