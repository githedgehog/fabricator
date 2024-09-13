package release

import (
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/version"
)

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:          "",
		Zot:          "",
		CertManager:  "",
		K9s:          "",
		Toolbox:      "",
		Reloader:     "",
		ControlProxy: "",
	},
	Fabricator: fabapi.FabricatorVersions{
		API:        meta.Version(version.Version),
		Controller: meta.Version(version.Version),
	},
	Fabric: fabapi.FabricVersions{
		API:           "",
		Controller:    "",
		KubeRBACProxy: "",
		DHCPD:         "",
		Boot:          "",
		Agent:         "",
		ControlAgent:  "",
		Ctl:           "",
		Alloy:         "",
		NOS:           map[string]meta.Version{},
	},
	VLAB: fabapi.VLABVersions{
		ONIE:    "",
		Flatcar: "",
	},
}
