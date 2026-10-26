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
	"k8s.io/client-go/util/retry"
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

	return migrateVLABGatewayPorts(ctx, kube, func() error {
		return waitFabricCtrlInitialized(ctx, kube, fabricVersion)
	})
}

// migrateVLABGatewayPorts migrates the gateways and then the connections separately, so a retry finishes a partial
// migration
func migrateVLABGatewayPorts(ctx context.Context, kube kclient.Client, waitRefreshed func() error) error {
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
	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns); err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}

	needed := false
	vms := map[string]bool{}
	for _, gw := range gws.Items {
		if hw[gw.Name] {
			continue
		}
		vms[gw.Name] = true

		renames, err := migrateVLABGatewayIfaces(gw.DeepCopy())
		if err != nil {
			return fmt.Errorf("checking gateway %s: %w", gw.Name, err)
		}
		needed = needed || len(renames) > 0
	}
	for _, conn := range conns.Items {
		if conn.Spec.Gateway == nil {
			continue
		}
		for _, link := range conn.Spec.Gateway.Links {
			gwName, iface, _ := strings.Cut(link.Gateway.Port, "/")
			needed = needed || vms[gwName] && strings.HasPrefix(iface, oldVLABGatewayIfacePrefix)
		}
	}
	if !needed {
		return nil
	}

	if err := waitRefreshed(); err != nil {
		return err
	}

	// re-listing as the fabric controller refresh updates them
	if err := kube.List(ctx, gws); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}

	// the new fabric controller writes to the same objects, so all updates are retried on conflict with a fresh copy
	migrated := map[string]gwapi.Gateway{}
	for _, gw := range gws.Items {
		if hw[gw.Name] || gw.Spec.Driver != gwapi.GatewayDriverKernel {
			continue
		}

		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := kube.Get(ctx, kclient.ObjectKeyFromObject(&gw), &gw); err != nil {
				return fmt.Errorf("getting: %w", err)
			}

			renames, err := migrateVLABGatewayIfaces(&gw)
			if err != nil || len(renames) == 0 {
				return err
			}

			slog.Info("Migrating VLAB gateway interfaces", "gateway", gw.Name, "renames", renames)

			if err := kube.Update(ctx, &gw); err != nil {
				return fmt.Errorf("updating: %w", err)
			}

			return nil
		}); err != nil {
			return fmt.Errorf("migrating gateway %s: %w", gw.Name, err)
		}
		migrated[gw.Name] = gw
	}

	if err := kube.List(ctx, conns); err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}

	for _, conn := range conns.Items {
		if conn.Spec.Gateway == nil {
			continue
		}

		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := kube.Get(ctx, kclient.ObjectKeyFromObject(&conn), &conn); err != nil {
				return fmt.Errorf("getting: %w", err)
			}

			changed, err := migrateVLABGatewayLinks(&conn, migrated)
			if err != nil || !changed {
				return err
			}

			slog.Info("Migrating VLAB gateway connection", "connection", conn.Name)

			if err := kube.Update(ctx, &conn); err != nil {
				return fmt.Errorf("updating: %w", err)
			}

			return nil
		}); err != nil {
			return fmt.Errorf("migrating connection %s: %w", conn.Name, err)
		}
	}

	return nil
}

// migrateVLABGatewayLinks renames the old style ports of the migrated gateways in the connection
func migrateVLABGatewayLinks(conn *wiringapi.Connection, migrated map[string]gwapi.Gateway) (bool, error) {
	changed := false
	for idx, link := range conn.Spec.Gateway.Links {
		gwName, iface, _ := strings.Cut(link.Gateway.Port, "/")
		gw, ok := migrated[gwName]
		if !ok {
			continue
		}

		port, _, err := newVLABGatewayPort(iface)
		if err != nil {
			return false, err
		}
		if _, exist := gw.Spec.Interfaces[port]; port == "" || !exist {
			continue
		}

		conn.Spec.Gateway.Links[idx].Gateway.Port = gwName + "/" + port
		changed = true
	}

	return changed, nil
}

// newVLABGatewayPort returns portN and N for an old style enp2sN interface or nothing for any other one
func newVLABGatewayPort(name string) (string, uint8, error) {
	raw, ok := strings.CutPrefix(name, oldVLABGatewayIfacePrefix)
	if !ok {
		return "", 0, nil
	}

	nicID, err := strconv.ParseUint(raw, 10, 8)
	if err != nil || nicID == 0 {
		return "", 0, fmt.Errorf("unexpected interface %s", name) //nolint:err113
	}

	return fmt.Sprintf("port%d", nicID), uint8(nicID), nil
}

// migrateVLABGatewayIfaces renames the old style interfaces of the gateway and the neighbors' sources and returns the
// renames
func migrateVLABGatewayIfaces(gw *gwapi.Gateway) (map[string]string, error) {
	renames := map[string]string{}
	ifaces := map[string]gwapi.GatewayInterface{}

	for name, iface := range gw.Spec.Interfaces {
		if iface.PCI != "" || iface.Kernel != "" {
			ifaces[name] = iface

			continue
		}

		port, nicID, err := newVLABGatewayPort(name)
		if err != nil {
			return nil, err
		}
		if port == "" {
			ifaces[name] = iface

			continue
		}

		if _, exist := gw.Spec.Interfaces[port]; exist {
			return nil, fmt.Errorf("interface %s already exists", port) //nolint:err113
		}

		iface.PCI = vlabcomp.GatewayPCI(nicID)
		iface.Kernel = vlabcomp.GatewayKernel(nicID)
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
