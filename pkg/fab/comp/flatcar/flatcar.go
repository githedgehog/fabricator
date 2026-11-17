// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package flatcar

import (
	_ "embed"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
)

const (
	ToolboxArchiveRef = "fabricator/toolbox"
	ToolboxArchiveBin = "toolbox.tar"
	ToolboxRef        = "toolbox"
	HostBGPArchiveRef = "fabricator/host-bgp"
	HostBGPArchiveBin = "host-bgp.tar"
	HostBGPRef        = "host-bgp"
	Home              = "/home/core"
	UpdateRef         = "fabricator/flatcar-update"
	UpdateBinName     = "flatcar_production_update.gz"
)

// VLABAssetTag is the SMBIOS chassis asset tag VLAB gives its control, gateway
// and server VMs, so that VLAB-only host configuration can match on it and is
// inert on any other machine, virtual or not.
const VLABAssetTag = "hedgehog-vlab"

// VLABVirtioNamesLinkPath and VLABVirtioNamesLink name virtio NICs on VLAB VMs
// the way the e1000 NICs VLAB used to emulate were named at the same PCI
// address, instead of the eth* names Flatcar's 98-virtio.link gives them. That
// keeps interface names - and the config referring to them - across a VLAB
// moving from e1000 to virtio-net. It is written on every install and upgrade
// but matches only VLAB VMs, so virtio NICs anywhere else keep their names.
//
// Only the first matching .link file applies, so its 11- prefix places it after
// the 10- ones fabricator and VLAB already write - an explicit name for a VLAB
// server NIC keeps winning - and before Flatcar's 98-virtio.link it overrides.
const (
	VLABVirtioNamesLinkPath = "/etc/systemd/network/11-vlab-virtio-names.link"
	VLABVirtioNamesLink     = `# Managed by Hedgehog Fabricator, applies to Hedgehog VLAB VMs only: names virtio NICs
# like systemd's 99-default.link names any other NIC, as e1000 ones were named before.
[Match]
Driver=virtio_net
Firmware=smbios-field(chassis_asset_tag = ` + VLABAssetTag + `)

[Link]
NamePolicy=keep kernel database onboard slot path
AlternativeNamesPolicy=database onboard slot path
MACAddressPolicy=persistent
`
)

// ToolboxImage and ToolboxTag are what the toolbox archive is tagged as
const (
	ToolboxImage = "ghcr.io/githedgehog/toolbox"
	ToolboxTag   = "latest"
)

//go:embed hhtoolbox.sh
var HHToolbox []byte

const (
	HHToolboxPath  = "/opt/bin/hhtoolbox"
	ToolboxEnvPath = "/etc/hedgehog/toolbox.env"

	// ToolboxHandoffPath is sourced by Flatcar's toolbox before it does anything else
	ToolboxHandoffPath = "/etc/default/toolbox"
	ToolboxHandoff     = `# Managed by Hedgehog Fabricator, hands Flatcar's toolbox over to the Hedgehog one
exec ` + HHToolboxPath + ` "$@"
`
)

func ToolboxVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.Toolbox
}

// ToolboxImageURL is always the control node registry one, so any node can pull it if it's not loaded yet
func ToolboxImageURL(f fabapi.Fabricator) (string, error) {
	regURL, err := comp.RegistryURL(f)
	if err != nil {
		return "", fmt.Errorf("getting registry URL: %w", err)
	}

	return comp.JoinURLParts(regURL, comp.RegistryPrefix, ToolboxRef) + ":" + string(ToolboxVersion(f)), nil
}

func ToolboxEnv(image string) string {
	return "# Managed by Hedgehog Fabricator\nTOOLBOX_IMAGE=" + image + "\n"
}

func HostBGPContainerVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.HostBGPContainer
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ToolboxRef: ToolboxVersion(cfg),
		HostBGPRef: HostBGPContainerVersion(cfg),
	}, nil
}

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Fabricator.Flatcar
}
