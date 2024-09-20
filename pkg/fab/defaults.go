package fab

import (
	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

var DefaultConfig = fabapi.FabConfig{
	Control: fabapi.ControlConfig{
		ManagementSubnet:  "172.30.1.0/24",
		VIP:               "172.30.1.1",
		TLSSAN:            []string{}, // TODO
		KubeClusterSubnet: "172.28.0.0/16",
		KubeServiceSubnet: "172.29.0.0/16",
		KubeClusterDNS:    "172.29.0.10",
		DefaultUser:       fabapi.ControlUser{},
	},
	Registry: fabapi.RegistryConfig{},
	Fabric: fabapi.FabricConfig{
		SpineASN:            65100,
		LeafASNStart:        65101,
		LeafASNEnd:          65999,
		ProtocolSubnet:      "TBD",               // TODO
		VTEPSubnet:          "TBD",               // TODO
		FabricSubnet:        "TBD",               // TODO
		BaseVPCCommunity:    "50000:0",           // TODO make sure it's really used
		VPCIRBVLANs:         []fmeta.VLANRange{}, // TODO
		VPCWorkaroundVLANs:  []fmeta.VLANRange{}, // TODO
		VPCWorkaroundSubnet: "172.30.240.0/20",   // TODO make sure it's really used
		ESLAGMACBase:        "f2:00:00:00:00:00", // TODO make sure it's really used
		ESLAGESIPrefix:      "00:f2:00:00:",      // TODO make sure it's really used
		DefaultSwitchUsers:  map[string]fabapi.SwitchUser{},
	},
}
