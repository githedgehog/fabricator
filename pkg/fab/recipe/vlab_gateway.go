// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const oldVLABGatewayIfacePrefix = "enp2s"

// migrateVLABGateways moves the kernel driver gateways of VLAB VMs from the enp2sN interfaces to the portN ones, as
// their NICs are on the root bus now. If any needs it, it waits for the fabric controller of the version to refresh
// the gateways with the driver first.
//
// TODO remove once upgrades are only supported from 26.05 onwards
func migrateVLABGateways(ctx context.Context, kube kclient.Client, fabricVersion string) error {
	if !isVLAB() {
		return nil
	}

	nodes := &fabapi.FabNodeList{}
	if err := kube.List(ctx, nodes); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}
	hw := map[string]bool{}
	for _, node := range nodes.Items {
		hw[node.Name] = node.Annotations[vlabcomp.NodeTypeAnnotation] == vlabcomp.NodeTypeHW
	}

	gws := &gwapi.GatewayList{}
	if err := kube.List(ctx, gws); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}

	needed := false
	for _, gw := range gws.Items {
		if hw[gw.Name] {
			continue
		}

		renames, err := migrateVLABGatewayIfaces(gw.DeepCopy())
		if err != nil {
			return fmt.Errorf("checking gateway %s: %w", gw.Name, err)
		}
		needed = needed || len(renames) > 0
	}
	if !needed {
		return nil
	}

	if err := waitFabricCtrlInitialized(ctx, kube, fabricVersion); err != nil {
		return err
	}

	if err := kube.List(ctx, gws); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}

	// gateway port -> new gateway port
	renames := map[string]string{}
	for _, gw := range gws.Items {
		if hw[gw.Name] || gw.Spec.Driver != gwapi.GatewayDriverKernel {
			continue
		}

		gwRenames, err := migrateVLABGatewayIfaces(&gw)
		if err != nil {
			return fmt.Errorf("migrating gateway %s: %w", gw.Name, err)
		}
		if len(gwRenames) == 0 {
			continue
		}

		slog.Info("Migrating VLAB gateway interfaces", "gateway", gw.Name, "renames", gwRenames)

		if err := kube.Update(ctx, &gw); err != nil {
			return fmt.Errorf("updating gateway %s: %w", gw.Name, err)
		}

		for old, port := range gwRenames {
			renames[gw.Name+"/"+old] = gw.Name + "/" + port
		}
	}

	if len(renames) == 0 {
		return nil
	}

	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns); err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}

	for _, conn := range conns.Items {
		if conn.Spec.Gateway == nil {
			continue
		}

		changed := false
		for idx, link := range conn.Spec.Gateway.Links {
			if port, ok := renames[link.Gateway.Port]; ok {
				conn.Spec.Gateway.Links[idx].Gateway.Port = port
				changed = true
			}
		}
		if !changed {
			continue
		}

		if err := kube.Update(ctx, &conn); err != nil {
			return fmt.Errorf("updating connection %s: %w", conn.Name, err)
		}
	}

	return nil
}

// migrateVLABGatewayIfaces renames the old style interfaces of the gateway and the neighbors' sources and returns the
// renames
func migrateVLABGatewayIfaces(gw *gwapi.Gateway) (map[string]string, error) {
	renames := map[string]string{}
	ifaces := map[string]gwapi.GatewayInterface{}

	for name, iface := range gw.Spec.Interfaces {
		raw, ok := strings.CutPrefix(name, oldVLABGatewayIfacePrefix)
		if !ok || iface.PCI != "" || iface.Kernel != "" {
			ifaces[name] = iface

			continue
		}

		nicID, err := strconv.ParseUint(raw, 10, 8)
		if err != nil || nicID == 0 {
			return nil, fmt.Errorf("unexpected interface %s", name) //nolint:err113
		}

		port := fmt.Sprintf("port%d", nicID)
		if _, exist := gw.Spec.Interfaces[port]; exist {
			return nil, fmt.Errorf("interface %s already exists", port) //nolint:err113
		}

		iface.PCI = vlabcomp.GatewayPCI(uint8(nicID))
		iface.Kernel = vlabcomp.GatewayKernel(uint8(nicID))
		ifaces[port] = iface
		renames[name] = port
	}

	gw.Spec.Interfaces = ifaces
	for idx, nei := range gw.Spec.Neighbors {
		if port, ok := renames[nei.Source]; ok {
			gw.Spec.Neighbors[idx].Source = port
		}
	}

	return renames, nil
}
