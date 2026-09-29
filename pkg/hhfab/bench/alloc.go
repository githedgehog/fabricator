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

// Each fabric is given a fixed-size block out of every address and ASN pool,
// sized for the worst case fabric shape rather than the shape actually
// requested. That keeps allocation pure arithmetic with no persisted state
// while guaranteeing that adding a fabric, or changing one fabric's shape,
// never renumbers another.
//
// The reserved worst case is the default shape, so reserving costs nothing at
// the default: the binding limit on stock fab.yaml is the leaf ASN range,
// which allows 6 fabrics either way.
const (
	StrideSwitches = 96   // 32 spines + 64 leaves
	StrideLeaves   = 64   // leaf ASNs and VTEP IPs
	StrideLinks    = 2048 // 64 leaves * 32 spines * 1 fabric link
)

// mgmtReservedAfterVIP mirrors hydrate(), which advances 4 addresses past the
// control VIP before allocating anything.
const mgmtReservedAfterVIP = 4

// Allocator hands out management, protocol, VTEP and fabric-link addresses and
// leaf ASNs. Every value is a pure function of (slot, index), where slot is the
// fabric's position in the --fabric list.
type Allocator struct {
	specs []FabricSpec

	mgmtSubnet    netip.Prefix
	mgmtDHCPStart netip.Addr
	mgmtBase      netip.Addr

	protoSubnet netip.Prefix
	protoBase   netip.Addr

	vtepSubnet netip.Prefix
	vtepBase   netip.Addr

	fabricSubnet netip.Prefix
	fabricBase   netip.Addr

	spineASN     uint32
	leafASNStart uint32
	leafASNEnd   uint32
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

	a := &Allocator{specs: specs}

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

	a.spineASN = f.Spec.Config.Fabric.SpineASN
	a.leafASNStart = f.Spec.Config.Fabric.LeafASNStart
	a.leafASNEnd = f.Spec.Config.Fabric.LeafASNEnd

	if err := a.preflight(); err != nil {
		return nil, err
	}

	return a, nil
}

// Slots is the number of fabrics this allocator was built for.
func (a *Allocator) Slots() uint {
	return uint(len(a.specs))
}

// SpineASN is the ASN shared by every spine in every fabric.
func (a *Allocator) SpineASN() uint32 {
	return a.spineASN
}

// preflight checks each fabric against the stride reservation and the whole
// request against every pool, failing with a message that names the fab.yaml
// field to change and the value it needs.
func (a *Allocator) preflight() error {
	for _, spec := range a.specs {
		if spec.Switches() > StrideSwitches {
			return fmt.Errorf("fabric %q has %d switches (%d spines + %d leaves), per-fabric reservation is %d", //nolint:err113
				spec.Name, spec.Switches(), spec.Spines, spec.Leaves, StrideSwitches)
		}
		if spec.Leaves > StrideLeaves {
			return fmt.Errorf("fabric %q has %d leaves, per-fabric reservation is %d", //nolint:err113
				spec.Name, spec.Leaves, StrideLeaves)
		}
		if !spec.FabricUnnum && spec.FabricLinksTotal() > StrideLinks {
			return fmt.Errorf("fabric %q needs %d fabric links (%d spines x %d leaves x %d links), per-fabric reservation is %d; use fabric-unnum=true to allocate no link addresses", //nolint:err113
				spec.Name, spec.FabricLinksTotal(), spec.Spines, spec.Leaves, spec.FabricLinks, StrideLinks)
		}
	}

	slots := a.Slots()

	// Leaf ASNs.
	needASNs := uint64(slots) * StrideLeaves
	haveASNs := uint64(a.leafASNEnd) - uint64(a.leafASNStart) + 1
	if needASNs > haveASNs {
		required := uint64(a.leafASNStart) + needASNs - 1
		note := ""
		if required > math.MaxUint16 {
			note = " (note: >65535 requires 4-byte ASNs)"
		}

		return fmt.Errorf("%d fabrics need %d leaf ASNs, fab.yaml leafASNStart/End (%d-%d) gives %d. Raise leafASNEnd to >= %d%s", //nolint:err113
			slots, needASNs, a.leafASNStart, a.leafASNEnd, haveASNs, required, note)
	}

	// Management IPs: bench switches must stay inside the subnet and below the
	// DHCP range.
	if err := a.checkPool("management", a.mgmtBase, uint64(slots)*StrideSwitches, a.mgmtSubnet, "managementSubnet"); err != nil {
		return err
	}
	lastMgmt, err := addrAdd(a.mgmtBase, uint64(slots)*StrideSwitches-1)
	if err != nil {
		return fmt.Errorf("computing last management IP: %w", err)
	}
	if lastMgmt.Compare(a.mgmtDHCPStart) >= 0 {
		return fmt.Errorf("%d fabrics need management IPs up to %s, which reaches the DHCP range starting at %s. Raise fab.yaml managementDHCPStart or widen managementSubnet", //nolint:err113
			slots, lastMgmt, a.mgmtDHCPStart)
	}

	if err := a.checkPool("protocol", a.protoBase, uint64(slots)*StrideSwitches, a.protoSubnet, "protocolSubnet"); err != nil {
		return err
	}

	if err := a.checkPool("VTEP", a.vtepBase, uint64(slots)*StrideLeaves, a.vtepSubnet, "vtepSubnet"); err != nil {
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
		if err := a.checkPool("fabric link", a.fabricBase, uint64(slots)*StrideLinks*2, a.fabricSubnet, "fabricSubnet"); err != nil {
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
		return fmt.Errorf("%d fabrics need %d %s addresses (%s - %s), which does not fit in fab.yaml %s (%s). Widen it", //nolint:err113
			a.Slots(), count, what, base, last, field, subnet)
	}

	return nil
}

// SwitchMgmtIP returns the management IP for a switch, as a prefix in the
// management subnet. switchIdx is the switch's index within its fabric, spines
// first then leaves.
func (a *Allocator) SwitchMgmtIP(slot, switchIdx uint) (netip.Prefix, error) {
	if err := a.checkIdx(slot, switchIdx, StrideSwitches); err != nil {
		return netip.Prefix{}, err
	}

	addr, err := addrAdd(a.mgmtBase, uint64(slot)*StrideSwitches+uint64(switchIdx))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("management IP for slot %d switch %d: %w", slot, switchIdx, err)
	}

	return netip.PrefixFrom(addr, a.mgmtSubnet.Bits()), nil
}

// SwitchProtocolIP returns the /32 protocol IP for a switch.
func (a *Allocator) SwitchProtocolIP(slot, switchIdx uint) (netip.Prefix, error) {
	if err := a.checkIdx(slot, switchIdx, StrideSwitches); err != nil {
		return netip.Prefix{}, err
	}

	addr, err := addrAdd(a.protoBase, uint64(slot)*StrideSwitches+uint64(switchIdx))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("protocol IP for slot %d switch %d: %w", slot, switchIdx, err)
	}

	return netip.PrefixFrom(addr, 32), nil
}

// LeafVTEPIP returns the /32 VTEP IP for a leaf. Spines have none.
func (a *Allocator) LeafVTEPIP(slot, leafIdx uint) (netip.Prefix, error) {
	if err := a.checkIdx(slot, leafIdx, StrideLeaves); err != nil {
		return netip.Prefix{}, err
	}

	addr, err := addrAdd(a.vtepBase, uint64(slot)*StrideLeaves+uint64(leafIdx))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("VTEP IP for slot %d leaf %d: %w", slot, leafIdx, err)
	}

	return netip.PrefixFrom(addr, 32), nil
}

// LeafASN returns the ASN for a leaf.
func (a *Allocator) LeafASN(slot, leafIdx uint) (uint32, error) {
	if err := a.checkIdx(slot, leafIdx, StrideLeaves); err != nil {
		return 0, err
	}

	// Bounded by checkIdx above and by the ASN capacity check in preflight.
	return a.leafASNStart + uint32(slot)*StrideLeaves + uint32(leafIdx), nil //nolint:gosec
}

// FabricLinkIPs returns the /31 pair for one fabric link: the spine side and
// the leaf side, in that order.
func (a *Allocator) FabricLinkIPs(slot, linkIdx uint) (netip.Prefix, netip.Prefix, error) {
	if err := a.checkIdx(slot, linkIdx, StrideLinks); err != nil {
		return netip.Prefix{}, netip.Prefix{}, err
	}

	offset := (uint64(slot)*StrideLinks + uint64(linkIdx)) * 2

	spine, err := addrAdd(a.fabricBase, offset)
	if err != nil {
		return netip.Prefix{}, netip.Prefix{}, fmt.Errorf("fabric link IP for slot %d link %d: %w", slot, linkIdx, err)
	}
	leaf, err := addrAdd(a.fabricBase, offset+1)
	if err != nil {
		return netip.Prefix{}, netip.Prefix{}, fmt.Errorf("fabric link IP for slot %d link %d: %w", slot, linkIdx, err)
	}

	return netip.PrefixFrom(spine, 31), netip.PrefixFrom(leaf, 31), nil
}

func (a *Allocator) checkIdx(slot, idx, stride uint) error {
	if slot >= a.Slots() {
		return fmt.Errorf("slot %d out of range, have %d fabrics", slot, a.Slots()) //nolint:err113
	}
	if idx >= stride {
		return fmt.Errorf("index %d out of range, stride is %d", idx, stride) //nolint:err113
	}

	return nil
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
