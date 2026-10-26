// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package vlab

import (
	"fmt"

	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
)

const (
	FlatcarRef      = "fabricator/flatcar-vlab"
	ONIERef         = "fabricator/onie-vlab"
	CumulusVXImgRef = "cumulus-private/cumulus-vx-img"

	// NodeTypeAnnotation set to NodeTypeHW marks the VLAB nodes that are hardware instead of VMs
	NodeTypeAnnotation = "type.hhfab.githedgehog.com"
	NodeTypeHW         = "hw"
)

const (
	// gateway VM NICs except the management one are on the root bus, so each is in its own IOMMU group for vfio-pci
	GatewayNICBaseSlot = 0x10
	GatewayMaxNICs     = 0x1f - GatewayNICBaseSlot // 0x1f is the q35 chipset
)

func GatewayPCI(nicID uint8) string {
	if nicID == 0 {
		return "0000:02:00.0"
	}

	return fmt.Sprintf("0000:00:%02x.0", GatewayNICBaseSlot+nicID-1)
}

func GatewayKernel(nicID uint8) string {
	if nicID == 0 {
		return "enp2s0"
	}

	return fmt.Sprintf("enp0s%d", GatewayNICBaseSlot+nicID-1)
}

func FlatcarVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.VLAB.Flatcar
}

func ONIEVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.VLAB.ONIE
}

func CumulusVXImgVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Fabric.NOS[fmeta.NOSTypeCumulusVX]
}
