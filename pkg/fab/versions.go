// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/version"
)

var FabricVersion = meta.Version("v0.50.7")

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:         "v1.31.1-k3s1",
		Zot:         "v2.1.1",
		CertManager: "v1.15.3",
		K9s:         "v0.32.5",
		Toolbox:     "latest",  // TODO use specific version, move to fabricator repo
		Reloader:    "v1.0.40", // TODO upgrade or get rid of?
	},
	Fabricator: fabapi.FabricatorVersions{
		API:            meta.Version(version.Version),
		Controller:     meta.Version(version.Version),
		ControlUSBRoot: "v0.0.3", // TODO separate repo/versioning to stay up to date with Flatcar?
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
			// TODO some const for platform names?
			"x86_64-kvm_x86_64-r0":          "test1", // TODO update with proper version
			"x86_64-dellemc_s5200_c3538-r0": "test1", // TODO update with proper version
		},
	},
	VLAB: fabapi.VLABVersions{
		ONIE:    "test3", // TODO replace with proper version
		Flatcar: "v3975.2.1",
	},
}
