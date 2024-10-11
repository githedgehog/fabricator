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
	FabricVersion     = meta.Version("v0.51.2")
)

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:         "v1.31.1-k3s1",
		Zot:         "v2.1.1",
		CertManager: "v1.15.3",
		K9s:         "v0.32.5",
		Toolbox:     "latest",  // TODO use specific version, move to fabricator repo
		Reloader:    "v1.0.40", // TODO upgrade or get rid of?
		NTP:         "v0.0.2",
		NTPChart:    FabricatorVersion,
	},
	Fabricator: fabapi.FabricatorVersions{
		API:            FabricatorVersion,
		Controller:     FabricatorVersion,
		ControlUSBRoot: "v0.0.4", // TODO separate repo/versioning to stay up to date with Flatcar?
	},
	Fabric: fabapi.FabricVersions{
		API:        FabricVersion,
		Controller: FabricVersion,
		DHCPD:      FabricVersion,
		Boot:       FabricVersion,
		Agent:      FabricVersion,
		Ctl:        FabricVersion,
		Alloy:      "v1.1.1",      // TODO upgrade to v1.4.x or newer
		ProxyChart: FabricVersion, // TODO
		Proxy:      "1.9.1",       // TODO use version starting with "v", upgrade or replace with better option
		NOS: map[string]meta.Version{
			string(fmeta.NOSTypeSONiCBCMVS):     "v4.4.0",
			string(fmeta.NOSTypeSONiCBCMBase):   "v4.4.0",
			string(fmeta.NOSTypeSONiCBCMCampus): "v4.4.0",
		},
		ONIE: map[string]meta.Version{
			switchprofile.DellS5232FON.Spec.Platform:         "test4", // TODO update with proper version
			switchprofile.DellS5248FON.Spec.Platform:         "test4", // TODO update with proper version
			switchprofile.CelesticaDS3000.Spec.Platform:      "test1", // TODO update with proper version
			switchprofile.CelesticaDS4000.Spec.Platform:      "test1", // TODO update with proper version
			switchprofile.EdgecoreDCS203.Spec.Platform:       "test3", // TODO update with proper version
			switchprofile.EdgecoreDCS204.Spec.Platform:       "test2", // TODO update with proper version
			switchprofile.EdgecoreDCS501.Spec.Platform:       "test2", // TODO update with proper version
			switchprofile.EdgecoreEPS203.Spec.Platform:       "test2", // TODO update with proper version
			switchprofile.SupermicroSSEC4632SB.Spec.Platform: "test1", // TODO update with proper version
			switchprofile.VS.Spec.Platform:                   "test1", // TODO update with proper version
		},
	},
	VLAB: fabapi.VLABVersions{
		ONIE:    "test5", // TODO replace with proper version
		Flatcar: "v3975.2.1",
	},
}
