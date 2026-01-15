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
	FabricVersion     = meta.Version("v0.101.0")
	GatewayVersion    = meta.Version("v0.36.1")
	DataplaneVersion  = meta.Version("v0.8.0")
	FRRVersion        = meta.Version("v0.4.0")
	BCMSONiCVersion   = meta.Version("v4.5.0")
	CLSSONiCVersion   = meta.Version("v4.2.1")

	// Upgrade constraints, "-0" to include pre-releases
	FabricatorCtrlConstraint = ">=0.41.3-0"
	FabricAgentConstraint    = ">=0.87.4-0"
	FabricNOSConstraint      = ">=4.5.0-0" // -0 is to allow -Enterprise_Base suffix
)

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:               "v1.35.0-k3s1",
		Zot:               "v2.1.11",
		ZotChart:          "v0.1.67-hh1",
		CertManager:       "v1.18.2",
		K9s:               "v0.50.16",
		Toolbox:           "v0.9.0",
		ReloaderChart:     "2.2.5",
		Reloader:          "v1.4.11",
		NTP:               "v0.0.4",
		NTPChart:          FabricatorVersion,
		Alloy:             "v1.12.0",
		ControlProxy:      "v1.11.2-hh2",
		ControlProxyChart: FabricatorVersion,
		BashCompletion:    "v2.16.0",
	},
	Fabricator: fabapi.FabricatorVersions{
		API:            FabricatorVersion,
		Controller:     FabricatorVersion,
		Ctl:            FabricatorVersion,
		NodeConfig:     FabricatorVersion,
		Pause:          "3.6", // wait image from k3s // TODO embed wait into node-config image?
		ControlUSBRoot: "v4459.2.1-hh1",
		Flatcar:        "v4459.2.1",
	},
	Fabric: fabapi.FabricVersions{
		API:        FabricVersion,
		Controller: FabricVersion,
		DHCPD:      FabricVersion,
		Boot:       FabricVersion,
		Agent:      FabricVersion,
		Ctl:        FabricVersion,
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
		ONIE:    "v0.2.1",
		Flatcar: "v4459.2.1",
	},
}

func CleanupFabricNOSVersion(version string) string {
	return strings.ReplaceAll(version, "_", "-")
}
