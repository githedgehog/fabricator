// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/hhfab/connmatrix"
)

func TestParseInterfaceAddrs(t *testing.T) {
	stdout := `lo 127.0.0.1/8
enp2s0 172.31.0.5/24
enp2s1 10.0.1.5/24
docker0 172.17.0.1/16
lo 10.0.50.1/32
`
	got, err := parseInterfaceAddrs(stdout)
	require.NoError(t, err)
	require.Len(t, got, 2, "lo 127.0.0.1, enp2s0, and docker0 are filtered; enp2s1 and /32 on lo are kept")
	require.Equal(t, "enp2s1", got[0].iface)
	require.Equal(t, "10.0.1.5/24", got[0].prefix.String())
	require.Equal(t, "lo", got[1].iface)
	require.Equal(t, "10.0.50.1/32", got[1].prefix.String())
}

func TestParseInterfaceAddrsEmpty(t *testing.T) {
	got, err := parseInterfaceAddrs("")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestMatchEndpointIPsSingleSubnet(t *testing.T) {
	addrs := []ifaceAddr{{iface: "enp2s1", prefix: netip.MustParsePrefix("10.0.1.5/24")}}
	atts := []subnetAttachment{
		{FullName: "vpc-1/default", CIDR: netip.MustParsePrefix("10.0.1.0/24"), HostBGP: false},
	}
	got := matchEndpointIPs("s1", addrs, atts)
	require.Len(t, got, 1)
	require.Equal(t, "10.0.1.5", got[0].IP.String())
	require.Equal(t, "vpc-1/default", got[0].Subnet)
	require.False(t, got[0].HostBGP)
}

func TestMatchEndpointIPsMultiVPCTrunking(t *testing.T) {
	// Server attached to vpc-1 and vpc-2 via VLAN-tagged sub-interfaces.
	addrs := []ifaceAddr{
		{iface: "enp2s1.1001", prefix: netip.MustParsePrefix("10.0.1.5/24")},
		{iface: "enp2s1.1002", prefix: netip.MustParsePrefix("10.0.2.5/24")},
	}
	atts := []subnetAttachment{
		{FullName: "vpc-1/default", CIDR: netip.MustParsePrefix("10.0.1.0/24")},
		{FullName: "vpc-2/default", CIDR: netip.MustParsePrefix("10.0.2.0/24")},
	}
	got := matchEndpointIPs("s1", addrs, atts)
	require.Len(t, got, 2, "one endpoint per subnet attachment")

	bySubnet := map[string]connmatrix.Endpoint{}
	for _, ep := range got {
		bySubnet[ep.Subnet] = ep
	}
	require.Equal(t, "10.0.1.5", bySubnet["vpc-1/default"].IP.String())
	require.Equal(t, "10.0.2.5", bySubnet["vpc-2/default"].IP.String())
}

func TestMatchEndpointIPsHostBGP(t *testing.T) {
	// HostBGP server: only a /32 VIP on lo, no subnet-assigned address.
	addrs := []ifaceAddr{
		{iface: "lo", prefix: netip.MustParsePrefix("10.0.1.42/32")},
	}
	atts := []subnetAttachment{
		{FullName: "vpc-1/default", CIDR: netip.MustParsePrefix("10.0.1.0/24"), HostBGP: true},
	}
	got := matchEndpointIPs("s1", addrs, atts)
	require.Len(t, got, 1)
	require.True(t, got[0].HostBGP)
	require.Equal(t, "10.0.1.42", got[0].IP.String())
}

func TestMatchEndpointIPsHostBGPMixedWithBundled(t *testing.T) {
	// Server attached to one HostBGP subnet (vpc-1) and one regular subnet (vpc-2).
	addrs := []ifaceAddr{
		{iface: "lo", prefix: netip.MustParsePrefix("10.0.1.42/32")},   // VIP for vpc-1
		{iface: "bond0", prefix: netip.MustParsePrefix("10.0.2.5/24")}, // DHCP for vpc-2
	}
	atts := []subnetAttachment{
		{FullName: "vpc-1/default", CIDR: netip.MustParsePrefix("10.0.1.0/24"), HostBGP: true},
		{FullName: "vpc-2/default", CIDR: netip.MustParsePrefix("10.0.2.0/24"), HostBGP: false},
	}
	got := matchEndpointIPs("s1", addrs, atts)
	require.Len(t, got, 2)

	bySubnet := map[string]connmatrix.Endpoint{}
	for _, ep := range got {
		bySubnet[ep.Subnet] = ep
	}
	require.True(t, bySubnet["vpc-1/default"].HostBGP)
	require.Equal(t, "10.0.1.42", bySubnet["vpc-1/default"].IP.String())
	require.False(t, bySubnet["vpc-2/default"].HostBGP)
	require.Equal(t, "10.0.2.5", bySubnet["vpc-2/default"].IP.String())
}

func TestMatchEndpointIPsRegularSubnetIgnoresLo(t *testing.T) {
	// A /32 on lo must not match a non-HostBGP subnet, even if it happens to be in-range.
	addrs := []ifaceAddr{
		{iface: "lo", prefix: netip.MustParsePrefix("10.0.1.42/32")},
	}
	atts := []subnetAttachment{
		{FullName: "vpc-1/default", CIDR: netip.MustParsePrefix("10.0.1.0/24"), HostBGP: false},
	}
	got := matchEndpointIPs("s1", addrs, atts)
	require.Empty(t, got, "regular subnets must not pick up VIPs")
}

func TestMatchEndpointIPsHostBGPIgnoresNonLoopback(t *testing.T) {
	// A /32 on a data interface must not match a HostBGP subnet.
	addrs := []ifaceAddr{
		{iface: "enp2s1", prefix: netip.MustParsePrefix("10.0.1.42/32")},
	}
	atts := []subnetAttachment{
		{FullName: "vpc-1/default", CIDR: netip.MustParsePrefix("10.0.1.0/24"), HostBGP: true},
	}
	got := matchEndpointIPs("s1", addrs, atts)
	require.Empty(t, got, "HostBGP subnets require /32 on lo specifically")
}

func TestMatchEndpointIPsNoMatch(t *testing.T) {
	addrs := []ifaceAddr{{iface: "enp2s1", prefix: netip.MustParsePrefix("10.0.9.5/24")}}
	atts := []subnetAttachment{
		{FullName: "vpc-1/default", CIDR: netip.MustParsePrefix("10.0.1.0/24")},
	}
	got := matchEndpointIPs("s1", addrs, atts)
	require.Empty(t, got, "addresses outside the subnet CIDR do not match")
}
