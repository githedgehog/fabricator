// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"errors"
	"fmt"
	"net/netip"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const FakeLabel = "loadgen.githedgehog.com/fake"

// FakeSwitchOpts controls the per-leaf unique fields of the fake Switch objects
type FakeSwitchOpts struct {
	// ProtocolBase and VTEPBase are the first protocol/VTEP IPs (as /32), incremented per fake leaf
	ProtocolBase netip.Addr
	VTEPBase     netip.Addr
	// ASNBase is the ASN of the first fake leaf, incremented per fake leaf
	ASNBase uint32
}

// FakeSwitchName is the name of the fake leaf emulating the relay with the given index
func (c *Config) FakeSwitchName(leaf int) string {
	return fmt.Sprintf("%s-leaf-%02d", c.VPCName(), leaf)
}

// BuildFakeSwitches clones a real leaf Switch into one Switch per emulated relay, so dhcpd puts the relay IPs on its
// allowlist. The clones keep profile and VLAN namespaces (to pass the webhook) and drop everything tying them to real
// hardware: boot MAC/serial, redundancy, groups, port settings.
func BuildFakeSwitches(c *Config, template *wiringapi.Switch, opts FakeSwitchOpts) ([]*wiringapi.Switch, error) {
	ipPrefix, err := netip.ParsePrefix(template.Spec.IP)
	if err != nil {
		return nil, fmt.Errorf("template switch %q has invalid spec.ip %q: %w", template.Name, template.Spec.IP, err)
	}
	if !opts.ProtocolBase.Is4() {
		return nil, errors.New("fake protocol base must be an IPv4 address") //nolint:err113
	}

	res := make([]*wiringapi.Switch, 0, c.Leaves)
	protocolIP, vtepIP := opts.ProtocolBase, opts.VTEPBase
	for leaf, relay := range c.RelayIPs() {
		sw := &wiringapi.Switch{
			TypeMeta: kmetav1.TypeMeta{
				Kind:       wiringapi.KindSwitch,
				APIVersion: wiringapi.GroupVersion.String(),
			},
			ObjectMeta: kmetav1.ObjectMeta{
				Name:      c.FakeSwitchName(leaf),
				Namespace: kmetav1.NamespaceDefault,
				Labels:    map[string]string{FakeLabel: "true"},
			},
			Spec: wiringapi.SwitchSpec{
				Role:           wiringapi.SwitchRoleServerLeaf,
				Description:    "fake leaf for DHCP load testing",
				Profile:        template.Spec.Profile,
				VLANNamespaces: append([]string(nil), template.Spec.VLANNamespaces...),
				ASN:            opts.ASNBase + uint32(leaf), //nolint:gosec
				IP:             netip.PrefixFrom(relay, ipPrefix.Bits()).String(),
				ProtocolIP:     netip.PrefixFrom(protocolIP, 32).String(),
			},
		}
		protocolIP = protocolIP.Next()
		if opts.VTEPBase.IsValid() {
			sw.Spec.VTEPIP = netip.PrefixFrom(vtepIP, 32).String()
			vtepIP = vtepIP.Next()
		}
		res = append(res, sw)
	}

	return res, nil
}
