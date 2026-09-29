// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/hhfab/bench"
)

// defaultFab is a Fabricator carrying the stock config, which is what a VLAB
// gets from `hhfab init`.
func defaultFab() fabapi.Fabricator {
	return fabapi.Fabricator{
		Spec: fabapi.FabricatorSpec{
			Config: fab.DefaultConfig,
		},
	}
}

// defaultSpecs builds n fabrics of the default shape, named f0..fn-1.
func defaultSpecs(t *testing.T, n int) []bench.FabricSpec {
	t.Helper()

	values := make([]string, 0, n)
	for i := range n {
		values = append(values, fmt.Sprintf("name=f%d", i))
	}

	specs, err := bench.ParseFabricSpecs(values)
	require.NoError(t, err)

	return specs
}

// The stock fab.yaml supports exactly 6 fabrics, bounded by the leaf ASN range.
func TestAllocatorCapacityOnStockDefaults(t *testing.T) {
	t.Parallel()

	for n := 1; n <= 6; n++ {
		_, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, n))
		require.NoError(t, err, "%d fabrics should fit on stock defaults", n)
	}

	_, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 7))
	require.Error(t, err)
	require.Contains(t, err.Error(), "leaf ASNs")
	require.Contains(t, err.Error(), "leafASNEnd")
	require.Contains(t, err.Error(), "4-byte ASNs")
}

// Bench addresses must never collide with the control VIP or the control
// node's own management IP (172.30.0.5, ControlPlaneAPIIP).
func TestAllocatorSkipsControlPlaneAddresses(t *testing.T) {
	t.Parallel()

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 6))
	require.NoError(t, err)

	first, err := a.SwitchMgmtIP(0, 0)
	require.NoError(t, err)
	require.Equal(t, "172.30.0.6/21", first.String())

	for slot := range a.Slots() {
		for idx := range uint(bench.StrideSwitches) {
			ip, err := a.SwitchMgmtIP(slot, idx)
			require.NoError(t, err)
			require.NotEqual(t, "172.30.0.1", ip.Addr().String(), "collides with the control VIP")
			require.NotEqual(t, "172.30.0.5", ip.Addr().String(), "collides with ControlPlaneAPIIP")
		}
	}
}

// Adding fabrics must not renumber the ones that already exist: this is what
// makes `bench init` safe to re-run with a longer --fabric list.
func TestAllocatorStableAsFabricsAreAdded(t *testing.T) {
	t.Parallel()

	four, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 4))
	require.NoError(t, err)
	six, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 6))
	require.NoError(t, err)

	for slot := range uint(4) {
		for idx := range uint(bench.StrideSwitches) {
			a, err := four.SwitchMgmtIP(slot, idx)
			require.NoError(t, err)
			b, err := six.SwitchMgmtIP(slot, idx)
			require.NoError(t, err)
			require.Equal(t, a, b, "mgmt IP moved for slot %d switch %d", slot, idx)

			a, err = four.SwitchProtocolIP(slot, idx)
			require.NoError(t, err)
			b, err = six.SwitchProtocolIP(slot, idx)
			require.NoError(t, err)
			require.Equal(t, a, b, "protocol IP moved for slot %d switch %d", slot, idx)
		}

		for idx := range uint(bench.StrideLeaves) {
			a, err := four.LeafASN(slot, idx)
			require.NoError(t, err)
			b, err := six.LeafASN(slot, idx)
			require.NoError(t, err)
			require.Equal(t, a, b, "leaf ASN moved for slot %d leaf %d", slot, idx)

			va, err := four.LeafVTEPIP(slot, idx)
			require.NoError(t, err)
			vb, err := six.LeafVTEPIP(slot, idx)
			require.NoError(t, err)
			require.Equal(t, va, vb, "VTEP IP moved for slot %d leaf %d", slot, idx)
		}
	}
}

// A smaller fabric in an earlier slot must not shift the later ones either,
// which is the whole point of reserving a fixed stride.
func TestAllocatorStableWhenFabricShrinks(t *testing.T) {
	t.Parallel()

	full := defaultSpecs(t, 3)

	shrunk, err := bench.ParseFabricSpecs([]string{"name=f0,spines=2,leaves=4", "name=f1", "name=f2"})
	require.NoError(t, err)

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, full)
	require.NoError(t, err)
	b, err := bench.NewAllocator(defaultFab(), 1, 0, 0, shrunk)
	require.NoError(t, err)

	for slot := uint(1); slot < 3; slot++ {
		x, err := a.SwitchMgmtIP(slot, 0)
		require.NoError(t, err)
		y, err := b.SwitchMgmtIP(slot, 0)
		require.NoError(t, err)
		require.Equal(t, x, y)

		xa, err := a.LeafASN(slot, 0)
		require.NoError(t, err)
		ya, err := b.LeafASN(slot, 0)
		require.NoError(t, err)
		require.Equal(t, xa, ya)
	}
}

func TestAllocatorValues(t *testing.T) {
	t.Parallel()

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 2))
	require.NoError(t, err)

	proto, err := a.SwitchProtocolIP(0, 0)
	require.NoError(t, err)
	require.Equal(t, "172.30.8.0/32", proto.String())

	vtep, err := a.LeafVTEPIP(0, 0)
	require.NoError(t, err)
	require.Equal(t, "172.30.12.0/32", vtep.String())

	asn, err := a.LeafASN(0, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(65101), asn)

	// Slot 1 starts a full stride later.
	asn, err = a.LeafASN(1, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(65101+bench.StrideLeaves), asn)

	require.Equal(t, fab.DefaultConfig.Fabric.SpineASN, a.SpineASN())
}

// Fabric link addresses must be a /31 pair in the same subnet, which
// Connection.Validate enforces.
func TestAllocatorFabricLinkPairs(t *testing.T) {
	t.Parallel()

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 2))
	require.NoError(t, err)

	spine, leaf, err := a.FabricLinkIPs(0, 0)
	require.NoError(t, err)
	require.Equal(t, "172.30.128.0/31", spine.String())
	require.Equal(t, "172.30.128.1/31", leaf.String())
	require.Equal(t, spine.Masked(), leaf.Masked())

	spine, leaf, err = a.FabricLinkIPs(0, 1)
	require.NoError(t, err)
	require.Equal(t, "172.30.128.2/31", spine.String())
	require.Equal(t, "172.30.128.3/31", leaf.String())
	require.Equal(t, spine.Masked(), leaf.Masked())

	// No overlap across slots.
	slot1, _, err := a.FabricLinkIPs(1, 0)
	require.NoError(t, err)
	require.Equal(t, "172.30.144.0/31", slot1.String())
}

// A fabric larger than the stride reservation is refused rather than silently
// overlapping the next slot.
func TestAllocatorRefusesOversizedFabric(t *testing.T) {
	t.Parallel()

	specs, err := bench.ParseFabricSpecs([]string{"name=big,spines=64,leaves=64"})
	require.NoError(t, err)

	_, err = bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.ErrorContains(t, err, "per-fabric reservation is 96")

	// Few enough switches to pass the switch check, but too many leaves for the
	// leaf ASN and VTEP strides.
	specs, err = bench.ParseFabricSpecs([]string{"name=big,spines=1,leaves=65"})
	require.NoError(t, err)

	_, err = bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.ErrorContains(t, err, "per-fabric reservation is 64")

	// Two links per pair doubles the address need past the reservation.
	specs, err = bench.ParseFabricSpecs([]string{"name=big,fabric-links=2"})
	require.NoError(t, err)

	_, err = bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.ErrorContains(t, err, "fabric-unnum=true")

	// Unless the links are unnumbered, in which case no addresses are needed.
	specs, err = bench.ParseFabricSpecs([]string{"name=big,fabric-links=2,fabric-unnum=true"})
	require.NoError(t, err)

	_, err = bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.NoError(t, err)
}

func TestAllocatorIndexBounds(t *testing.T) {
	t.Parallel()

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 1))
	require.NoError(t, err)

	_, err = a.SwitchMgmtIP(1, 0)
	require.ErrorContains(t, err, "slot 1 out of range")

	_, err = a.SwitchMgmtIP(0, bench.StrideSwitches)
	require.ErrorContains(t, err, "out of range")

	_, err = a.LeafASN(0, bench.StrideLeaves)
	require.ErrorContains(t, err, "out of range")
}
