// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// IPs for ~1000 switches, ASNs are for 433 leafs
var DefaultConfig = fabapi.FabConfig{
	Control: fabapi.ControlConfig{
		ManagementSubnet:  "172.30.0.0/21", // 2046 hosts: 172.30.0.1 - 172.30.7.254
		VIP:               "172.30.0.1/32",
		TLSSAN:            []string{},
		KubeClusterSubnet: "172.28.0.0/16",
		KubeServiceSubnet: "172.29.0.0/16",
		KubeClusterDNS:    "172.29.0.10",
		DummySubnet:       "172.30.127.0/24",
		DefaultUser:       fabapi.ControlUser{},
	},
	Registry: fabapi.RegistryConfig{},
	Fabric: fabapi.FabricConfig{
		Mode:                fmeta.FabricModeSpineLeaf,
		ManagementDHCPStart: "172.30.4.0", // second half of the management subnet
		ManagementDHCPEnd:   "172.30.7.254",
		SpineASN:            65100, // TODO probably switch to 32-bit ASNs
		LeafASNStart:        65101,
		LeafASNEnd:          65534,             // only 433 leafs
		ProtocolSubnet:      "172.30.8.0/22",   // 1022 hosts: 172.30.8.1 - 172.30.11.254
		VTEPSubnet:          "172.30.12.0/22",  // 1022 hosts: 172.30.12.1 - 172.30.15.254
		FabricSubnet:        "172.30.128.0/17", // 16384 /31 subnets: 172.30.128.1 - 172.30.255.254
		BaseVPCCommunity:    "50000:0",         // TODO make sure it's really used
		VPCIRBVLANs: []fmeta.VLANRange{
			{From: 3000, To: 3999},
		},
		VPCWorkaroundVLANs: []fmeta.VLANRange{
			{From: 100, To: 3999},
		},
		VPCWorkaroundSubnet: "172.30.112.0/19",   // 4096 /31 subnets: 172.30.96.1 - 172.30.127.254 // TODO make sure it's really used
		ESLAGMACBase:        "f2:00:00:00:00:00", // TODO make sure it's really used
		ESLAGESIPrefix:      "00:f2:00:00:",      // TODO make sure it's really used
		DefaultSwitchUsers:  map[string]fabapi.SwitchUser{},
		DefaultAlloyConfig:  fmeta.AlloyConfig{},
	},
}
