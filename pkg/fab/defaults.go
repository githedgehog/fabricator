// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// IPs for ~1000 switches. ASNs use one asdot block: spines from .1, gateways from .501 and leafs .1000-.9999
var DefaultConfig = fabapi.FabConfig{
	Control: fabapi.ControlConfig{
		ManagementSubnet:  "172.30.0.0/21", // 2046 hosts: 172.30.0.1 - 172.30.7.254
		VIP:               "172.30.0.1/32",
		TLSSAN:            []string{},
		KubeClusterSubnet: "172.28.0.0/16",
		KubeServiceSubnet: "172.29.0.0/16",
		KubeClusterDNS:    "172.29.0.10",
		DummySubnet:       "172.30.90.0/24",
		NoPassAuth:        false,
		DefaultUser:       fabapi.ControlUser{},
	},
	Registry: fabapi.RegistryConfig{
		Mode: fabapi.RegistryModeAirgap,
	},
	Fabric: fabapi.FabricConfig{
		Mode:                fmeta.FabricModeSpineLeaf,
		ManagementDHCPStart: "172.30.4.0", // second half of the management subnet
		ManagementDHCPEnd:   "172.30.7.254",
		SpineASN:            4200005633,        // 64087.1 in asdot
		LeafASNStart:        4200006632,        // 64087.1000 in asdot
		LeafASNEnd:          4200015631,        // 64087.9999 in asdot
		ProtocolSubnet:      "172.30.8.0/22",   // 1022 hosts: 172.30.8.1 - 172.30.11.254
		VTEPSubnet:          "172.30.12.0/22",  // 1022 hosts: 172.30.12.1 - 172.30.15.254
		ProxyExternalSubnet: "172.30.16.0/22",  // 516 /31 subnets: 172.30.16.0/31 - 172.30.19.254/31
		FabricSubnet:        "172.30.128.0/17", // 16384 /31 subnets: 172.30.128.1 - 172.30.255.254
		BaseVPCCommunity:    "50000:0",         // TODO make sure it's really used
		VPCIRBVLANs: []fmeta.VLANRange{
			{From: 3000, To: 3899},
		},
		ESLAGMACBase:       "f2:00:00:00:00:00", // TODO make sure it's really used
		ESLAGESIPrefix:     "00:f2:00:00:",      // TODO make sure it's really used
		MCLAGSessionSubnet: "172.30.95.0/31",
		DefaultSwitchUsers: map[string]fabapi.SwitchUser{},
		DefaultAlloyConfig: fmeta.AlloyConfig{},
		TH5WorkaroundVLANs: []fmeta.VLANRange{
			{From: 3900, To: 3966}, // the 3967-4094 range is reserved in BCM SONiC
		},
	},
	Gateway: fabapi.GatewayConfig{
		ASN: 4200006133, // 64087.501 in asdot
	},
}
