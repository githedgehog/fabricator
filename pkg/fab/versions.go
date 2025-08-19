// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	"strings"

	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/version"
)

var (
	FabricatorVersion = meta.Version(version.Version)
	FabricVersion     = meta.Version("v0.88.1")
	GatewayVersion    = meta.Version("v0.17.0")
	DataplaneVersion  = meta.Version("main.x86_64-unknown-linux-gnu.debug.f27f76cd91213cf4dc85d0dab95e7c70ede30efc")
	FRRVersion        = meta.Version("0ba323e489ea2baf3f85fc42ff23aff674a25690.debug")
	BCMSONiCVersion   = meta.Version("v4.5.0")
	CLSSONiCVersion   = meta.Version("v4.1.0-beta1-hh")

	// Upgrade constraints, "-0" to include pre-releases
	FabricatorCtrlConstraint = ">=0.40.0-0"
	FabricAgentConstraint    = ">=0.81.1-0"
	FabricNOSConstraint      = ">=4.5.0-0" // -0 is to allow -Enterprise_Base suffix
)

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:         "v1.33.3-k3s1",
		Zot:         "v2.1.1",
		CertManager: "v1.18.2",
		K9s:         "v0.50.7",
		Toolbox:     "v0.6.0",
		Reloader:    "v1.0.40", // TODO upgrade or get rid of?
		NTP:         "v0.0.2",
		NTPChart:    FabricatorVersion,
		Alloy:       "v1.9.2",
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
		ProxyChart: FabricVersion, // TODO switch to a better proxy
		Proxy:      "1.9.1",       // TODO use version starting with "v"
		NOS: map[fmeta.NOSType]meta.Version{
			fmeta.NOSTypeSONiCBCMVS:           BCMSONiCVersion,
			fmeta.NOSTypeSONiCBCMBase:         BCMSONiCVersion,
			fmeta.NOSTypeSONiCBCMCampus:       BCMSONiCVersion,
			fmeta.NOSTypeSONiCCLSPlusVS:       CLSSONiCVersion,
			fmeta.NOSTypeSONiCCLSPlusBroadcom: CLSSONiCVersion,
			fmeta.NOSTypeSONiCCLSPlusMarvell:  CLSSONiCVersion,
		},
		ONIE: map[string]meta.Version{
			switchprofile.DellS5232FON.Spec.Platform:         "v0.1.0",
			switchprofile.DellS5248FON.Spec.Platform:         "v0.1.0",
			switchprofile.DellZ9332FON.Spec.Platform:         "v0.1.0",
			switchprofile.CelesticaDS2000.Spec.Platform:      "v0.4.0",
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
		Dataplane:  DataplaneVersion,
		FRR:        FRRVersion,
	},
	VLAB: fabapi.VLABVersions{
		ONIE:    "v0.2.0",
		Flatcar: "v4152.2.3",
	},
}

func CleanupFabricNOSVersion(version string) string {
	return strings.ReplaceAll(version, "_", "-")
}
