// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package connmatrix

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
)

func TestCalculateStaticNATIP(t *testing.T) {
	for _, tc := range []struct {
		name         string
		serverIP     string
		subnetStart  string
		natPoolStart string
		want         string
		wantErr      bool
	}{
		{
			name:         "first host maps to first pool host",
			serverIP:     "10.0.1.10",
			subnetStart:  "10.0.1.0",
			natPoolStart: "10.100.0.0",
			want:         "10.100.0.10",
		},
		{
			name:         "zero offset",
			serverIP:     "10.0.1.0",
			subnetStart:  "10.0.1.0",
			natPoolStart: "10.100.0.0",
			want:         "10.100.0.0",
		},
		{
			name:         "cross-octet carry",
			serverIP:     "10.0.1.255",
			subnetStart:  "10.0.1.0",
			natPoolStart: "10.100.0.250",
			want:         "10.100.1.249",
		},
		{
			name:         "server below subnet start errors",
			serverIP:     "10.0.0.5",
			subnetStart:  "10.0.1.0",
			natPoolStart: "10.100.0.0",
			wantErr:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CalculateStaticNATIP(
				netip.MustParseAddr(tc.serverIP),
				netip.MustParseAddr(tc.subnetStart),
				netip.MustParseAddr(tc.natPoolStart),
			)
			if tc.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got.String())
		})
	}
}

func TestResolveNATTranslation(t *testing.T) {
	subnet := netip.MustParsePrefix("10.0.1.0/24")
	serverIP := netip.MustParseAddr("10.0.1.5")
	poolCIDR := "10.100.0.0/24"

	t.Run("nil NAT returns nil", func(t *testing.T) {
		got, err := resolveNATTranslation(&gwapi.PeeringEntryExpose{}, serverIP, subnet)
		require.NoError(t, err)
		require.Nil(t, got)
	})

	t.Run("NAT without As errors", func(t *testing.T) {
		expose := &gwapi.PeeringEntryExpose{
			NAT: &gwapi.PeeringNAT{Static: &gwapi.PeeringNATStatic{}},
		}
		_, err := resolveNATTranslation(expose, serverIP, subnet)
		require.Error(t, err)
	})

	t.Run("static NAT maps by offset", func(t *testing.T) {
		expose := &gwapi.PeeringEntryExpose{
			As:  []gwapi.PeeringEntryAs{{CIDR: poolCIDR}},
			NAT: &gwapi.PeeringNAT{Static: &gwapi.PeeringNATStatic{}},
		}
		got, err := resolveNATTranslation(expose, serverIP, subnet)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "10.100.0.5", got.DestinationIP.String())
		require.Equal(t, "10.100.0.5", got.SourceIP.String())
		require.Equal(t, poolCIDR, got.SourcePool.String())
	})

	t.Run("masquerade reports pool prefix for SNAT verification", func(t *testing.T) {
		expose := &gwapi.PeeringEntryExpose{
			As:  []gwapi.PeeringEntryAs{{CIDR: poolCIDR}},
			NAT: &gwapi.PeeringNAT{Masquerade: &gwapi.PeeringNATMasquerade{}},
		}
		got, err := resolveNATTranslation(expose, serverIP, subnet)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "10.100.0.0", got.SourceIP.String())
		require.Equal(t, poolCIDR, got.SourcePool.String())
		require.False(t, got.DestinationIP.IsValid(), "masquerade has no DNAT")
	})

	t.Run("port forward collects port map", func(t *testing.T) {
		expose := &gwapi.PeeringEntryExpose{
			As: []gwapi.PeeringEntryAs{{CIDR: poolCIDR}},
			NAT: &gwapi.PeeringNAT{PortForward: &gwapi.PeeringNATPortForward{
				Ports: []gwapi.PeeringNATPortForwardEntry{
					{Protocol: gwapi.PeeringNATProtocolTCP, Port: "80", As: "8080"},
					{Protocol: gwapi.PeeringNATProtocolUDP, Port: "53", As: "5353"},
				},
			}},
		}
		got, err := resolveNATTranslation(expose, serverIP, subnet)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "10.100.0.0", got.DestinationIP.String())
		require.Len(t, got.PortForwards, 2)
		require.Equal(t, ProtoPort{Protocol: "tcp", Port: 8080}, got.PortForwards[ProtoPort{Protocol: "tcp", Port: 80}])
		require.Equal(t, ProtoPort{Protocol: "udp", Port: 5353}, got.PortForwards[ProtoPort{Protocol: "udp", Port: 53}])
	})
}

// makeGWPeering constructs a minimal GatewayPeering with two sides and their
// expose entries. Each side is `vpc-<name>`, exposing its default subnet with
// the optional NAT config described by setupNAT.
func makeGWPeering(t *testing.T, name, vpcA, vpcB string, natA, natB *gwapi.PeeringNAT, asA, asB string) *gwapi.GatewayPeering {
	t.Helper()

	mkEntry := func(vpc string, nat *gwapi.PeeringNAT, as string) *gwapi.PeeringEntry {
		expose := gwapi.PeeringEntryExpose{
			IPs: []gwapi.PeeringEntryIP{{VPCSubnet: "default"}},
		}
		if nat != nil {
			expose.NAT = nat
			expose.As = []gwapi.PeeringEntryAs{{CIDR: as}}
		}

		return &gwapi.PeeringEntry{Expose: []gwapi.PeeringEntryExpose{expose}}
	}

	return &gwapi.GatewayPeering{
		Spec: gwapi.PeeringSpec{
			Peering: map[string]*gwapi.PeeringEntry{
				vpcA: mkEntry(vpcA, natA, asA),
				vpcB: mkEntry(vpcB, natB, asB),
			},
		},
	}
}

// makeVPCInfo builds a gwapi.VPCInfo with a single "default" subnet.
func makeVPCInfo(name, cidr string) *gwapi.VPCInfo {
	return &gwapi.VPCInfo{
		Spec: gwapi.VPCInfoSpec{
			Subnets: map[string]*gwapi.VPCInfoSubnet{
				"default": {CIDR: cidr},
			},
		},
	}
}

func TestBuildGatewayDirection(t *testing.T) {
	vpcA, cidrA := "vpc-a", "10.0.1.0/24"
	vpcB, cidrB := "vpc-b", "10.0.2.0/24"
	poolA := "10.100.0.0/24"
	poolB := "10.101.0.0/24"

	srvA := makeEndpoint("server-a", vpcA+"/default", "10.0.1.5", false)
	srvB := makeEndpoint("server-b", vpcB+"/default", "10.0.2.5", false)
	endpoints := map[string][]Endpoint{
		vpcA: {srvA},
		vpcB: {srvB},
	}
	infoA, infoB := makeVPCInfo(vpcA, cidrA), makeVPCInfo(vpcB, cidrB)

	// Helper: expectations from A→B for a given peering config.
	forward := func(t *testing.T, gp *gwapi.GatewayPeering) []ConnectivityExpectation {
		t.Helper()
		out, err := buildGatewayDirection("p", vpcA, vpcB,
			gp.Spec.Peering[vpcA], gp.Spec.Peering[vpcB], infoA, infoB, endpoints)
		require.NoError(t, err)

		return out
	}

	t.Run("no NAT both sides: ALLOW", func(t *testing.T) {
		gp := makeGWPeering(t, "p", vpcA, vpcB, nil, nil, "", "")
		got := forward(t, gp)
		require.Len(t, got, 1)
		require.Equal(t, VerdictAllow, got[0].Verdict)
		require.Equal(t, DirectionForward, got[0].Direction)
		require.Nil(t, got[0].NAT, "no NAT")
	})

	t.Run("masquerade on src: ALLOW forward only", func(t *testing.T) {
		gp := makeGWPeering(t, "p", vpcA, vpcB,
			&gwapi.PeeringNAT{Masquerade: &gwapi.PeeringNATMasquerade{}}, nil, poolA, "")
		got := forward(t, gp)
		require.Len(t, got, 1)
		require.Equal(t, VerdictAllow, got[0].Verdict)
		require.NotNil(t, got[0].NAT)
		require.Equal(t, "10.100.0.0", got[0].NAT.SourceIP.String(), "SNAT source pool prefix")
	})

	t.Run("masquerade on dst: DENY (skipped)", func(t *testing.T) {
		gp := makeGWPeering(t, "p", vpcA, vpcB,
			nil, &gwapi.PeeringNAT{Masquerade: &gwapi.PeeringNATMasquerade{}}, "", poolB)
		got := forward(t, gp)
		require.Empty(t, got, "dst is initiator; src cannot reach it forward")
	})

	t.Run("port-forward on src: DENY (skipped)", func(t *testing.T) {
		gp := makeGWPeering(t, "p", vpcA, vpcB,
			&gwapi.PeeringNAT{PortForward: &gwapi.PeeringNATPortForward{
				Ports: []gwapi.PeeringNATPortForwardEntry{{Port: "80", As: "8080"}},
			}}, nil, poolA, "")
		got := forward(t, gp)
		require.Empty(t, got, "src is a target; cannot initiate")
	})

	t.Run("port-forward on dst: ALLOW with DNAT pool IP", func(t *testing.T) {
		gp := makeGWPeering(t, "p", vpcA, vpcB,
			nil, &gwapi.PeeringNAT{PortForward: &gwapi.PeeringNATPortForward{
				Ports: []gwapi.PeeringNATPortForwardEntry{{Port: "80", As: "8080"}},
			}}, "", poolB)
		got := forward(t, gp)
		require.Len(t, got, 1)
		require.Equal(t, VerdictAllow, got[0].Verdict)
		require.NotNil(t, got[0].NAT)
		require.Equal(t, "10.101.0.0", got[0].NAT.DestinationIP.String())
		require.NotEmpty(t, got[0].NAT.PortForwards)
	})

	t.Run("static NAT both sides: ALLOW with offset DNAT", func(t *testing.T) {
		gp := makeGWPeering(t, "p", vpcA, vpcB,
			&gwapi.PeeringNAT{Static: &gwapi.PeeringNATStatic{}},
			&gwapi.PeeringNAT{Static: &gwapi.PeeringNATStatic{}}, poolA, poolB)
		got := forward(t, gp)
		require.Len(t, got, 1)
		require.Equal(t, VerdictAllow, got[0].Verdict)
		require.NotNil(t, got[0].NAT)
		// Forward A→B: dst side (B) has static → DestinationIP is B's pool + offset.
		// server-b is at 10.0.2.5 (offset 5 into 10.0.2.0/24), pool is 10.101.0.0/24.
		require.Equal(t, "10.101.0.5", got[0].NAT.DestinationIP.String())
		// src side (A) has static → SourceIP is A's pool offset for server-a.
		// server-a at 10.0.1.5 → 10.100.0.5.
		require.Equal(t, "10.100.0.5", got[0].NAT.SourceIP.String())
	})
}

func TestParseSinglePort(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    uint16
		wantErr bool
	}{
		{in: "80", want: 80},
		{in: "65535", want: 65535},
		{in: "80-80", want: 80},
		{in: "80-81", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "", wantErr: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSinglePort(tc.in)
			if tc.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
