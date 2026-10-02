// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench_test

import (
	"fmt"
	"net/netip"
	"slices"
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

// ASNs do not come from fab.yaml, so its leaf ASN range is not a limit. On the
// stock config the binding one is the fabric link subnet: a /17 holds the /31s
// for exactly 8 numbered fabrics of the default shape.
func TestAllocatorCapacityOnStockDefaults(t *testing.T) {
	t.Parallel()

	for n := 1; n <= 8; n++ {
		_, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, n))
		require.NoError(t, err, "%d fabrics should fit on stock defaults", n)
	}

	_, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 9))
	require.ErrorContains(t, err, "fabricSubnet")
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
			a, err := four.LeafASN(slot, 0, idx)
			require.NoError(t, err)
			b, err := six.LeafASN(slot, 0, idx)
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

		xa, err := a.LeafASN(slot, 0, 0)
		require.NoError(t, err)
		ya, err := b.LeafASN(slot, 0, 0)
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

	// Slot s owns the block starting at 4200000000 + s*1000.
	asns, err := a.ASNs(0)
	require.NoError(t, err)
	require.Equal(t, bench.ASNBlock{
		Spines:    []uint32{4_200_000_001},
		Gateways:  []uint32{4_200_000_051},
		LeafStart: 4_200_000_100,
		LeafEnd:   4_200_000_163,
	}, asns)

	asn, err := a.LeafASN(0, 0, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(4_200_000_100), asn)

	asn, err = a.LeafASN(1, 0, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(4_200_001_100), asn)
}

// Each domain of a fabric is its own address unit, so a fabric with several
// takes consecutive units and pushes the next fabric along by as many. Its ASNs
// stay in the one per-fabric block: a spine and gateway ASN per domain, and the
// leaves of all its domains in one range.
func TestAllocatorDomains(t *testing.T) {
	t.Parallel()

	specs, err := bench.ParseFabricSpecs([]string{"name=f0,domains=3", "name=f1"})
	require.NoError(t, err)

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.NoError(t, err)
	require.Equal(t, uint(2), a.Slots())
	require.Equal(t, uint(4), a.Units())

	for domain, want := range []uint{0, 1, 2} {
		unit, err := a.Unit(0, uint(domain)) //nolint:gosec // tiny
		require.NoError(t, err)
		require.Equal(t, want, unit)
	}
	unit, err := a.Unit(1, 0)
	require.NoError(t, err)
	require.Equal(t, uint(3), unit, "the next fabric starts after every domain of the previous one")

	_, err = a.Unit(0, 3)
	require.ErrorContains(t, err, "domain 3 out of range")

	asns, err := a.ASNs(0)
	require.NoError(t, err)
	require.Equal(t, bench.ASNBlock{
		Spines:    []uint32{4_200_000_001, 4_200_000_002, 4_200_000_003},
		Gateways:  []uint32{4_200_000_051, 4_200_000_052, 4_200_000_053},
		LeafStart: 4_200_000_100,
		LeafEnd:   4_200_000_100 + 3*bench.StrideLeaves - 1,
	}, asns)

	asn, err := a.LeafASN(0, 2, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(4_200_000_100+2*bench.StrideLeaves), asn)

	// The second fabric's ASNs are keyed by its slot, not its unit.
	asn, err = a.LeafASN(1, 0, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(4_200_001_100), asn)

	// Addresses are keyed by unit: domain 1 of f0 sits a full stride after
	// domain 0.
	d0, err := a.SwitchMgmtIP(0, 0)
	require.NoError(t, err)
	d1, err := a.SwitchMgmtIP(1, 0)
	require.NoError(t, err)
	require.NotEqual(t, d0, d1)
}

// At the largest allowed domain count the leaf range still ends inside the
// fabric's ASN block, and the spine and gateway ASNs never reach each other or
// the leaves. Checked on the layout itself: stock fab.yaml runs out of
// management addresses well before MaxDomains, so no allocator gets this far.
func TestMaxDomainsFitASNBlock(t *testing.T) {
	t.Parallel()

	require.Less(t, bench.ASNSpineOffset+bench.MaxDomains-1, bench.ASNGatewayOffset, "spine ASNs reach the gateway ASNs")
	require.Less(t, bench.ASNGatewayOffset+bench.MaxDomains-1, bench.ASNLeafOffset, "gateway ASNs reach the leaf range")
	require.LessOrEqual(t, bench.ASNLeafOffset+bench.MaxDomains*bench.StrideLeaves, bench.StrideASNs, "leaf range overflows the block")

	_, err := bench.ParseFabricSpecs([]string{fmt.Sprintf("name=f0,domains=%d", bench.MaxDomains+1)})
	require.ErrorContains(t, err, "domains must be between")

	_, err = bench.ParseFabricSpecs([]string{"name=f0,domains=0"})
	require.ErrorContains(t, err, "domains must be between")
}

// Every leaf ASN the allocator hands out has to sit inside its own fabric's
// leaf range, and the spine and gateway ASNs outside every fabric's, or the
// Fabric and Switch webhooks refuse them.
func TestAllocatorASNBlocksAreConsistent(t *testing.T) {
	t.Parallel()

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 6))
	require.NoError(t, err)

	blocks := make([]bench.ASNBlock, 0, a.Slots())
	for slot := range a.Slots() {
		block, err := a.ASNs(slot)
		require.NoError(t, err)
		require.Equal(t, uint32(bench.StrideLeaves), block.LeafEnd-block.LeafStart+1)

		for idx := range uint(bench.StrideLeaves) {
			asn, err := a.LeafASN(slot, 0, idx)
			require.NoError(t, err)
			require.True(t, asn >= block.LeafStart && asn <= block.LeafEnd, "slot %d leaf %d ASN %d outside %d-%d", slot, idx, asn, block.LeafStart, block.LeafEnd)
		}

		blocks = append(blocks, block)
	}

	for i, block := range blocks {
		for j, other := range blocks {
			for _, asn := range append(slices.Clone(block.Spines), block.Gateways...) {
				require.False(t, asn >= other.LeafStart && asn <= other.LeafEnd, "slot %d ASN %d inside slot %d leaf range", i, asn, j)
			}
			if i != j {
				require.False(t, block.LeafStart <= other.LeafEnd && other.LeafStart <= block.LeafEnd, "slots %d and %d leaf ranges overlap", i, j)
			}
		}
	}

	// All of it is in the 32-bit private range, clear of the stock 16-bit ASNs.
	last := blocks[len(blocks)-1]
	require.GreaterOrEqual(t, blocks[0].Spines[0], uint32(bench.ASNPrivate32Start))
	require.LessOrEqual(t, last.LeafEnd, uint32(bench.ASNPrivate32End))
	require.Greater(t, blocks[0].Spines[0], fab.DefaultConfig.Fabric.LeafASNEnd)
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
	require.ErrorContains(t, err, "per-domain reservation is 96")

	// Few enough switches to pass the switch check, but too many leaves for the
	// leaf ASN and VTEP strides.
	specs, err = bench.ParseFabricSpecs([]string{"name=big,spines=1,leaves=65"})
	require.NoError(t, err)

	_, err = bench.NewAllocator(defaultFab(), 1, 0, 0, specs)
	require.ErrorContains(t, err, "per-domain reservation is 64")

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

func TestLastHostAddr(t *testing.T) {
	t.Parallel()

	for subnet, want := range map[string]string{
		"172.30.0.0/21": "172.30.7.254",
		"172.30.0.5/21": "172.30.7.254", // host bits are ignored
		"10.0.0.0/8":    "10.255.255.254",
		"192.0.2.0/30":  "192.0.2.2",
	} {
		got, err := bench.LastHostAddr(netip.MustParsePrefix(subnet))
		require.NoError(t, err, subnet)
		require.Equal(t, want, got.String(), subnet)
	}

	for _, subnet := range []string{"192.0.2.0/31", "192.0.2.1/32", "fd00::/64"} {
		_, err := bench.LastHostAddr(netip.MustParsePrefix(subnet))
		require.Error(t, err, subnet)
	}

	// The stock management subnet's last address stays clear of everything the
	// allocator hands out.
	f := defaultFab()
	subnet, err := f.Spec.Config.Control.ManagementSubnet.Parse()
	require.NoError(t, err)
	last, err := bench.LastHostAddr(subnet)
	require.NoError(t, err)

	a, err := bench.NewAllocator(f, 1, 0, 0, defaultSpecs(t, 8))
	require.NoError(t, err)
	top, err := a.SwitchMgmtIP(a.Units()-1, bench.StrideSwitches-1)
	require.NoError(t, err)
	require.Equal(t, -1, top.Addr().Compare(last))
}

func TestAllocatorIndexBounds(t *testing.T) {
	t.Parallel()

	a, err := bench.NewAllocator(defaultFab(), 1, 0, 0, defaultSpecs(t, 1))
	require.NoError(t, err)

	_, err = a.SwitchMgmtIP(1, 0)
	require.ErrorContains(t, err, "unit 1 out of range")

	_, err = a.SwitchMgmtIP(0, bench.StrideSwitches)
	require.ErrorContains(t, err, "out of range")

	_, err = a.LeafASN(0, 0, bench.StrideLeaves)
	require.ErrorContains(t, err, "out of range")

	_, err = a.LeafASN(0, 1, 0)
	require.ErrorContains(t, err, "domain 1 out of range")

	_, err = a.ASNs(1)
	require.ErrorContains(t, err, "slot 1 out of range")
}
