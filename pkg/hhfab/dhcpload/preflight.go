// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"errors"
	"fmt"
	"net/netip"

	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

// CheckSwitchConflicts makes sure none of the IPs/ASNs of the fake switches is used by a real one
func CheckSwitchConflicts(fakes []*wiringapi.Switch, existing []wiringapi.Switch) error {
	type key struct{ kind, val string }
	used := map[key]string{}
	for _, sw := range existing {
		if sw.Labels[FakeLabel] != "" {
			continue
		}
		for kind, val := range map[string]string{"ip": sw.Spec.IP, "protocolIP": sw.Spec.ProtocolIP, "vtepIP": sw.Spec.VTEPIP} {
			if p, err := netip.ParsePrefix(val); err == nil {
				used[key{kind, p.Addr().String()}] = sw.Name
			}
		}
		if sw.Spec.ASN != 0 {
			used[key{"asn", fmt.Sprint(sw.Spec.ASN)}] = sw.Name
		}
	}

	var errs []error
	for _, sw := range fakes {
		for kind, val := range map[string]string{"ip": sw.Spec.IP, "protocolIP": sw.Spec.ProtocolIP, "vtepIP": sw.Spec.VTEPIP} {
			p, err := netip.ParsePrefix(val)
			if err != nil {
				continue
			}
			if other, ok := used[key{kind, p.Addr().String()}]; ok {
				errs = append(errs, fmt.Errorf("fake switch %s %s %s is used by switch %s", sw.Name, kind, p.Addr(), other)) //nolint:err113
			}
		}
		if other, ok := used[key{"asn", fmt.Sprint(sw.Spec.ASN)}]; ok {
			errs = append(errs, fmt.Errorf("fake switch %s ASN %d is used by switch %s", sw.Name, sw.Spec.ASN, other)) //nolint:err113
		}
	}

	return errors.Join(errs...)
}

// CheckManagement makes sure the relay IPs are on the management network (dhcpd only sees them on its management
// interface), outside of the management DHCP range and don't collide with the control node
func CheckManagement(relays []netip.Addr, mgmt netip.Prefix, dhcpStart, dhcpEnd netip.Addr, control netip.Addr) error {
	var errs []error
	for _, ip := range relays {
		switch {
		case !mgmt.Contains(ip):
			errs = append(errs, fmt.Errorf("relay IP %s is outside of the management subnet %s", ip, mgmt)) //nolint:err113
		case ip == mgmt.Masked().Addr() || ip == control:
			errs = append(errs, fmt.Errorf("relay IP %s is reserved or used by the control node", ip)) //nolint:err113
		case dhcpStart.IsValid() && dhcpEnd.IsValid() && ip.Compare(dhcpStart) >= 0 && ip.Compare(dhcpEnd) <= 0:
			errs = append(errs, fmt.Errorf("relay IP %s is in the management DHCP range %s-%s", ip, dhcpStart, dhcpEnd)) //nolint:err113
		}
	}

	return errors.Join(errs...)
}

// CheckNamespaces makes sure all subnets of the VPC fit into the IPv4 namespace and VLAN namespaces of the leaf, as
// the VPC webhook would otherwise reject it
func CheckNamespaces(c *Config, ipv4 []netip.Prefix, vlans []meta.VLANRange) error {
	var errs []error
	for i := range c.NumSubnets() {
		s := c.subnet(i)
		if !slicesAnyPrefixContains(ipv4, s.prefix) {
			errs = append(errs, fmt.Errorf("subnet %s is not inside the IPv4 namespace %v", s.prefix, ipv4)) //nolint:err113
		}
		if !vlanInRanges(s.vlan, vlans) {
			errs = append(errs, fmt.Errorf("VLAN %d is not inside the VLAN namespace %v", s.vlan, vlans)) //nolint:err113
		}
	}

	return errors.Join(errs...)
}

func slicesAnyPrefixContains(ns []netip.Prefix, p netip.Prefix) bool {
	for _, n := range ns {
		if n.Bits() <= p.Bits() && n.Contains(p.Addr()) {
			return true
		}
	}

	return false
}

func vlanInRanges(vlan int, ranges []meta.VLANRange) bool {
	for _, r := range ranges {
		if vlan >= int(r.From) && vlan <= int(r.To) {
			return true
		}
	}

	return false
}
