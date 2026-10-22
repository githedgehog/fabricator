// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"
	"net/netip"

	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
)

// BootPort is where fabric-boot serves ONIE and ZTP on the control VIP
const BootPort = 32000

// ManagementDHCPSubnetSpec is the spec of the management DHCPSubnet that fabric-dhcpd serves to the switches
func (f *Fabricator) ManagementDHCPSubnetSpec() (dhcpapi.DHCPSubnetSpec, error) {
	mgmt, err := f.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return dhcpapi.DHCPSubnetSpec{}, fmt.Errorf("parsing management subnet: %w", err)
	}

	controlVIP, err := f.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return dhcpapi.DHCPSubnetSpec{}, fmt.Errorf("parsing control VIP: %w", err)
	}

	static := map[string]dhcpapi.DHCPSubnetStatic{}
	for mac, ip := range f.Spec.Config.Control.ManagementSubnetStatic {
		static[mac] = dhcpapi.DHCPSubnetStatic{IP: ip}
	}

	return dhcpapi.DHCPSubnetSpec{
		Subnet:           dhcpapi.ManagementSubnet,
		CIDRBlock:        mgmt.Masked().String(),
		Gateway:          mgmt.Addr().Next().String(),
		StartIP:          string(f.Spec.Config.Fabric.ManagementDHCPStart),
		EndIP:            string(f.Spec.Config.Fabric.ManagementDHCPEnd),
		LeaseTimeSeconds: 1800, // 30 mins should be enough for ZTP needs
		ZTPBaseURL:       "http://" + netip.AddrPortFrom(controlVIP.Addr(), BootPort).String(),
		DNSServers:       []string{},
		TimeServers:      []string{},
		Static:           static,
	}, nil
}
