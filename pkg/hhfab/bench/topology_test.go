// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/api/valid"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	fabriccomp "go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/hhfab/bench"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

// fabricConfig builds the meta.FabricConfig the admission webhooks validate
// against, the same way the Fabric controller does.
func fabricConfig(t *testing.T) *fmeta.FabricConfig {
	t.Helper()

	cfg, err := fabriccomp.GetFabricConfig(defaultFab())
	require.NoError(t, err)

	cfg, err = cfg.Init(fmeta.ExtraValidators{Peering: valid.Peering})
	require.NoError(t, err)

	return cfg
}

// switchProfiles returns the default profile catalog, keyed by name.
func switchProfiles(t *testing.T, ctx context.Context, l *apiutil.Loader, cfg *fmeta.FabricConfig) map[string]*wiringapi.SwitchProfile {
	t.Helper()

	profiles := switchprofile.NewDefaultSwitchProfiles()
	require.NoError(t, profiles.RegisterAll(ctx, l.GetClient(), cfg))

	out := map[string]*wiringapi.SwitchProfile{}
	for _, sp := range profiles.List() {
		out[sp.Name] = sp
	}

	return out
}

// generate builds the given fabrics into a fresh loader.
func generate(t *testing.T, values []string) (*apiutil.Loader, *fmeta.FabricConfig) {
	t.Helper()

	ctx := t.Context()
	cfg := fabricConfig(t)
	l := apiutil.NewLoader()
	profiles := switchProfiles(t, ctx, l, cfg)

	specs, err := bench.ParseFabricSpecs(values)
	require.NoError(t, err)

	alloc, err := bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.NoError(t, err)

	g, err := bench.NewGenerator(specs, alloc, profiles)
	require.NoError(t, err)

	require.NoError(t, g.Generate(ctx, l))

	return l, cfg
}

// The generated wiring has to pass exactly the Default() + Validate() the
// admission webhooks run, or `bench init` would fail partway through a long
// apply instead of up front.
func TestGeneratedWiringValidates(t *testing.T) {
	t.Parallel()

	l, cfg := generate(t, []string{"name=dc1,spines=2,leaves=2,server-ports=2,vpcs=2,attach=2,peerings=1"})

	require.NoError(t, apiutil.ValidateFabricGateway(t.Context(), l, cfg))
}

// Several fabrics must coexist: they reuse identical VLAN ranges and IPv4
// prefixes in their own namespaces, which validation permits because it never
// compares across namespaces.
func TestMultipleFabricsValidate(t *testing.T) {
	t.Parallel()

	l, cfg := generate(t, []string{
		"name=dc1,spines=2,leaves=2,server-ports=1,vpcs=1",
		"name=dc2,spines=2,leaves=2,server-ports=1,vpcs=1",
	})

	require.NoError(t, apiutil.ValidateFabricGateway(t.Context(), l, cfg))
}

// BGP-unnumbered fabric links leave both link IPs empty, which
// Connection.Validate accepts (it only rejects a one-sided link).
func TestUnnumberedFabricValidates(t *testing.T) {
	t.Parallel()

	l, cfg := generate(t, []string{"name=dc1,spines=2,leaves=2,server-ports=1,fabric-unnum=true"})

	require.NoError(t, apiutil.ValidateFabricGateway(t.Context(), l, cfg))
}

// Multiple links per spine-leaf pair land in a single Connection.
func TestMultiLinkFabricValidates(t *testing.T) {
	t.Parallel()

	l, cfg := generate(t, []string{"name=dc1,spines=2,leaves=2,fabric-links=2,server-ports=1"})

	require.NoError(t, apiutil.ValidateFabricGateway(t.Context(), l, cfg))

	conns := &wiringapi.ConnectionList{}
	require.NoError(t, l.List(t.Context(), conns))

	fabricConns := 0
	for _, conn := range conns.Items {
		if conn.Spec.Fabric != nil {
			fabricConns++
			require.Len(t, conn.Spec.Fabric.Links, 2)
		}
	}
	require.Equal(t, 4, fabricConns) // 2 spines x 2 leaves
}

func TestGeneratedObjectCounts(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l, _ := generate(t, []string{"name=dc1,spines=2,leaves=3,server-ports=2,vpcs=2,attach=2,peerings=1"})

	switches := &wiringapi.SwitchList{}
	require.NoError(t, l.List(ctx, switches))
	require.Len(t, switches.Items, 5)

	servers := &wiringapi.ServerList{}
	require.NoError(t, l.List(ctx, servers))
	require.Len(t, servers.Items, 24) // 3 leaves * 2 ports * 4 subports

	conns := &wiringapi.ConnectionList{}
	require.NoError(t, l.List(ctx, conns))
	require.Len(t, conns.Items, 6+24) // 2*3 fabric + one per server
}

// Every leaf must carry the breakout configuration for its server ports, and
// spines must not.
func TestLeafBreakouts(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l, _ := generate(t, []string{"name=dc1,spines=2,leaves=2,server-ports=3"})

	switches := &wiringapi.SwitchList{}
	require.NoError(t, l.List(ctx, switches))

	for _, sw := range switches.Items {
		switch sw.Spec.Role {
		case wiringapi.SwitchRoleSpine:
			require.Empty(t, sw.Spec.PortBreakouts, "spine %s has breakouts", sw.Name)
			require.Empty(t, sw.Spec.VTEPIP, "spine %s has a VTEP IP", sw.Name)
		default:
			require.Len(t, sw.Spec.PortBreakouts, 3, "leaf %s breakouts", sw.Name)
			require.NotEmpty(t, sw.Spec.VTEPIP, "leaf %s has no VTEP IP", sw.Name)

			for _, mode := range sw.Spec.PortBreakouts {
				require.Equal(t, "4x200G", mode)
			}
		}
	}
}

// The real fabric shape fills a DS5000 exactly: 32 uplinks per leaf to 32
// spines, and 64 ports per spine, one per leaf.
//
// This deliberately does not run ValidateFabricGateway: Connection.Validate
// Lists every Connection and walks its endpoints on each create, so validating
// this many connections takes ~50s. The smaller tests above cover validation;
// what is unique at full size is that the port plan fits the profile and that
// no two connections claim the same port.
func TestFullShapeFillsProfileExactly(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l, _ := generate(t, []string{"name=dc1,server-ports=1"})

	switches := &wiringapi.SwitchList{}
	require.NoError(t, l.List(ctx, switches))
	require.Len(t, switches.Items, 96)

	conns := &wiringapi.ConnectionList{}
	require.NoError(t, l.List(ctx, conns))

	// Every port a switch uses must be distinct: Connection.Validate treats
	// "E1/33" and "E1/33/1" as the same port, so a collision here is the bug
	// most likely to slip past review.
	used := map[string]string{}
	for _, conn := range conns.Items {
		_, _, ports, _, err := conn.Spec.Endpoints()
		require.NoError(t, err)

		for _, port := range ports {
			prev, seen := used[port]
			require.False(t, seen, "port %s used by both %s and %s", port, prev, conn.Name)
			used[port] = conn.Name
		}
	}

	// 2048 fabric links + 64 leaves x 4 server subports, two endpoints each.
	require.Len(t, conns.Items, 2048+256)
}

// Every breakout-capable port is referenced by its subport name, including
// uplinks on a single-subport 1x800G breakout, so port names never rely on the
// "E1/1" alias that Connection.Validate treats as colliding with "E1/1/1".
func TestPortsUseSubportNames(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l, _ := generate(t, []string{"name=dc1,spines=2,leaves=2,server-ports=1"})

	conns := &wiringapi.ConnectionList{}
	require.NoError(t, l.List(ctx, conns))

	subportRe := regexp.MustCompile(`^E\d+/\d+/\d+$`)

	checked := 0
	for _, conn := range conns.Items {
		_, _, ports, _, err := conn.Spec.Endpoints()
		require.NoError(t, err)

		for _, port := range ports {
			device, local, found := strings.Cut(port, "/")
			require.True(t, found, "port %q has no device", port)

			// Server-side ports are NIC names, not switch ports.
			if !strings.Contains(device, "-spine-") && !strings.Contains(device, "-leaf-") {
				continue
			}

			require.Regexp(t, subportRe, local, "switch port %q on %s is not a subport name", local, device)
			checked++
		}
	}

	require.Positive(t, checked)
}

// A profile that cannot supply the requested ports has to be refused by the
// generator rather than by the webhook mid-apply.
func TestGeneratorRefusesOversizedShape(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg := fabricConfig(t)
	l := apiutil.NewLoader()
	profiles := switchProfiles(t, ctx, l, cfg)

	// A DS5000 has 64 data ports usable for uplinks plus breakouts; asking for
	// 60 uplinks and 32 server ports cannot fit.
	specs, err := bench.ParseFabricSpecs([]string{"name=dc1,spines=60,leaves=4,server-ports=32"})
	require.NoError(t, err)

	alloc, err := bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.NoError(t, err)

	_, err = bench.NewGenerator(specs, alloc, profiles)
	require.ErrorContains(t, err, "data ports")
}

// 1x800G is the one server breakout that uses a single subport per port, so it
// exercises the E1/N/1 naming path for single-subport modes and must be
// accepted.
func TestGeneratorAcceptsSingleSubportBreakout(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg := fabricConfig(t)
	l := apiutil.NewLoader()
	profiles := switchProfiles(t, ctx, l, cfg)

	specs, err := bench.ParseFabricSpecs([]string{"name=dc1,spines=2,leaves=2,server-breakout=1x800G"})
	require.NoError(t, err)

	alloc, err := bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.NoError(t, err)

	// 1x800G is supported, so this one must succeed.
	_, err = bench.NewGenerator(specs, alloc, profiles)
	require.NoError(t, err)
}
