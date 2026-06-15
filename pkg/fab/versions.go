// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/version"
)

const (
	// User-facing release version for the whole project (as referenced in docs.hedgehog.cloud)
	Release = "26.03.0"

	// Fabric version used for all fabric components
	FabricVersion = meta.Version("v0.125.0")

	// Gateway Dataplane version (including WASM validator)
	DataplaneVersion = meta.Version("v0.22.0")

	// Gateway FRR version
	FRRVersion = meta.Version("v0.22.0")

	// Broadcom Enterprise SONiC version (including all flavors)
	BCMSONiCVersion = meta.Version("v4.5.2")

	// Celestica SONiC+ version (including all flavors)
	CLSSONiCVersion = meta.Version("v5.0.0")

	// NVIDIA Cumulus version (including all flavors)
	CumulusVersion = meta.Version("v5.16.0")
)

// Fabricator version is set at build time via ldflags
var FabricatorVersion = meta.Version(version.Version)

// Upgrade constraints, "-0" to include pre-releases
const (
	FabricatorCtrlConstraint = ">=0.45.5-0"
	FabricAgentConstraint    = ">=0.115.4-0"
	FabricNOSConstraint      = ">=4.5.0-0" // -0 is to allow -Enterprise_Base suffix
)

var Versions = fabapi.Versions{
	Platform: fabapi.PlatformVersions{
		K3s:               "v1.36.1-k3s1",
		Zot:               "v2.1.16",
		ZotChart:          "v0.1.67-hh1",
		CertManager:       "v1.20.2",
		K9s:               "v0.50.18",
		Toolbox:           "v0.13.0",
		ReloaderChart:     "2.2.11",
		Reloader:          "v1.4.16",
		NTP:               "v0.0.4",
		NTPChart:          FabricatorVersion,
		Alloy:             "v1.16.1",
		ControlProxy:      "v1.11.2-hh2",
		ControlProxyChart: FabricatorVersion,
		BashCompletion:    "v2.16.0",
		HostBGPContainer:  "v0.2.0",
	},
	Fabricator: fabapi.FabricatorVersions{
		API:            FabricatorVersion,
		Controller:     FabricatorVersion,
		Ctl:            FabricatorVersion,
		NodeConfig:     FabricatorVersion,
		Pause:          "3.6", // wait image from k3s // TODO embed wait into node-config image?
		ControlUSBRoot: "v4593.2.3-hh1",
		Flatcar:        "v4593.2.3",
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
			fmeta.NOSTypeCumulusVX:            CumulusVersion,
			fmeta.NOSTypeCumulusMlx:           CumulusVersion,
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
		Dataplane: DataplaneVersion,
		FRR:       FRRVersion,
	},
	VLAB: fabapi.VLABVersions{
		ONIE:    "v0.2.1",
		Flatcar: "v4593.2.3",
	},
}

func CleanupFabricNOSVersion(version string) string {
	return strings.ReplaceAll(version, "_", "-")
}

func ReleaseChannel() (string, error) {
	ver, err := semver.NewVersion(Release)
	if err != nil {
		return "", fmt.Errorf("parsing release version: %w", err)
	}

	return fmt.Sprintf("%d.%02d", ver.Major(), ver.Minor()), nil
}
