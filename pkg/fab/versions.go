// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/version"
)

var (
	FabricatorVersion = meta.Version(version.Version)
	FabricVersion     = meta.Version("v0.76.0")
	GatewayVersion    = meta.Version("v0.9.0")
)

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:         "v1.32.4-k3s1",
		Zot:         "v2.1.1",
		CertManager: "v1.17.1",
		K9s:         "v0.50.5",
		Toolbox:     "latest",  // TODO use specific version, move to fabricator repo
		Reloader:    "v1.0.40", // TODO upgrade or get rid of?
		NTP:         "v0.0.2",
		NTPChart:    FabricatorVersion,
	},
	Fabricator: fabapi.FabricatorVersions{
		API:            FabricatorVersion,
		Controller:     FabricatorVersion,
		Ctl:            FabricatorVersion,
		NodeConfig:     FabricatorVersion,
		Pause:          "3.6", // wait image from k3s // TODO embed wait into node-config image?
		ControlUSBRoot: "v4152.2.3-hh1",
		Flatcar:        "v4152.2.3",
	},
	Fabric: fabapi.FabricVersions{
		API:        FabricVersion,
		Controller: FabricVersion,
		DHCPD:      FabricVersion,
		Boot:       FabricVersion,
		Agent:      FabricVersion,
		Ctl:        FabricVersion,
		Alloy:      "v1.1.1",      // TODO upgrade to v1.4.x or newer
		ProxyChart: FabricVersion, // TODO switch to a better proxy
		Proxy:      "1.9.1",       // TODO use version starting with "v"
		NOS: map[string]meta.Version{
			string(fmeta.NOSTypeSONiCBCMVS):     "v4.4.2",
			string(fmeta.NOSTypeSONiCBCMBase):   "v4.4.2",
			string(fmeta.NOSTypeSONiCBCMCampus): "v4.4.2",
		},
		ONIE: map[string]meta.Version{
			switchprofile.DellS5232FON.Spec.Platform:         "v0.1.0",
			switchprofile.DellS5248FON.Spec.Platform:         "v0.1.0",
			switchprofile.DellZ9332FON.Spec.Platform:         "v0.1.0",
			switchprofile.CelesticaDS3000.Spec.Platform:      "v0.1.0",
			switchprofile.CelesticaDS4000.Spec.Platform:      "v0.1.0",
			switchprofile.CelesticaDS4101.Spec.Platform:      "v0.2.0",
			switchprofile.EdgecoreDCS203.Spec.Platform:       "v0.1.0",
			switchprofile.EdgecoreDCS204.Spec.Platform:       "v0.1.0",
			switchprofile.EdgecoreDCS501.Spec.Platform:       "v0.1.0",
			switchprofile.EdgecoreEPS203.Spec.Platform:       "v0.1.0",
			switchprofile.SupermicroSSEC4632SB.Spec.Platform: "v0.1.0", // same as DS3000
			switchprofile.VS.Spec.Platform:                   "v0.1.0",
		},
	},
	Gateway: fabapi.GatewayVersions{
		API:        GatewayVersion,
		Controller: GatewayVersion,
		Agent:      GatewayVersion,
		Dataplane:  "16", // TODO set actual version
		FRR:        "13", // TODO set actual version
	},
	VLAB: fabapi.VLABVersions{
		ONIE:    "v0.2.0",
		Flatcar: "v4152.2.3",
	},
}
