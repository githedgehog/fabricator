// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubnetName is the VPC subnet name of the generator subnet with the given index
func SubnetName(idx int) string {
	return fmt.Sprintf("subnet-%d", idx)
}

// DHCPSubnetName is the name of the DHCPSubnet object the VPC controller creates for the subnet with the given index
func (c *Config) DHCPSubnetName(idx int) string {
	return fmt.Sprintf("%s--%s", c.VPCName(), SubnetName(idx))
}

// BuildVPC returns a VPC with one DHCP-enabled subnet per generator subnet, so a running fabric controller creates
// the matching DHCPSubnets. CIDRBase has to be inside the VPC's IPv4Namespace and VLANs inside its VLANNamespace.
func BuildVPC(c *Config) *vpcapi.VPC {
	vpc := &vpcapi.VPC{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       vpcapi.KindVPC,
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      c.VPCName(),
			Namespace: kmetav1.NamespaceDefault,
			Labels:    map[string]string{FakeLabel: "true"},
		},
		Spec: vpcapi.VPCSpec{
			Subnets: map[string]*vpcapi.VPCSubnet{},
		},
	}

	for i := range c.NumSubnets() {
		s := c.subnet(i)
		first := s.prefix.Addr()
		gw := first.Next()
		start := gw
		for range 9 {
			start = start.Next()
		}
		var last [4]byte
		binary.BigEndian.PutUint32(last[:], binary.BigEndian.Uint32(first.AsSlice())+4094)

		vpc.Spec.Subnets[SubnetName(i)] = &vpcapi.VPCSubnet{
			Subnet:  s.prefix.String(),
			Gateway: gw.String(),
			VLAN:    uint16(s.vlan), //nolint:gosec
			DHCP: vpcapi.VPCDHCP{
				Enable: true,
				Range: &vpcapi.VPCDHCPRange{
					Start: start.String(),
					End:   netip.AddrFrom4(last).String(),
				},
				Options: &vpcapi.VPCDHCPOptions{
					LeaseTimeSeconds: uint32(c.LeaseTime), //nolint:gosec
				},
			},
		}
	}

	return vpc
}
