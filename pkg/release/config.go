package release

import (
	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

var Config = fabapi.FabConfig{
	Control: fabapi.ControlConfig{
		ManagementSubnet:  "172.30.1.0/24",
		ControlVIP:        "172.30.1.1",
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
		ProtocolSubnet:      "",        // TODO
		VTEPSubnet:          "",        // TODO
		FabricSubnet:        "",        // TODO
		BaseVPCCommunity:    "50000:0", // TODO make sure it's really used
		VPCIRBVLANs:         []fmeta.VLANRange{},
		VPCWorkaroundVLANs:  []fmeta.VLANRange{},
		VPCWorkaroundSubnet: "172.30.240.0/20",   // TODO make sure it's really used
		ESLAGMACBase:        "f2:00:00:00:00:00", // TODO make sure it's really used
		ESLAGESIPrefix:      "00:f2:00:00:",      // TODO make sure it's really used
		DefaultSwitchUsers:  []fabapi.SwitchUser{},
	},
}

const DevSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGpF2+9I1Nj4BcN7y6DjzTbq1VcUYIRGyfzId5ZoBEFj"

var DevConfig = fabapi.FabConfig{
	Control: fabapi.ControlConfig{
		DefaultUser: fabapi.ControlUser{
			PasswordHash:   "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36",
			AuthorizedKeys: []string{DevSSHKey},
		},
	},
	Fabric: fabapi.FabricConfig{
		DefaultSwitchUsers: []fabapi.SwitchUser{
			{
				Username:       "admin",
				PasswordHash:   "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36",
				Role:           "admin",
				AuthorizedKeys: []string{DevSSHKey},
			},
			{
				Username:       "op",
				PasswordHash:   "$5$oj/NxDtFw3eTyini$VHwdjWXSNYRxlFMu.1S5ZlGJbUF/CGmCAZIBroJlax4",
				Role:           "operator",
				AuthorizedKeys: []string{DevSSHKey},
			},
		},
	},
}
