// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

func testConfig() *Config {
	c := DefaultConfig()
	c.Leaves = 4
	c.Servers = 6
	c.NICs = 3

	return c
}

func TestValidate(t *testing.T) {
	require.NoError(t, testConfig().Validate())

	for name, mod := range map[string]func(*Config){
		"layout":  func(c *Config) { c.Layout = "wat" },
		"retries": func(c *Config) { c.Retries = "wat" },
		"leaves":  func(c *Config) { c.Leaves = 0 },
		"relay":   func(c *Config) { c.RelayBase = netip.MustParseAddr("::1") },
	} {
		c := testConfig()
		mod(c)
		require.Error(t, c.Validate(), name)
	}
}

func TestRelayIPs(t *testing.T) {
	c := testConfig()
	require.Equal(t, []netip.Addr{
		netip.MustParseAddr("172.30.3.1"), netip.MustParseAddr("172.30.3.2"),
		netip.MustParseAddr("172.30.3.3"), netip.MustParseAddr("172.30.3.4"),
	}, c.RelayIPs())

	c.RelayBase = netip.MustParseAddr("172.30.3.254")
	c.Leaves = 3
	require.Equal(t, "172.30.4.0", c.RelayIPs()[2].String())
}

func TestTopology(t *testing.T) {
	for _, tt := range []struct {
		layout  string
		subnets int
	}{{LayoutRail, 3}, {LayoutFlat, 1}, {LayoutLeaf, 12}} {
		c := testConfig()
		c.Layout = tt.layout
		require.Equal(t, tt.subnets, c.NumSubnets(), tt.layout)

		perSubnet := map[int]int{}
		for i := range c.Servers * c.NICs {
			leaf, sub := c.placement(i)
			require.Less(t, leaf, c.Leaves)
			require.Less(t, sub, tt.subnets)
			perSubnet[sub]++
		}
		require.Len(t, perSubnet, tt.subnets, tt.layout)
	}

	c := testConfig()
	leaf, sub := c.placement(7)
	require.Equal(t, 3, leaf)
	require.Equal(t, 1, sub)

	c.Layout = LayoutLeaf
	leaf, sub = c.placement(7)
	require.Equal(t, 3, leaf)
	require.Equal(t, 3*3+1, sub)
}

func TestSubnet(t *testing.T) {
	c := testConfig()
	s := c.subnet(2)
	require.Equal(t, "10.0.160.0/20", s.prefix.String())
	require.Equal(t, 1002, s.vlan)
	require.Equal(t, "Vlan1002", s.circuitID)
}

func TestSubnetPrefix(t *testing.T) {
	c := testConfig()
	c.SubnetPrefix = 24
	c.NICs = 16

	require.NoError(t, c.Validate())
	require.Equal(t, "10.0.128.0/24", c.subnet(0).prefix.String())
	require.Equal(t, "10.0.131.0/24", c.subnet(3).prefix.String())

	// a /24 pool holds 250 clients: everything after the gateway up to the last host
	sub := BuildVPC(c).Spec.Subnets["subnet-3"]
	require.NotNil(t, sub)
	require.Equal(t, "10.0.131.0/24", sub.Subnet)
	require.Equal(t, "10.0.131.1", sub.Gateway)
	require.Equal(t, "10.0.131.2", sub.DHCP.Range.Start)
	require.Equal(t, "10.0.131.254", sub.DHCP.Range.End)

	c.SubnetPrefix = 29
	require.Error(t, c.Validate())
}

func TestBuildVPC(t *testing.T) {
	c := testConfig()
	c.VPC = "LoadTest"
	vpc := BuildVPC(c)

	require.Equal(t, "loadtest", vpc.Name)
	require.Equal(t, "default", vpc.Namespace)
	require.Len(t, vpc.Spec.Subnets, 3)

	sub := vpc.Spec.Subnets["subnet-1"]
	require.NotNil(t, sub)
	require.Equal(t, "10.0.144.0/20", sub.Subnet)
	require.Equal(t, "10.0.144.1", sub.Gateway)
	require.Equal(t, uint16(1001), sub.VLAN)
	require.True(t, sub.DHCP.Enable)
	require.Equal(t, "10.0.144.10", sub.DHCP.Range.Start)
	require.Equal(t, "10.0.159.254", sub.DHCP.Range.End)
	require.Equal(t, uint32(3600), sub.DHCP.Options.LeaseTimeSeconds)

	require.Equal(t, "loadtest--subnet-1", c.DHCPSubnetName(1))
}

func TestBuildFakeSwitches(t *testing.T) {
	c := testConfig()
	tmpl := &wiringapi.Switch{Spec: wiringapi.SwitchSpec{
		Role:           wiringapi.SwitchRoleServerLeaf,
		Profile:        "vs",
		Groups:         []string{"g"},
		Redundancy:     wiringapi.SwitchRedundancy{Group: "r"},
		VLANNamespaces: []string{"default"},
		ASN:            65101,
		IP:             "172.30.0.8/21",
		Boot:           wiringapi.SwitchBoot{MAC: "aa:bb:cc:dd:ee:ff"},
		PortBreakouts:  map[string]string{"1/1": "4x25G"},
	}}
	tmpl.Name = "leaf-01"

	fakes, err := BuildFakeSwitches(c, tmpl, FakeSwitchOpts{
		ProtocolBase: netip.MustParseAddr("172.30.11.1"),
		VTEPBase:     netip.MustParseAddr("172.30.15.1"),
		ASNBase:      65400,
	})
	require.NoError(t, err)
	require.Len(t, fakes, 4)

	last := fakes[3]
	require.Equal(t, "loadtest-leaf-03", last.Name)
	require.Equal(t, "true", last.Labels[FakeLabel])
	require.Equal(t, "172.30.3.4/21", last.Spec.IP)
	require.Equal(t, "172.30.11.4/32", last.Spec.ProtocolIP)
	require.Equal(t, "172.30.15.4/32", last.Spec.VTEPIP)
	require.Equal(t, uint32(65403), last.Spec.ASN)
	require.Equal(t, "vs", last.Spec.Profile)
	require.Equal(t, []string{"default"}, last.Spec.VLANNamespaces)
	require.Empty(t, last.Spec.Groups)
	require.Empty(t, last.Spec.Redundancy.Group)
	require.Empty(t, last.Spec.Boot.MAC)
	require.Empty(t, last.Spec.PortBreakouts)

	// the template is left untouched
	require.Equal(t, "172.30.0.8/21", tmpl.Spec.IP)
	require.NotEmpty(t, tmpl.Spec.Groups)

	fakes, err = BuildFakeSwitches(c, tmpl, FakeSwitchOpts{ProtocolBase: netip.MustParseAddr("172.30.11.1")})
	require.NoError(t, err)
	require.Empty(t, fakes[0].Spec.VTEPIP)

	tmpl.Spec.IP = "nope"
	_, err = BuildFakeSwitches(c, tmpl, FakeSwitchOpts{ProtocolBase: netip.MustParseAddr("172.30.11.1")})
	require.Error(t, err)
}

func TestCheckSwitchConflicts(t *testing.T) {
	c := testConfig()
	tmpl := &wiringapi.Switch{Spec: wiringapi.SwitchSpec{IP: "172.30.0.8/21"}}
	fakes, err := BuildFakeSwitches(c, tmpl, FakeSwitchOpts{
		ProtocolBase: netip.MustParseAddr("172.30.11.1"),
		VTEPBase:     netip.MustParseAddr("172.30.15.1"),
		ASNBase:      65400,
	})
	require.NoError(t, err)

	real := []wiringapi.Switch{{Spec: wiringapi.SwitchSpec{
		IP: "172.30.0.8/21", ProtocolIP: "172.30.8.1/32", VTEPIP: "172.30.12.1/32", ASN: 65101,
	}}}
	require.NoError(t, CheckSwitchConflicts(fakes, real))

	for name, sw := range map[string]wiringapi.SwitchSpec{
		"ip":       {IP: "172.30.3.2/21"},
		"protocol": {ProtocolIP: "172.30.11.3/32"},
		"vtep":     {VTEPIP: "172.30.15.4/32"},
		"asn":      {ASN: 65402},
	} {
		require.Error(t, CheckSwitchConflicts(fakes, []wiringapi.Switch{{Spec: sw}}), name)
	}
}

func TestCheckManagement(t *testing.T) {
	c := testConfig()
	mgmt := netip.MustParsePrefix("172.30.0.0/21")
	start, end := netip.MustParseAddr("172.30.4.0"), netip.MustParseAddr("172.30.7.254")
	control := netip.MustParseAddr("172.30.0.5")

	require.NoError(t, CheckManagement(c.RelayIPs(), mgmt, start, end, control))

	c.RelayBase = netip.MustParseAddr("172.30.3.254")
	require.Error(t, CheckManagement(c.RelayIPs(), mgmt, start, end, control), "reaches DHCP range")

	c.RelayBase = netip.MustParseAddr("10.0.0.1")
	require.Error(t, CheckManagement(c.RelayIPs(), mgmt, start, end, control), "outside of management")

	c.RelayBase = control
	require.Error(t, CheckManagement(c.RelayIPs(), mgmt, start, end, control), "control IP")
}

func TestCheckNamespaces(t *testing.T) {
	c := testConfig()
	ipv4 := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")}
	vlans := []meta.VLANRange{{From: 1000, To: 2999}}

	require.NoError(t, CheckNamespaces(c, ipv4, vlans))

	c.CIDRBase = netip.MustParseAddr("10.64.0.0")
	require.Error(t, CheckNamespaces(c, ipv4, vlans))
	require.NoError(t, CheckNamespaces(c, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, vlans))

	c = testConfig()
	c.VLANBase = 2998
	require.Error(t, CheckNamespaces(c, ipv4, vlans))
}
