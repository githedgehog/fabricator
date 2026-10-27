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

// Gateway VM NICs except the management one are on the root bus, so each is in its own IOMMU group for vfio-pci.
// The igb NICs expose SR-IOV VFs at PF devfn + 0x80 with stride 2, i.e. in slots PF+16 and PF+17, and QEMU 10.x
// rejects a PF whose VFs overflow the bus or overlap another PF's VFs, so the PF slots are spaced by 2. The first
// slot is the one after the fixed devices (virtio rng and blk, two DMI bridges) and the last is 0xd, as slot 0xe
// puts a VF on the q35 LPC device at slot 31.
const (
	GatewayNICBaseSlot = 0x6
	GatewayNICSlotStep = 2
	gatewayNICLastSlot = 0xd
	GatewayMaxNICs     = (gatewayNICLastSlot-GatewayNICBaseSlot)/GatewayNICSlotStep + 1
)

// GatewayNICSlot is the PCI slot on the root bus of the gateway VM NIC, nicID is 1-based as 0 is the management NIC.
func GatewayNICSlot(nicID uint8) uint8 {
	return GatewayNICBaseSlot + GatewayNICSlotStep*(nicID-1)
}

func GatewayPCI(nicID uint8) string {
	if nicID == 0 {
		return "0000:02:00.0"
	}

	return fmt.Sprintf("0000:00:%02x.0", GatewayNICSlot(nicID))
}

func GatewayKernel(nicID uint8) string {
	if nicID == 0 {
		return "enp2s0"
	}

	return fmt.Sprintf("enp0s%d", GatewayNICSlot(nicID))
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
