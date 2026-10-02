// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/hhfab/bench"
)

func TestParseFabricSpecDefaults(t *testing.T) {
	t.Parallel()

	spec, err := bench.ParseFabricSpec("name=dc1")
	require.NoError(t, err)

	require.Equal(t, "dc1", spec.Name)
	require.Equal(t, uint(bench.DefaultDomains), spec.Domains)
	require.Equal(t, "default", spec.DomainName(0))
	require.Equal(t, uint(bench.DefaultSpines), spec.Spines)
	require.Equal(t, uint(bench.DefaultLeaves), spec.Leaves)
	require.Equal(t, uint(bench.DefaultFabricLinks), spec.FabricLinks)
	require.False(t, spec.FabricUnnum)
	require.Equal(t, uint(bench.DefaultServerPorts), spec.ServerPorts)
	require.Equal(t, bench.DefaultServerBreakout, spec.ServerBreakout)
	require.Equal(t, uint(bench.DefaultSysNameOverride), spec.SysNameOverride)
	require.Equal(t, uint(bench.DefaultVPCs), spec.VPCs)
	require.Equal(t, uint(bench.DefaultAttach), spec.Attach)
	require.Equal(t, uint(bench.DefaultPeerings), spec.Peerings)
	require.Equal(t, bench.DefaultProfile, spec.Profile)
}

// The default shape fills a DS5000 exactly: 32 uplinks to 32 spines and 32
// ports broken out 4x200G for servers.
func TestDefaultShapeCounts(t *testing.T) {
	t.Parallel()

	spec, err := bench.ParseFabricSpec("name=dc1")
	require.NoError(t, err)

	require.Equal(t, uint(4), spec.ServerSubportsPerPort())
	require.Equal(t, uint(96), spec.Switches())
	require.Equal(t, uint(2048), spec.FabricConns())      // one per spine-leaf pair
	require.Equal(t, uint(2048), spec.FabricLinksTotal()) // one link per pair
	require.Equal(t, uint(128), spec.ServersPerLeaf())
	require.Equal(t, uint(8192), spec.Servers())
	require.Equal(t, uint(8192), spec.Attachments())
}

func TestParseFabricSpecOverrides(t *testing.T) {
	t.Parallel()

	spec, err := bench.ParseFabricSpec("name=small,spines=2,leaves=4,fabric-links=2,fabric-unnum=true,server-ports=8,server-breakout=2x400G,vpcs=4,attach=2,peerings=3")
	require.NoError(t, err)

	require.Equal(t, "small", spec.Name)
	require.Equal(t, uint(2), spec.Spines)
	require.Equal(t, uint(4), spec.Leaves)
	require.Equal(t, uint(2), spec.FabricLinks)
	require.True(t, spec.FabricUnnum)
	require.Equal(t, uint(8), spec.ServerPorts)
	require.Equal(t, uint(2), spec.ServerSubportsPerPort())
	require.Equal(t, uint(4), spec.VPCs)
	require.Equal(t, uint(2), spec.Attach)
	require.Equal(t, uint(3), spec.Peerings)

	require.Equal(t, uint(8), spec.FabricConns())       // 2 spines * 4 leaves
	require.Equal(t, uint(16), spec.FabricLinksTotal()) // ... each carrying 2 links
	require.Equal(t, uint(64), spec.Servers())          // 4 leaves * 8 ports * 2 subports
	require.Equal(t, uint(128), spec.Attachments())
}

// The overrides are an exact share of the servers, spread evenly rather than
// bunched at the start of each leaf.
func TestOverridesSysName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pct, n, want uint
	}{
		{0, 100, 0},
		{50, 100, 50},
		{50, 101, 50},
		{25, 8, 2},
		{100, 7, 7},
	} {
		spec, err := bench.ParseFabricSpec(fmt.Sprintf("name=dc1,sysname-override=%d", tc.pct))
		require.NoError(t, err)

		got := uint(0)
		for idx := range tc.n {
			if spec.OverridesSysName(idx) {
				got++
			}
		}
		require.Equal(t, tc.want, got, "%d%% of %d", tc.pct, tc.n)
	}

	// At 50% it alternates, so every leaf gets its half.
	spec, err := bench.ParseFabricSpec("name=dc1")
	require.NoError(t, err)
	require.False(t, spec.OverridesSysName(0))
	require.True(t, spec.OverridesSysName(1))
	require.False(t, spec.OverridesSysName(2))
	require.True(t, spec.OverridesSysName(3))
}

// Every count describes one domain, so the shape helpers stay per domain and
// only the object total multiplies; the Fabric and the namespaces and group
// are shared by every domain.
func TestParseFabricSpecDomains(t *testing.T) {
	t.Parallel()

	one, err := bench.ParseFabricSpec("name=dc1,spines=2,leaves=4,server-ports=1,vpcs=2,peerings=1")
	require.NoError(t, err)
	three, err := bench.ParseFabricSpec("name=dc1,domains=3,spines=2,leaves=4,server-ports=1,vpcs=2,peerings=1")
	require.NoError(t, err)

	require.Equal(t, uint(3), three.Domains)
	require.Equal(t, one.Switches(), three.Switches())
	require.Equal(t, one.Servers(), three.Servers())
	require.Equal(t, (one.Objects()-4)*3+4, three.Objects())

	require.Equal(t, []string{"domain-1", "domain-2", "domain-3"},
		[]string{three.DomainName(0), three.DomainName(1), three.DomainName(2)})

	// VPCs share the fabric-wide numbering, so the cap is on the total.
	_, err = bench.ParseFabricSpec("name=dc1,domains=2,vpcs=500")
	require.ErrorContains(t, err, "maximum is 999 in total")
}

func TestParseFabricSpecRejects(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		value  string
		errStr string
	}{
		"no name":          {"spines=4", "name is required"},
		"name too long":    {"name=toolongx", "maximum is 7"},
		"name uppercase":   {"name=DC1", "RFC 1123"},
		"name underscore":  {"name=dc_1", "RFC 1123"},
		"name default":     {"name=default", "reserved for the cluster's own Fabric"},
		"zero spines":      {"name=dc1,spines=0", "spines must be >= 1"},
		"zero leaves":      {"name=dc1,leaves=0", "leaves must be >= 1"},
		"zero links":       {"name=dc1,fabric-links=0", "fabric-links must be >= 1"},
		"bad breakout":     {"name=dc1,server-breakout=3x100G", "server-breakout"},
		"sysname over 100": {"name=dc1,sysname-override=101", "percentage of servers"},
		"attach over vpcs": {"name=dc1,vpcs=2,attach=3", "each attachment goes to a distinct VPC"},
		"peering one vpc":  {"name=dc1,vpcs=1,peerings=1", "requires at least 2 vpcs"},
		"peering too many": {"name=dc1,vpcs=3,peerings=4", "at most 3 distinct pairs"},
		"too many vpcs":    {"name=dc1,vpcs=1000", "maximum is 999"},
		"unknown key":      {"name=dc1,nope=1", "unknown key"},
		"not key=value":    {"name=dc1,bare", "should be key=value"},
		"bad uint":         {"name=dc1,spines=x", "not a non-negative integer"},
		"bad bool":         {"name=dc1,fabric-unnum=maybe", "not a boolean"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := bench.ParseFabricSpec(tc.value)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errStr)
		})
	}
}

// A 7 character name is the longest that keeps "<fabric>-NNN" within the 11
// character VPC name limit.
func TestFabricNameLengthBoundary(t *testing.T) {
	t.Parallel()

	spec, err := bench.ParseFabricSpec("name=edgefab")
	require.NoError(t, err)
	require.Len(t, spec.Name, bench.MaxFabricNameLen)
	require.LessOrEqual(t, len(spec.Name+"-999"), 11)

	_, err = bench.ParseFabricSpec("name=edgefabs")
	require.Error(t, err)
}

func TestParseFabricSpecs(t *testing.T) {
	t.Parallel()

	specs, err := bench.ParseFabricSpecs([]string{"name=dc1", "name=dc2,leaves=8"})
	require.NoError(t, err)
	require.Len(t, specs, 2)
	require.Equal(t, "dc1", specs[0].Name)
	require.Equal(t, "dc2", specs[1].Name)
	require.Equal(t, uint(8), specs[1].Leaves)

	_, err = bench.ParseFabricSpecs(nil)
	require.ErrorContains(t, err, "at least one --fabric is required")

	_, err = bench.ParseFabricSpecs([]string{"name=dc1", "name=dc1"})
	require.ErrorContains(t, err, "duplicate fabric name")

	_, err = bench.ParseFabricSpecs([]string{"name=dc1", "name=dc2,spines=0"})
	require.ErrorContains(t, err, "--fabric 2")
}
