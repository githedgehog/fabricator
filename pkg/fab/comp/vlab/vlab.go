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
	// a NIC behind its own PCIe root port on the root bus is in its own IOMMU group for vfio-pci and has the port's
	// bus for its SR-IOV VFs, e.g. igb's
	RootPortBaseSlot = 0x10
	MaxRootPorts     = 0x1f - RootPortBaseSlot // 0x1f is the q35 chipset
	// buses 1 to 4 are the two DMI-to-PCI bridges with the PCI bridges behind them
	RootPortBaseBus = 5
)

func RootPortPCI(port uint8) string {
	return fmt.Sprintf("0000:%02x:00.0", RootPortBaseBus+port)
}

func RootPortKernel(port uint8) string {
	return fmt.Sprintf("enp%ds0", RootPortBaseBus+port)
}

// gateway VM NICs except the management one are behind root ports for vfio-pci
func GatewayPCI(nicID uint8) string {
	if nicID == 0 {
		return "0000:02:00.0"
	}

	return RootPortPCI(nicID - 1)
}

func GatewayKernel(nicID uint8) string {
	if nicID == 0 {
		return "enp2s0"
	}

	return RootPortKernel(nicID - 1)
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
