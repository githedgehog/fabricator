package fab

import (
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/version"
)

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:          "v1.31.1-k3s1",
		Zot:          "v2.1.1",
		CertManager:  "v1.15.3",
		K9s:          "v0.32.5",
		Toolbox:      "latest",  // TODO use specific version, move to fabricator repo
		Reloader:     "v1.0.40", // TODO upgrade or get rid of?
		ControlProxy: "1.9.1",   // TODO use version starting with "v", upgrade or replace with better option
	},
	Fabricator: fabapi.FabricatorVersions{
		API:            meta.Version(version.Version),
		Controller:     meta.Version(version.Version),
		ControlISORoot: "v0.0.1", // TODO separate repo/versioning to stay up to date with Flatcar?
	},
	Fabric: fabapi.FabricVersions{ // TODO use version from fabric/version.Version? as a default
		API:          "v0.50.2",
		Controller:   "v0.50.2",
		DHCPD:        "v0.50.2",
		Boot:         "v0.50.2",
		Agent:        "v0.50.2",
		ControlAgent: "v0.50.2",
		Ctl:          "v0.50.2",
		Alloy:        "v1.1.1", // TODO upgrade to v1.4.x or newer
		NOS: map[string]meta.Version{
			// TODO some enums for NOS "types"?
			"sonic-bcm-base":   "base-bin-4.4.0",
			"sonic-bcm-campus": "campus-bin-4.4.0",
			"sonic-bcm-vs":     "vs-bin-4.4.0",
		},
	},
	VLAB: fabapi.VLABVersions{
		ONIE:    "test3", // TODO replace with proper version
		Flatcar: "v3975.2.1",
	},
}
