// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package vlab

import (
	"fmt"
	"slices"
	"strconv"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	fmeta "go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	FlatcarRef      = "fabricator/flatcar-vlab"
	ONIERef         = "fabricator/onie-vlab"
	CumulusVXImgRef = "cumulus-private/cumulus-vx-img"

	// NodeTypeAnnotation set to NodeTypeHW marks the VLAB nodes that are hardware instead of VMs
	NodeTypeAnnotation = "type.hhfab.githedgehog.com"
	NodeTypeHW         = "hw"

	// GatewayHugePages1G is the 1G hugepages of the gateway VMs, set on their FabNodes and Gateways unless already there
	GatewayHugePages1G = 4
)

const (
	// a NIC behind its own PCIe root port on the root bus is in its own IOMMU group for vfio-pci and has the port's
	// bus for its SR-IOV VFs, e.g. igb's
	RootPortBaseSlot = 0x10
	// each root port with an I/O BAR device behind it, e.g. igb, takes a 4K I/O window, so the firmware only fits
	// about 9 next to the PCI bridges and rejects the NICs over it
	MaxRootPorts = 8
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

// IsGatewayVM reports whether the node is a gateway VM and not a hardware one of a hybrid VLAB
func IsGatewayVM(node fabapi.FabNode) bool {
	return slices.Contains(node.Spec.Roles, fabapi.NodeRoleGateway) && node.Annotations[NodeTypeAnnotation] != NodeTypeHW
}

// SetGatewayHugePages sets the gateway VM hugepages on its FabNode and Gateway, if there is one, unless already set.
// The value already set on either of them is used for the other one, so they match, the VLAB default otherwise for
// the 1G ones. Returns whether the FabNode and the Gateway are changed.
func SetGatewayHugePages(node *fabapi.FabNode, gw *gwapi.Gateway) (bool, bool) {
	nodeChanged, gwChanged := false, false
	for _, key := range []string{gwapi.AnnotationHugePages1G, gwapi.AnnotationHugePages2M} {
		val, ok := node.Annotations[key]
		if !ok && gw != nil {
			val, ok = gw.Annotations[key]
		}
		if !ok && key == gwapi.AnnotationHugePages1G {
			val, ok = strconv.Itoa(GatewayHugePages1G), true
		}
		if !ok {
			continue
		}

		if setAnnotation(node, key, val) {
			nodeChanged = true
		}
		if gw != nil && setAnnotation(gw, key, val) {
			gwChanged = true
		}
	}

	return nodeChanged, gwChanged
}

// setAnnotation sets the annotation unless already set, returns whether it's changed
func setAnnotation(obj kclient.Object, key, val string) bool {
	anns := obj.GetAnnotations()
	if _, ok := anns[key]; ok {
		return false
	}

	if anns == nil {
		anns = map[string]string{}
	}
	anns[key] = val
	obj.SetAnnotations(anns)

	return true
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
