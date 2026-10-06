// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/netip"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// Addresses are handed out per unit: one domain of one fabric, in --fabric
// order, so a fabric with N domains takes N consecutive units. Each unit is
// given a fixed-size block out of every address pool, sized for the worst case
// domain shape rather than the shape actually requested. That keeps allocation
// pure arithmetic with no persisted state while guaranteeing that adding a
// fabric, or changing one domain's shape, never renumbers another. Changing a
// fabric's domain count does shift the fabrics after it, but domains are
// immutable on an existing Fabric and Switch, so that needs a clean anyway.
//
// The reserved worst case is the default shape, so reserving costs nothing at
// the default.
const (
	StrideSwitches = 96   // 32 spines + 64 leaves
	StrideLeaves   = 64   // leaf ASNs and VTEP IPs
	StrideLinks    = 2048 // 64 leaves * 32 spines * 1 fabric link
)

// Every bench fabric is its own Fabric object with its own ASNs, taken from the
// 32-bit private range (RFC 6996) rather than fab.yaml. That range is far
// larger than any bench will need and disjoint from the 16-bit ASNs the stock
// config gives the default fabric, so bench fabrics never compete with it or
// with each other.
//
// ASNs are per fabric rather than per unit, since the Fabric object owns them.
// Each fabric gets a block of StrideASNs, which keeps it readable - fabric slot
// s starts at ASNPrivate32Start + s*1000. Domain d's spine ASN is base+1+d and
// its gateway ASN base+51+d, and the leaves of every domain share one range at
// ASNLeafOffset, StrideLeaves per domain in domain order.
const (
	ASNPrivate32Start = 4_200_000_000
	ASNPrivate32End   = 4_294_967_294

	StrideASNs       = 1000
	ASNSpineOffset   = 1
	ASNGatewayOffset = 51
	ASNLeafOffset    = 100
)

// ASNBlock is one fabric's ASNs: what its Fabric object declares and what its
// switches use. Spines and Gateways are indexed by domain.
type ASNBlock struct {
	Spines    []uint32 // shared by every spine of a domain
	Gateways  []uint32 // reserved for gateways, which the bench does not create
	LeafStart uint32
	LeafEnd   uint32
}

// mgmtReservedAfterVIP mirrors hydrate(), which advances 4 addresses past the
// control VIP before allocating anything.
const mgmtReservedAfterVIP = 4

// Allocator hands out management, protocol, VTEP and fabric-link addresses and
// ASNs. Every address is a pure function of (unit, index), where unit is one
// domain of one fabric; every ASN of (slot, ...), where slot is the fabric's
// position in the --fabric list.
type Allocator struct {
	specs []FabricSpec
	units []uint // first unit of each fabric, by slot

	mgmtSubnet    netip.Prefix
	mgmtDHCPStart netip.Addr
	mgmtBase      netip.Addr

	protoSubnet netip.Prefix
	protoBase   netip.Addr

	vtepSubnet netip.Prefix
	vtepBase   netip.Addr

	fabricSubnet netip.Prefix
	fabricBase   netip.Addr

	// The default fabric's ASNs from fab.yaml, used instead of the per-fabric
	// blocks when every switch goes into the default fabric.
	defaultSpineASN     uint32
	defaultLeafASNStart uint32
	defaultLeafASNEnd   uint32
}

// NewAllocator parses the pools out of the cluster's Fabricator object and
// validates that the requested fabrics fit. controls and nodes are the number
// of ControlNodes and FabNodes (both consume a management IP in hydrate order);
// gateways is how many of those nodes have the gateway role, since gateways
// also consume a protocol and a VTEP IP.
func NewAllocator(f fabapi.Fabricator, controls, nodes, gateways uint, specs []FabricSpec) (*Allocator, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("at least one fabric is required") //nolint:err113
	}

	a := &Allocator{specs: specs, units: make([]uint, 0, len(specs))}

	next := uint(0)
	for _, spec := range specs {
		a.units = append(a.units, next)
		next += spec.Domains
	}

	vip, err := f.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing control VIP: %w", err)
	}
	if a.mgmtSubnet, err = f.Spec.Config.Control.ManagementSubnet.Parse(); err != nil {
		return nil, fmt.Errorf("parsing management subnet: %w", err)
	}
	if a.mgmtDHCPStart, err = f.Spec.Config.Fabric.ManagementDHCPStart.Parse(); err != nil {
		return nil, fmt.Errorf("parsing management DHCP start: %w", err)
	}
	if a.protoSubnet, err = f.Spec.Config.Fabric.ProtocolSubnet.Parse(); err != nil {
		return nil, fmt.Errorf("parsing protocol subnet: %w", err)
	}
	if a.vtepSubnet, err = f.Spec.Config.Fabric.VTEPSubnet.Parse(); err != nil {
		return nil, fmt.Errorf("parsing VTEP subnet: %w", err)
	}
	if a.fabricSubnet, err = f.Spec.Config.Fabric.FabricSubnet.Parse(); err != nil {
		return nil, fmt.Errorf("parsing fabric subnet: %w", err)
	}

	a.defaultSpineASN = f.Spec.Config.Fabric.SpineASN
	a.defaultLeafASNStart = f.Spec.Config.Fabric.LeafASNStart
	a.defaultLeafASNEnd = f.Spec.Config.Fabric.LeafASNEnd

	// hydrate() reserves 4 addresses after the VIP, then gives one management
	// IP to each ControlNode and FabNode, in that order. Bench switches start
	// immediately after.
	if a.mgmtBase, err = addrAdd(vip.Addr(), uint64(mgmtReservedAfterVIP+controls+nodes)); err != nil {
		return nil, fmt.Errorf("computing management base: %w", err)
	}

	// Control nodes take neither a protocol nor a VTEP IP; gateways take both.
	if a.protoBase, err = addrAdd(a.protoSubnet.Masked().Addr(), uint64(gateways)); err != nil {
		return nil, fmt.Errorf("computing protocol base: %w", err)
	}
	if a.vtepBase, err = addrAdd(a.vtepSubnet.Masked().Addr(), uint64(gateways)); err != nil {
		return nil, fmt.Errorf("computing VTEP base: %w", err)
	}
	a.fabricBase = a.fabricSubnet.Masked().Addr()

	if err := a.preflight(); err != nil {
		return nil, err
	}

	return a, nil
}

// Slots is the number of fabrics this allocator was built for.
func (a *Allocator) Slots() uint {
	return uint(len(a.specs))
}

// Units is the number of address units: the domains of every fabric.
func (a *Allocator) Units() uint {
	last := len(a.specs) - 1

	return a.units[last] + a.specs[last].Domains
}

// Unit returns the address unit of one domain of a fabric.
func (a *Allocator) Unit(slot, domain uint) (uint, error) {
	if slot >= a.Slots() {
		return 0, fmt.Errorf("slot %d out of range, have %d fabrics", slot, a.Slots()) //nolint:err113
	}
	if domains := a.specs[slot].Domains; domain >= domains {
		return 0, fmt.Errorf("domain %d out of range, fabric %q has %d", domain, a.specs[slot].Name, domains) //nolint:err113
	}

	return a.units[slot] + domain, nil
}

// preflight checks each domain against the stride reservation and the whole
// request against every pool, failing with a message that names the fab.yaml
// field to change and the value it needs.
func (a *Allocator) preflight() error {
	for _, spec := range a.specs {
		if spec.Domains < 1 || spec.Domains > MaxDomains {
			return fmt.Errorf("fabric %q has %d domains, the ASN block allows 1 to %d", //nolint:err113
				spec.Name, spec.Domains, MaxDomains)
		}
		if spec.Switches() > StrideSwitches {
			return fmt.Errorf("fabric %q has %d switches per domain (%d spines + %d leaves), per-domain reservation is %d", //nolint:err113
				spec.Name, spec.Switches(), spec.Spines, spec.Leaves, StrideSwitches)
		}
		if spec.Leaves > StrideLeaves {
			return fmt.Errorf("fabric %q has %d leaves per domain, per-domain reservation is %d", //nolint:err113
				spec.Name, spec.Leaves, StrideLeaves)
		}
		if !spec.FabricUnnum && spec.FabricLinksTotal() > StrideLinks {
			return fmt.Errorf("fabric %q needs %d fabric links per domain (%d spines x %d leaves x %d links), per-domain reservation is %d; use fabric-unnum=true to allocate no link addresses", //nolint:err113
				spec.Name, spec.FabricLinksTotal(), spec.Spines, spec.Leaves, spec.FabricLinks, StrideLinks)
		}
	}

	slots := a.Slots()
	units := a.Units()

	// ASNs come from the private range, not fab.yaml, so this only fails at a
	// fabric count no address pool could carry either.
	if last := uint64(ASNPrivate32Start) + uint64(slots)*StrideASNs - 1; last > ASNPrivate32End {
		return fmt.Errorf("%d fabrics need ASNs up to %d, past the end of the 32-bit private range %d", //nolint:err113
			slots, last, uint64(ASNPrivate32End))
	}

	// Management IPs: bench switches must stay inside the subnet and below the
	// DHCP range.
	if err := a.checkPool("management", a.mgmtBase, uint64(units)*StrideSwitches, a.mgmtSubnet, "managementSubnet"); err != nil {
		return err
	}
	lastMgmt, err := addrAdd(a.mgmtBase, uint64(units)*StrideSwitches-1)
	if err != nil {
		return fmt.Errorf("computing last management IP: %w", err)
	}
	if lastMgmt.Compare(a.mgmtDHCPStart) >= 0 {
		return fmt.Errorf("%d domains need management IPs up to %s, which reaches the DHCP range starting at %s. Raise fab.yaml managementDHCPStart or widen managementSubnet", //nolint:err113
			units, lastMgmt, a.mgmtDHCPStart)
	}

	if err := a.checkPool("protocol", a.protoBase, uint64(units)*StrideSwitches, a.protoSubnet, "protocolSubnet"); err != nil {
		return err
	}

	if err := a.checkPool("VTEP", a.vtepBase, uint64(units)*StrideLeaves, a.vtepSubnet, "vtepSubnet"); err != nil {
		return err
	}

	// Fabric links consume two addresses each, but only for numbered fabrics.
	numbered := false
	for _, spec := range a.specs {
		if !spec.FabricUnnum {
			numbered = true

			break
		}
	}
	if numbered {
		if err := a.checkPool("fabric link", a.fabricBase, uint64(units)*StrideLinks*2, a.fabricSubnet, "fabricSubnet"); err != nil {
			return err
		}
	}

	return nil
}

// checkPool verifies that count addresses starting at base stay inside subnet.
func (a *Allocator) checkPool(what string, base netip.Addr, count uint64, subnet netip.Prefix, field string) error {
	if count == 0 {
		return nil
	}

	last, err := addrAdd(base, count-1)
	if err != nil {
		return fmt.Errorf("computing last %s address: %w", what, err)
	}

	if !subnet.Contains(base) || !subnet.Contains(last) {
		return fmt.Errorf("%d fabrics (%d domains) need %d %s addresses (%s - %s), which does not fit in fab.yaml %s (%s). Widen it", //nolint:err113
			a.Slots(), a.Units(), count, what, base, last, field, subnet)
	}

	return nil
}

// SwitchMgmtIP returns the management IP for a switch, as a prefix in the
// management subnet. switchIdx is the switch's index within its domain, spines
// first then leaves.
func (a *Allocator) SwitchMgmtIP(unit, switchIdx uint) (netip.Prefix, error) {
	if err := a.checkIdx(unit, switchIdx, StrideSwitches); err != nil {
		return netip.Prefix{}, err
	}

	addr, err := addrAdd(a.mgmtBase, uint64(unit)*StrideSwitches+uint64(switchIdx))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("management IP for unit %d switch %d: %w", unit, switchIdx, err)
	}

	return netip.PrefixFrom(addr, a.mgmtSubnet.Bits()), nil
}

// SwitchProtocolIP returns the /32 protocol IP for a switch.
func (a *Allocator) SwitchProtocolIP(unit, switchIdx uint) (netip.Prefix, error) {
	if err := a.checkIdx(unit, switchIdx, StrideSwitches); err != nil {
		return netip.Prefix{}, err
	}

	addr, err := addrAdd(a.protoBase, uint64(unit)*StrideSwitches+uint64(switchIdx))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("protocol IP for unit %d switch %d: %w", unit, switchIdx, err)
	}

	return netip.PrefixFrom(addr, 32), nil
}

// LeafVTEPIP returns the /32 VTEP IP for a leaf. Spines have none.
func (a *Allocator) LeafVTEPIP(unit, leafIdx uint) (netip.Prefix, error) {
	if err := a.checkIdx(unit, leafIdx, StrideLeaves); err != nil {
		return netip.Prefix{}, err
	}

	addr, err := addrAdd(a.vtepBase, uint64(unit)*StrideLeaves+uint64(leafIdx))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("VTEP IP for unit %d leaf %d: %w", unit, leafIdx, err)
	}

	return netip.PrefixFrom(addr, 32), nil
}

// ASNs returns a fabric's ASN block. The leaf range is exactly the leaf
// reservation of its domains, so the Fabric object admits every leaf LeafASN
// hands out and nothing more.
func (a *Allocator) ASNs(slot uint) (ASNBlock, error) {
	if slot >= a.Slots() {
		return ASNBlock{}, fmt.Errorf("slot %d out of range, have %d fabrics", slot, a.Slots()) //nolint:err113
	}

	domains := a.specs[slot].Domains

	// Bounded by the slot check above and by the ASN capacity and domain count
	// checks in preflight.
	base := uint32(ASNPrivate32Start + uint64(slot)*StrideASNs) //nolint:gosec

	block := ASNBlock{
		Spines:    make([]uint32, 0, domains),
		Gateways:  make([]uint32, 0, domains),
		LeafStart: base + ASNLeafOffset,
		LeafEnd:   base + ASNLeafOffset + uint32(domains)*StrideLeaves - 1, //nolint:gosec
	}
	for domain := range uint32(domains) { //nolint:gosec
		block.Spines = append(block.Spines, base+ASNSpineOffset+domain)
		block.Gateways = append(block.Gateways, base+ASNGatewayOffset+domain)
	}

	return block, nil
}

// LeafASN returns the ASN for a leaf. leafIdx is the leaf's index within its
// domain.
func (a *Allocator) LeafASN(slot, domain, leafIdx uint) (uint32, error) {
	unit, err := a.Unit(slot, domain)
	if err != nil {
		return 0, err
	}
	if err := a.checkIdx(unit, leafIdx, StrideLeaves); err != nil {
		return 0, err
	}

	block, err := a.ASNs(slot)
	if err != nil {
		return 0, err
	}

	return block.LeafStart + uint32(domain*StrideLeaves+leafIdx), nil //nolint:gosec // bounded by Unit and checkIdx
}

// CheckDefaultFabric verifies that every fabric fits into the default fabric:
// one domain each, since the default fabric has only the one, and all their
// leaves within the fab.yaml leaf ASN range, which they share.
func (a *Allocator) CheckDefaultFabric() error {
	if a.defaultSpineASN == 0 || a.defaultLeafASNStart == 0 || a.defaultLeafASNEnd < a.defaultLeafASNStart {
		return fmt.Errorf("fab.yaml has no usable default fabric ASNs: spineASN %d, leafASNStart %d, leafASNEnd %d", //nolint:err113
			a.defaultSpineASN, a.defaultLeafASNStart, a.defaultLeafASNEnd)
	}

	leaves := uint64(0)
	for _, spec := range a.specs {
		if spec.Domains != 1 {
			return fmt.Errorf("fabric %q has %d domains, the default fabric has only one", spec.Name, spec.Domains) //nolint:err113
		}
		leaves += uint64(spec.Leaves)
	}

	if capacity := uint64(a.defaultLeafASNEnd) - uint64(a.defaultLeafASNStart) + 1; leaves > capacity {
		return fmt.Errorf("%d leaves need more ASNs than the %d in fab.yaml leafASNStart-leafASNEnd (%d-%d). Widen it", //nolint:err113
			leaves, capacity, a.defaultLeafASNStart, a.defaultLeafASNEnd)
	}

	return nil
}

// DefaultSpineASN is the ASN every spine has in the default fabric.
func (a *Allocator) DefaultSpineASN() uint32 {
	return a.defaultSpineASN
}

// DefaultLeafASN returns the ASN for a leaf in the default fabric, where the
// leaves of every fabric share the fab.yaml range, numbered in --fabric order.
// leafIdx is the leaf's index within its fabric.
func (a *Allocator) DefaultLeafASN(slot, leafIdx uint) (uint32, error) {
	if slot >= a.Slots() {
		return 0, fmt.Errorf("slot %d out of range, have %d fabrics", slot, a.Slots()) //nolint:err113
	}
	if leafIdx >= a.specs[slot].Leaves {
		return 0, fmt.Errorf("leaf %d out of range, fabric %q has %d", leafIdx, a.specs[slot].Name, a.specs[slot].Leaves) //nolint:err113
	}

	offset := uint64(leafIdx)
	for _, spec := range a.specs[:slot] {
		offset += uint64(spec.Leaves)
	}

	asn := uint64(a.defaultLeafASNStart) + offset
	if asn > uint64(a.defaultLeafASNEnd) {
		return 0, fmt.Errorf("leaf ASN %d is past fab.yaml leafASNEnd %d", asn, a.defaultLeafASNEnd) //nolint:err113
	}

	return uint32(asn), nil //nolint:gosec // bounded by defaultLeafASNEnd above
}

// FabricLinkIPs returns the /31 pair for one fabric link: the spine side and
// the leaf side, in that order.
func (a *Allocator) FabricLinkIPs(unit, linkIdx uint) (netip.Prefix, netip.Prefix, error) {
	if err := a.checkIdx(unit, linkIdx, StrideLinks); err != nil {
		return netip.Prefix{}, netip.Prefix{}, err
	}

	offset := (uint64(unit)*StrideLinks + uint64(linkIdx)) * 2

	spine, err := addrAdd(a.fabricBase, offset)
	if err != nil {
		return netip.Prefix{}, netip.Prefix{}, fmt.Errorf("fabric link IP for unit %d link %d: %w", unit, linkIdx, err)
	}
	leaf, err := addrAdd(a.fabricBase, offset+1)
	if err != nil {
		return netip.Prefix{}, netip.Prefix{}, fmt.Errorf("fabric link IP for unit %d link %d: %w", unit, linkIdx, err)
	}

	return netip.PrefixFrom(spine, 31), netip.PrefixFrom(leaf, 31), nil
}

func (a *Allocator) checkIdx(unit, idx, stride uint) error {
	if unit >= a.Units() {
		return fmt.Errorf("unit %d out of range, have %d domains", unit, a.Units()) //nolint:err113
	}
	if idx >= stride {
		return fmt.Errorf("index %d out of range, stride is %d", idx, stride) //nolint:err113
	}

	return nil
}

// LastHostAddr returns the last usable address of an IPv4 subnet, the one
// before its broadcast address. The bench puts it on the management bridge to
// reach the control node, since it is the address least likely to be taken by
// the control plane or the switches, which are numbered from the bottom.
func LastHostAddr(subnet netip.Prefix) (netip.Addr, error) {
	if !subnet.Addr().Is4() || subnet.Bits() > 30 {
		return netip.Addr{}, fmt.Errorf("need an IPv4 subnet of /30 or wider, got %s", subnet) //nolint:err113
	}

	raw := subnet.Masked().Addr().As4()
	broadcast := uint64(binary.BigEndian.Uint32(raw[:])) | (uint64(1)<<(32-subnet.Bits()) - 1)

	var out [4]byte
	binary.BigEndian.PutUint32(out[:], uint32(broadcast-1)) //nolint:gosec // a /30 or wider IPv4 subnet

	return netip.AddrFrom4(out), nil
}

// addrAdd returns base + n for an IPv4 address.
func addrAdd(base netip.Addr, n uint64) (netip.Addr, error) {
	if !base.Is4() {
		return netip.Addr{}, fmt.Errorf("only IPv4 is supported, got %s", base) //nolint:err113
	}

	raw := base.As4()
	sum := uint64(binary.BigEndian.Uint32(raw[:])) + n
	if sum > math.MaxUint32 {
		return netip.Addr{}, fmt.Errorf("address arithmetic overflowed: %s + %d", base, n) //nolint:err113
	}

	var out [4]byte
	binary.BigEndian.PutUint32(out[:], uint32(sum))

	return netip.AddrFrom4(out), nil
}
