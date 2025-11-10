// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	fmeta "go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type HydrateMode string

const (
	HydrateModeNever        HydrateMode = "never"
	HydrateModeIfNotPresent HydrateMode = "if-not-present"
	HydrateModeOverride     HydrateMode = "override"
	// TODO "auto" to only allocate missing values
)

var HydrateModes = []HydrateMode{
	HydrateModeNever,
	HydrateModeIfNotPresent,
	HydrateModeOverride,
}

type HydrationStatus string

const (
	HydrationStatusNone    HydrationStatus = "none"
	HydrationStatusPartial HydrationStatus = "partial"
	HydrationStatusFull    HydrationStatus = "full"
)

func (c *Config) loadHydrateValidate(ctx context.Context, mode HydrateMode) error {
	l, err := c.loadWiring(ctx)
	if err != nil {
		return fmt.Errorf("loading wiring: %w", err)
	}

	kube := l.GetClient()

	if err := c.ensureHydrated(ctx, kube, mode); err != nil {
		return fmt.Errorf("ensuring hydrated: %w", err)
	}

	fabricCfg, err := fabric.GetFabricConfig(c.Fab)
	if err != nil {
		return fmt.Errorf("getting fabric config: %w", err)
	}
	if fabricCfg, err = fabricCfg.Init(); err != nil {
		return fmt.Errorf("initializing fabric config: %w", err)
	}

	if err := apiutil.ValidateFabricGateway(ctx, l, fabricCfg); err != nil {
		return fmt.Errorf("validating wiring after hydrate: %w", err)
	}

	c.Client = kube

	return nil
}

func (c *Config) loadWiring(ctx context.Context) (*apiutil.Loader, error) {
	includeDir := filepath.Join(c.WorkDir, IncludeDir)
	stat, err := os.Stat(includeDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("include dir %q: %w", includeDir, ErrNotExist)
		}

		return nil, fmt.Errorf("checking include dir %q: %w", includeDir, err)
	}
	if !stat.IsDir() {
		return nil, fmt.Errorf("include dir %q: %w", includeDir, ErrNotDir)
	}

	l := apiutil.NewLoader()

	fabCfg, err := os.ReadFile(filepath.Join(c.WorkDir, FabConfigFile))
	if err != nil {
		return nil, fmt.Errorf("reading fab config: %w", err)
	}
	if err := l.LoadAdd(ctx, apiutil.FabricatorGVKs, fabCfg); err != nil {
		return nil, fmt.Errorf("loading fab config: %w", err)
	}

	files, err := os.ReadDir(includeDir)
	if err != nil {
		return nil, fmt.Errorf("reading include dir %q: %w", includeDir, err)
	}

	for _, file := range files {
		relName := filepath.Join(IncludeDir, file.Name())
		if file.IsDir() {
			slog.Warn("Skipping directory", "name", relName)

			continue
		}
		if !strings.HasSuffix(file.Name(), YAMLExt) {
			slog.Warn("Skipping non-YAML file", "name", file.Name())

			continue
		}

		data, err := os.ReadFile(filepath.Join(includeDir, file.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading wiring %q: %w", relName, err)
		}

		if err := l.LoadAdd(ctx, apiutil.FabricGatewayGVKs, data); err != nil {
			return nil, fmt.Errorf("loading include %q: %w", relName, err)
		}
	}

	return l, nil
}

func (c *Config) ensureHydrated(ctx context.Context, kube kclient.Client, mode HydrateMode) error {
	h, err := c.getHydration(ctx, kube)
	if err != nil {
		return fmt.Errorf("checking if hydrated: %w", err)
	}

	if mode == HydrateModeNever {
		if h != HydrationStatusFull {
			return fmt.Errorf("wiring is not fully hydrated while hydration is disabled, cleanup and/or change hydration mode") //nolint:goerr113
		}

		return nil
	} else if mode == HydrateModeIfNotPresent || mode == HydrateModeOverride {
		if mode == HydrateModeIfNotPresent && h == HydrationStatusFull {
			return nil
		}

		if mode == HydrateModeIfNotPresent && h != HydrationStatusNone {
			return fmt.Errorf("wiring is already partially hydrated, cleanup or change hydration mode") //nolint:goerr113
		}

		if err := c.hydrate(ctx, kube); err != nil {
			return fmt.Errorf("hydrating: %w", err)
		}

		slog.Info("Wiring hydrated successfully", "mode", mode)

		uh, err := c.getHydration(ctx, kube)
		if err != nil {
			return fmt.Errorf("checking status after hydration: %w", err)
		}

		if uh != HydrationStatusFull {
			return fmt.Errorf("wiring is not fully hydrated after hydration") //nolint:goerr113
		}

		return nil
	}

	return fmt.Errorf("unknown hydration mode %q or invalid hydration status", mode) //nolint:goerr113
}

func (c *Config) getHydration(ctx context.Context, kube kclient.Reader) (HydrationStatus, error) {
	status := HydrationStatusPartial

	total := 0
	missing := 0

	mgmtSubnet, err := c.Fab.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing management subnet: %w", err)
	}

	controlVIP, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing control VIP: %w", err)
	}

	mgmtIPs := map[netip.Addr]bool{
		controlVIP.Addr(): true,
	}

	mgmtDHCPStart, err := c.Fab.Spec.Config.Fabric.ManagementDHCPStart.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing management DHCP start: %w", err)
	}
	if !mgmtSubnet.Contains(mgmtDHCPStart) {
		return status, fmt.Errorf("management DHCP start %s is not in the management subnet %s", mgmtDHCPStart, mgmtSubnet) //nolint:goerr113
	}

	mgmtDHCPEnd, err := c.Fab.Spec.Config.Fabric.ManagementDHCPEnd.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing management DHCP end: %w", err)
	}
	if !mgmtSubnet.Contains(mgmtDHCPEnd) {
		return status, fmt.Errorf("management DHCP end %s is not in the management subnet %s", mgmtDHCPStart, mgmtSubnet) //nolint:goerr113
	}

	if mgmtDHCPStart.Compare(mgmtDHCPEnd) >= 0 {
		return status, fmt.Errorf("management DHCP start %s should be less than the management DHCP end %s", mgmtDHCPStart, mgmtDHCPEnd) //nolint:goerr113
	}

	dummySubnet, err := c.Fab.Spec.Config.Control.DummySubnet.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing dummy subnet: %w", err)
	}
	if dummySubnet.Bits() > 24 {
		return status, fmt.Errorf("dummy subnet %s should be at least a /24", dummySubnet) //nolint:goerr113
	}

	dummyIPs := map[netip.Addr]bool{}

	processNode := func(name string, mgmt fabapi.ControlNodeManagement, dummy fabapi.ControlNodeDummy) error {
		total++
		if mgmt.IP != "" {
			controlIP, err := mgmt.IP.Parse()
			if err != nil {
				return fmt.Errorf("parsing control node %s management IP %s: %w", name, mgmt.IP, err)
			}

			if !mgmtSubnet.Contains(controlIP.Addr()) {
				return fmt.Errorf("control node %s management IP %s is not in the management subnet %s", name, controlIP, mgmtSubnet) //nolint:goerr113
			}

			if controlIP.Addr().Compare(mgmtDHCPStart) >= 0 {
				return fmt.Errorf("control node %s management IP %s should be less than the management DHCP start %s", name, controlIP, mgmtDHCPStart) //nolint:goerr113
			}

			if _, exist := mgmtIPs[controlIP.Addr()]; exist {
				return fmt.Errorf("control node %s management IP %s is already in use", name, controlIP) //nolint:goerr113
			}

			mgmtIPs[controlIP.Addr()] = true

			dummyIP, err := dummy.IP.Parse()
			if err != nil {
				return fmt.Errorf("parsing control node %s dummy IP %s: %w", name, dummy.IP, err)
			}
			if dummyIP.Bits() != 31 {
				return fmt.Errorf("control node %s dummy IP %s must be a /31", name, dummyIP) //nolint:goerr113
			}

			if !dummySubnet.Contains(dummyIP.Addr()) {
				return fmt.Errorf("control node %s dummy IP %s is not in the dummy subnet %s", name, dummyIP, dummySubnet) //nolint:goerr113
			}

			if _, exist := dummyIPs[dummyIP.Addr()]; exist {
				return fmt.Errorf("control node %s dummy IP %s is already in use", name, dummyIP) //nolint:goerr113
			}

			dummyIPs[dummyIP.Addr()] = true
		} else {
			missing++
		}

		return nil
	}

	for _, control := range c.Controls {
		if err := processNode(control.Name, control.Spec.Management, control.Spec.Dummy); err != nil {
			return status, err
		}
	}

	for _, node := range c.Nodes {
		if err := processNode(node.Name, node.Spec.Management, node.Spec.Dummy); err != nil {
			return status, err
		}
	}

	vtepSubnet, err := c.Fab.Spec.Config.Fabric.VTEPSubnet.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing VTEP subnet: %w", err)
	}

	vtepIPs := map[netip.Addr]bool{}

	protocolSubnet, err := c.Fab.Spec.Config.Fabric.ProtocolSubnet.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing protocol subnet: %w", err)
	}

	protocolIPs := map[netip.Addr]bool{}

	asnSpine := c.Fab.Spec.Config.Fabric.SpineASN
	asnLeafStart := c.Fab.Spec.Config.Fabric.LeafASNStart
	asnLeafEnd := c.Fab.Spec.Config.Fabric.LeafASNEnd

	leafASNs := map[uint32]bool{}

	mclagPeer := map[string]*wiringapi.Switch{}

	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return status, fmt.Errorf("listing switches: %w", err)
	}

	for _, sw := range switches.Items {
		if sw.Spec.Role == "" {
			return status, fmt.Errorf("switch %s role is not set", sw.Name) //nolint:goerr113
		}
		if !slices.Contains(wiringapi.SwitchRoles, sw.Spec.Role) {
			return status, fmt.Errorf("switch %s role %q is invalid", sw.Name, sw.Spec.Role) //nolint:goerr113
		}

		total++
		if sw.Spec.IP != "" {
			swIP, err := netip.ParsePrefix(sw.Spec.IP)
			if err != nil {
				return status, fmt.Errorf("parsing switch %s IP %s: %w", sw.Name, sw.Spec.IP, err)
			}

			if !mgmtSubnet.Contains(swIP.Addr()) {
				return status, fmt.Errorf("switch %s IP %s is not in the management subnet %s", sw.Name, swIP, mgmtSubnet) //nolint:goerr113
			}

			if swIP.Addr().Compare(mgmtDHCPStart) >= 0 {
				return status, fmt.Errorf("switch %s IP %s should be less than the management DHCP start %s", sw.Name, swIP, mgmtDHCPStart) //nolint:goerr113
			}

			if _, exist := mgmtIPs[swIP.Addr()]; exist {
				return status, fmt.Errorf("switch %s (management) IP %s is already in use", sw.Name, swIP) //nolint:goerr113
			}
			mgmtIPs[swIP.Addr()] = true
		} else {
			missing++
		}

		total++
		if sw.Spec.ProtocolIP != "" {
			swProtoIP, err := netip.ParsePrefix(sw.Spec.ProtocolIP)
			if err != nil {
				return status, fmt.Errorf("parsing switch %s protocol IP %s: %w", sw.Name, sw.Spec.ProtocolIP, err)
			}
			if swProtoIP.Bits() != 32 {
				return status, fmt.Errorf("switch %s protocol IP %s must be a /32", sw.Name, swProtoIP) //nolint:goerr113
			}

			if !protocolSubnet.Contains(swProtoIP.Addr()) {
				return status, fmt.Errorf("switch %s protocol IP %s is not in the protocol subnet %s", sw.Name, swProtoIP, protocolSubnet) //nolint:goerr113
			}

			if _, exist := protocolIPs[swProtoIP.Addr()]; exist {
				return status, fmt.Errorf("switch %s protocol IP %s is already in use", sw.Name, swProtoIP) //nolint:goerr113
			}
			protocolIPs[swProtoIP.Addr()] = true
		} else {
			missing++
		}

		total++
		if sw.Spec.ASN > 0 {
			if sw.Spec.Role.IsLeaf() {
				if sw.Spec.ASN < asnLeafStart || sw.Spec.ASN > asnLeafEnd {
					return status, fmt.Errorf("leaf %s ASN %d is not in the leaf ASN range %d-%d", sw.Name, sw.Spec.ASN, asnLeafStart, asnLeafEnd) //nolint:goerr113
				}

				if sw.Spec.Redundancy.Type == fmeta.RedundancyTypeMCLAG {
					if peer, exist := mclagPeer[sw.Spec.Redundancy.Group]; exist {
						if peer.Spec.ASN != sw.Spec.ASN {
							return status, fmt.Errorf("mclag peers should have same ASNs: %s and %s", sw.Name, peer.Name) //nolint:goerr113
						}
					} else {
						mclagPeer[sw.Spec.Redundancy.Group] = &sw

						if _, exist := leafASNs[sw.Spec.ASN]; exist {
							return status, fmt.Errorf("leaf %s ASN %d is already in use", sw.Name, sw.Spec.ASN) //nolint:goerr113
						}
					}
				} else if _, exist := leafASNs[sw.Spec.ASN]; exist {
					return status, fmt.Errorf("leaf %s ASN %d is already in use", sw.Name, sw.Spec.ASN) //nolint:goerr113
				}
				leafASNs[sw.Spec.ASN] = true
			}

			if sw.Spec.Role.IsSpine() && sw.Spec.ASN != asnSpine {
				return status, fmt.Errorf("spine %s ASN %d is not %d", sw.Name, sw.Spec.ASN, asnSpine) //nolint:goerr113
			}
		} else {
			missing++
		}

		if sw.Spec.VTEPIP != "" && sw.Spec.Role.IsSpine() {
			return status, fmt.Errorf("spine %s should not have VTEP IP", sw.Name) //nolint:goerr113
		}

		if sw.Spec.Role.IsLeaf() {
			total++
			if sw.Spec.VTEPIP != "" {
				swVTEPIP, err := netip.ParsePrefix(sw.Spec.VTEPIP)
				if err != nil {
					return status, fmt.Errorf("parsing switch %s VTEP IP %s: %w", sw.Name, sw.Spec.VTEPIP, err)
				}
				if swVTEPIP.Bits() != 32 {
					return status, fmt.Errorf("switch %s VTEP IP %s must be a /32", sw.Name, swVTEPIP) //nolint:goerr113
				}

				if !vtepSubnet.Contains(swVTEPIP.Addr()) {
					return status, fmt.Errorf("switch %s VTEP IP %s is not in the VTEP subnet %s", sw.Name, swVTEPIP, vtepSubnet) //nolint:goerr113
				}

				if sw.Spec.Redundancy.Type == fmeta.RedundancyTypeMCLAG {
					if peer, exist := mclagPeer[sw.Spec.Redundancy.Group]; exist {
						if peer.Spec.VTEPIP != sw.Spec.VTEPIP {
							return status, fmt.Errorf("mclag peers should have same VTEP IPs: %s and %s", sw.Name, peer.Name) //nolint:goerr113
						}
					} else {
						mclagPeer[sw.Spec.Redundancy.Group] = &sw

						if _, exist := vtepIPs[swVTEPIP.Addr()]; exist {
							return status, fmt.Errorf("switch %s VTEP IP %s is already in use", sw.Name, swVTEPIP) //nolint:goerr113
						}
					}
				} else if _, exist := vtepIPs[swVTEPIP.Addr()]; exist {
					return status, fmt.Errorf("switch %s VTEP IP %s is already in use", sw.Name, swVTEPIP) //nolint:goerr113
				}
				vtepIPs[swVTEPIP.Addr()] = true
			} else {
				missing++
			}
		}
	}

	fabricSubnet, err := c.Fab.Spec.Config.Fabric.FabricSubnet.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing fabric subnet: %w", err)
	}

	fabricIPs := map[netip.Addr]bool{}

	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns); err != nil {
		return status, fmt.Errorf("listing connections: %w", err)
	}

	for _, conn := range conns.Items {
		if conn.Spec.Fabric != nil { //nolint:gocritic
			cf := conn.Spec.Fabric

			for idx, link := range cf.Links {
				total += 2
				if link.Spine.IP == "" {
					missing++
				}
				if link.Leaf.IP == "" {
					missing++
				}
				if link.Spine.IP == "" || link.Leaf.IP == "" {
					continue
				}

				spinePrefix, err := netip.ParsePrefix(link.Spine.IP)
				if err != nil {
					return status, fmt.Errorf("parsing fabric connection %s link %d spine IP %s: %w", conn.Name, idx, link.Spine.IP, err)
				}
				if spinePrefix.Bits() != 31 {
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s is not a /31", conn.Name, idx, spinePrefix) //nolint:goerr113
				}

				spineIP := spinePrefix.Addr()
				if !fabricSubnet.Contains(spineIP) {
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s is not in the fabric subnet %s", conn.Name, idx, spineIP, fabricSubnet) //nolint:goerr113
				}
				if _, exist := fabricIPs[spineIP]; exist {
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s is already in use", conn.Name, idx, spineIP) //nolint:goerr113
				}
				fabricIPs[spineIP] = true

				leafPrefix, err := netip.ParsePrefix(link.Leaf.IP)
				if err != nil {
					return status, fmt.Errorf("parsing fabric connection %s link %d leaf IP %s: %w", conn.Name, idx, link.Leaf.IP, err)
				}
				if leafPrefix.Bits() != 31 {
					return status, fmt.Errorf("fabric connection %s link %d leaf IP %s is not a /31", conn.Name, idx, leafPrefix) //nolint:goerr113
				}

				leafIP := leafPrefix.Addr()
				if !fabricSubnet.Contains(leafIP) {
					return status, fmt.Errorf("fabric connection %s link %d leaf IP %s is not in the fabric subnet %s", conn.Name, idx, leafIP, fabricSubnet) //nolint:goerr113
				}
				if _, exist := fabricIPs[leafIP]; exist {
					return status, fmt.Errorf("fabric connection %s link %d leaf IP %s is already in use", conn.Name, idx, leafIP) //nolint:goerr113
				}
				fabricIPs[leafIP] = true

				if spinePrefix.Masked() != leafPrefix.Masked() {
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s and leaf IP %s are not in the same subnet", conn.Name, idx, spineIP, leafIP) //nolint:goerr113
				}
			}
		} else if conn.Spec.Mesh != nil {
			cm := conn.Spec.Mesh

			for idx, link := range cm.Links {
				total += 2
				if link.Leaf1.IP == "" {
					missing++
				}
				if link.Leaf2.IP == "" {
					missing++
				}
				if link.Leaf1.IP == "" || link.Leaf2.IP == "" {
					continue
				}

				leaf1Prefix, err := netip.ParsePrefix(link.Leaf1.IP)
				if err != nil {
					return status, fmt.Errorf("parsing mesh connection %s link %d leaf1 IP %s: %w", conn.Name, idx, link.Leaf1.IP, err)
				}
				if leaf1Prefix.Bits() != 31 {
					return status, fmt.Errorf("mesh connection %s link %d leaf1 IP %s is not a /31", conn.Name, idx, leaf1Prefix) //nolint:goerr113
				}

				leaf1IP := leaf1Prefix.Addr()
				if !fabricSubnet.Contains(leaf1IP) {
					return status, fmt.Errorf("mesh connection %s link %d leaf1 IP %s is not in the fabric subnet %s", conn.Name, idx, leaf1IP, fabricSubnet) //nolint:goerr113
				}
				if _, exist := fabricIPs[leaf1IP]; exist {
					return status, fmt.Errorf("mesh connection %s link %d leaf1 IP %s is already in use", conn.Name, idx, leaf1IP) //nolint:goerr113
				}
				fabricIPs[leaf1IP] = true

				leaf2Prefix, err := netip.ParsePrefix(link.Leaf2.IP)
				if err != nil {
					return status, fmt.Errorf("parsing mesh connection %s link %d leaf2 IP %s: %w", conn.Name, idx, link.Leaf2.IP, err)
				}
				if leaf2Prefix.Bits() != 31 {
					return status, fmt.Errorf("mesh connection %s link %d leaf2 IP %s is not a /31", conn.Name, idx, leaf2Prefix) //nolint:goerr113
				}

				leaf2IP := leaf2Prefix.Addr()
				if !fabricSubnet.Contains(leaf2IP) {
					return status, fmt.Errorf("mesh connection %s link %d leaf2 IP %s is not in the fabric subnet %s", conn.Name, idx, leaf2IP, fabricSubnet) //nolint:goerr113
				}
				if _, exist := fabricIPs[leaf2IP]; exist {
					return status, fmt.Errorf("mesh connection %s link %d leaf2 IP %s is already in use", conn.Name, idx, leaf2IP) //nolint:goerr113
				}
				fabricIPs[leaf2IP] = true

				if leaf1Prefix.Masked() != leaf2Prefix.Masked() {
					return status, fmt.Errorf("mesh connection %s link %d spine IP %s and leaf IP %s are not in the same subnet", conn.Name, idx, leaf1IP, leaf2IP) //nolint:goerr113
				}
			}
		} else if conn.Spec.Gateway != nil {
			cg := conn.Spec.Gateway

			for idx, link := range cg.Links {
				total += 2
				if link.Switch.IP == "" {
					missing++
				}
				if link.Gateway.IP == "" {
					missing++
				}
				if link.Switch.IP == "" || link.Gateway.IP == "" {
					continue
				}

				switchPrefix, err := netip.ParsePrefix(link.Switch.IP)
				if err != nil {
					return status, fmt.Errorf("parsing gateway connection %s link %d switch IP %s: %w", conn.Name, idx, link.Switch.IP, err)
				}
				if switchPrefix.Bits() != 31 {
					return status, fmt.Errorf("gateway connection %s link %d switch IP %s is not a /31", conn.Name, idx, switchPrefix) //nolint:goerr113
				}

				switchIP := switchPrefix.Addr()
				if !fabricSubnet.Contains(switchIP) {
					return status, fmt.Errorf("gateway connection %s link %d switch IP %s is not in the fabric subnet %s", conn.Name, idx, switchIP, fabricSubnet) //nolint:goerr113
				}
				if _, exist := fabricIPs[switchIP]; exist {
					return status, fmt.Errorf("gateway connection %s link %d switch IP %s is already in use", conn.Name, idx, switchIP) //nolint:goerr113
				}
				fabricIPs[switchIP] = true

				gwPrefix, err := netip.ParsePrefix(link.Gateway.IP)
				if err != nil {
					return status, fmt.Errorf("parsing gateway connection %s link %d gateway IP %s: %w", conn.Name, idx, link.Gateway.IP, err)
				}
				if gwPrefix.Bits() != 31 {
					return status, fmt.Errorf("gateway connection %s link %d gateway IP %s is not a /31", conn.Name, idx, gwPrefix) //nolint:goerr113
				}

				gwIP := gwPrefix.Addr()
				if !fabricSubnet.Contains(gwIP) {
					return status, fmt.Errorf("gateway connection %s link %d gateway IP %s is not in the fabric subnet %s", conn.Name, idx, gwIP, fabricSubnet) //nolint:goerr113
				}
				if _, exist := fabricIPs[gwIP]; exist {
					return status, fmt.Errorf("gateway connection %s link %d gateway IP %s is already in use", conn.Name, idx, gwIP) //nolint:goerr113
				}
				fabricIPs[gwIP] = true

				if switchPrefix.Masked() != gwPrefix.Masked() {
					return status, fmt.Errorf("gateway connection %s link %d spine IP %s and gateway IP %s are not in the same subnet", conn.Name, idx, switchIP, gwIP) //nolint:goerr113
				}
			}
		}
	}

	gateways := &gwapi.GatewayList{}
	if err := kube.List(ctx, gateways); err != nil {
		return status, fmt.Errorf("listing gateways: %w", err)
	}
	for _, gw := range gateways.Items {
		total++
		if gw.Spec.ASN != 0 {
			if gw.Spec.ASN != c.Fab.Spec.Config.Gateway.ASN {
				return status, fmt.Errorf("gateway %s ASN %d is not %d", gw.Name, gw.Spec.ASN, c.Fab.Spec.Config.Gateway.ASN) //nolint:goerr113
			}
		} else {
			missing++
		}

		// TODO should it be a separate subnet from the fabric?
		total++
		if gw.Spec.ProtocolIP != "" {
			gwProtoIP, err := netip.ParsePrefix(gw.Spec.ProtocolIP)
			if err != nil {
				return status, fmt.Errorf("parsing gateway %s protocol IP %s: %w", gw.Name, gw.Spec.ProtocolIP, err)
			}
			if gwProtoIP.Bits() != 32 {
				return status, fmt.Errorf("gateway %s protocol IP %s must be a /32", gw.Name, gwProtoIP) //nolint:goerr113
			}

			if !protocolSubnet.Contains(gwProtoIP.Addr()) {
				return status, fmt.Errorf("gateway %s protocol IP %s is not in the protocol subnet %s", gw.Name, gwProtoIP, protocolSubnet) //nolint:goerr113
			}

			if _, exist := protocolIPs[gwProtoIP.Addr()]; exist {
				return status, fmt.Errorf("gateway %s protocol IP %s is already in use", gw.Name, gwProtoIP) //nolint:goerr113
			}
			protocolIPs[gwProtoIP.Addr()] = true
		} else {
			missing++
		}

		// TODO should it be a separate subnet from the fabric?
		total++
		if gw.Spec.VTEPIP != "" {
			gwVTEPIP, err := netip.ParsePrefix(gw.Spec.VTEPIP)
			if err != nil {
				return status, fmt.Errorf("parsing gateway %s vtep IP %s: %w", gw.Name, gw.Spec.VTEPIP, err)
			}
			if gwVTEPIP.Bits() != 32 {
				return status, fmt.Errorf("gateway %s vtep IP %s must be a /32", gw.Name, gwVTEPIP) //nolint:goerr113
			}

			if !vtepSubnet.Contains(gwVTEPIP.Addr()) {
				return status, fmt.Errorf("gateway %s vtep IP %s is not in the vtep subnet %s", gw.Name, gwVTEPIP, vtepSubnet) //nolint:goerr113
			}

			if _, exist := vtepIPs[gwVTEPIP.Addr()]; exist {
				return status, fmt.Errorf("gateway %s vtep IP %s is already in use", gw.Name, gwVTEPIP) //nolint:goerr113
			}
			vtepIPs[gwVTEPIP.Addr()] = true
		} else {
			missing++
		}

		// TODO do some basic validation of the interfaces and neighbors
	}

	switch {
	case missing == total:
		return HydrationStatusNone, nil
	case total > 0 && missing == 0:
		return HydrationStatusFull, nil
	case total > 0 && missing > 0:
		return HydrationStatusPartial, nil
	}

	return status, fmt.Errorf("invalid hydration status: total=%d, missing=%d", total, missing) //nolint:goerr113
}

func (c *Config) hydrate(ctx context.Context, kube kclient.Client) error {
	mgmtSubnet, err := c.Fab.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing management subnet: %w", err)
	}

	controlVIP, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}

	if controlVIP.Addr() != mgmtSubnet.Masked().Addr().Next() {
		return fmt.Errorf("control VIP %s is not the first IP of the management subnet %s", controlVIP, mgmtSubnet) //nolint:goerr113
	}

	nextMgmtIP := controlVIP.Addr()
	for i := 0; i < 4; i++ { // reserve few IPs for future use
		nextMgmtIP = nextMgmtIP.Next()
	}

	dummySubnet, err := c.Fab.Spec.Config.Control.DummySubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing dummy subnet: %w", err)
	}

	nextDummyIP := dummySubnet.Masked().Addr()

	slices.SortFunc(c.Controls, func(a, b fabapi.ControlNode) int {
		return cmp.Compare(a.Name, b.Name)
	})

	for idx := range c.Controls {
		control := &c.Controls[idx]

		control.Spec.Management.IP = meta.Prefix(netip.PrefixFrom(nextMgmtIP, mgmtSubnet.Bits()).String())
		nextMgmtIP = nextMgmtIP.Next()

		control.Spec.Dummy.IP = meta.Prefix(netip.PrefixFrom(nextDummyIP, 31).String())
		nextDummyIP = nextDummyIP.Next().Next()
	}

	for idx := range c.Nodes {
		node := &c.Nodes[idx]

		node.Spec.Management.IP = meta.Prefix(netip.PrefixFrom(nextMgmtIP, mgmtSubnet.Bits()).String())
		nextMgmtIP = nextMgmtIP.Next()

		node.Spec.Dummy.IP = meta.Prefix(netip.PrefixFrom(nextDummyIP, 31).String())
		nextDummyIP = nextDummyIP.Next().Next()
	}

	vtepSubnet, err := c.Fab.Spec.Config.Fabric.VTEPSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing VTEP subnet: %w", err)
	}
	nextVTEPIP := vtepSubnet.Masked().Addr()

	protocolSubnet, err := c.Fab.Spec.Config.Fabric.ProtocolSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing protocol subnet: %w", err)
	}
	nextProtoIP := protocolSubnet.Masked().Addr()

	spineASN := c.Fab.Spec.Config.Fabric.SpineASN
	nextLeafASN := c.Fab.Spec.Config.Fabric.LeafASNStart

	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}

	slices.SortFunc(switches.Items, func(a, b wiringapi.Switch) int {
		if a.Spec.Role == b.Spec.Role {
			return cmp.Compare(a.Name, b.Name)
		}

		if a.Spec.Role == wiringapi.SwitchRoleSpine {
			return -1
		}

		if b.Spec.Role == wiringapi.SwitchRoleSpine {
			return 1
		}

		return cmp.Compare(a.Spec.Role, b.Spec.Role)
	})

	mclagPeer := map[string]*wiringapi.Switch{}

	for idx := range switches.Items {
		sw := &switches.Items[idx]
		if sw.Spec.Role == "" {
			return fmt.Errorf("switch %s role is not set", sw.Name) //nolint:goerr113
		}
		if !slices.Contains(wiringapi.SwitchRoles, sw.Spec.Role) {
			return fmt.Errorf("switch %s role %q is invalid", sw.Name, sw.Spec.Role) //nolint:goerr113
		}

		sw.Spec.IP = netip.PrefixFrom(nextMgmtIP, mgmtSubnet.Bits()).String()
		nextMgmtIP = nextMgmtIP.Next()

		sw.Spec.ProtocolIP = netip.PrefixFrom(nextProtoIP, 32).String()
		nextProtoIP = nextProtoIP.Next()

		if sw.Spec.Role.IsSpine() {
			sw.Spec.ASN = spineASN
		}

		if sw.Spec.Redundancy.Type == fmeta.RedundancyTypeMCLAG {
			if peer, exist := mclagPeer[sw.Spec.Redundancy.Group]; exist {
				sw.Spec.ASN = peer.Spec.ASN
				sw.Spec.VTEPIP = peer.Spec.VTEPIP

				continue
			}

			mclagPeer[sw.Spec.Redundancy.Group] = sw
		}

		if sw.Spec.Role.IsLeaf() {
			sw.Spec.ASN = nextLeafASN
			nextLeafASN++

			sw.Spec.VTEPIP = ""
			sw.Spec.VTEPIP = netip.PrefixFrom(nextVTEPIP, 32).String()
			nextVTEPIP = nextVTEPIP.Next()
		}
	}

	for _, sw := range switches.Items {
		if err := kube.Update(ctx, &sw); err != nil {
			return fmt.Errorf("updating switch %s: %w", sw.Name, err)
		}
	}

	fabricSubnet, err := c.Fab.Spec.Config.Fabric.FabricSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing fabric subnet: %w", err)
	}
	nextFabricIP := fabricSubnet.Masked().Addr()

	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns); err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}

	slices.SortFunc(conns.Items, func(a, b wiringapi.Connection) int {
		return cmp.Compare(a.Name, b.Name)
	})

	for _, conn := range conns.Items {
		if conn.Spec.Fabric != nil { //nolint:gocritic
			cf := conn.Spec.Fabric

			for idx := range cf.Links {
				link := &cf.Links[idx]
				link.Spine.IP = nextFabricIP.String() + "/31"
				nextFabricIP = nextFabricIP.Next()

				link.Leaf.IP = nextFabricIP.String() + "/31"
				nextFabricIP = nextFabricIP.Next()
			}
		} else if conn.Spec.Mesh != nil {
			cm := conn.Spec.Mesh

			for idx := range cm.Links {
				link := &cm.Links[idx]
				link.Leaf1.IP = nextFabricIP.String() + "/31"
				nextFabricIP = nextFabricIP.Next()

				link.Leaf2.IP = nextFabricIP.String() + "/31"
				nextFabricIP = nextFabricIP.Next()
			}
		} else if conn.Spec.Gateway != nil {
			cg := conn.Spec.Gateway

			for idx := range cg.Links {
				link := &cg.Links[idx]
				link.Switch.IP = nextFabricIP.String() + "/31"
				nextFabricIP = nextFabricIP.Next()

				link.Gateway.IP = nextFabricIP.String() + "/31"
				nextFabricIP = nextFabricIP.Next()

				gwName := link.Gateway.DeviceName()
				gw := &gwapi.Gateway{}
				if err := kube.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: gwName}, gw); err != nil {
					return fmt.Errorf("getting gateway %s: %w", gwName, err)
				}
				if gw.Spec.Interfaces == nil {
					gw.Spec.Interfaces = map[string]gwapi.GatewayInterface{}
				}

				iface, exist := gw.Spec.Interfaces[link.Gateway.LocalPortName()]
				if !exist {
					return fmt.Errorf("gateway %s does not have interface %s", gwName, link.Gateway.LocalPortName()) //nolint:goerr113
				}
				iface.IPs = []string{link.Gateway.IP}
				iface.MTU = fabric.MTU
				gw.Spec.Interfaces[link.Gateway.LocalPortName()] = iface

				switchIP, err := netip.ParsePrefix(link.Switch.IP)
				if err != nil {
					return fmt.Errorf("parsing gateway %s link %d switch IP %s: %w", gwName, idx, link.Switch.IP, err)
				}

				asn := uint32(0)
				for _, sw := range switches.Items {
					if sw.Name == link.Switch.DeviceName() {
						asn = sw.Spec.ASN

						break
					}
				}
				if asn == 0 {
					return fmt.Errorf("switch %s not found or ASN not set", link.Switch.DeviceName()) //nolint:err113
				}

				// TODO check that it's not already set?
				gw.Spec.Neighbors = append(gw.Spec.Neighbors, gwapi.GatewayBGPNeighbor{
					Source: link.Gateway.LocalPortName(),
					IP:     switchIP.Addr().String(),
					ASN:    asn,
				})

				if err := kube.Update(ctx, gw); err != nil {
					return fmt.Errorf("updating gateway %s: %w", gw.Name, err)
				}
			}
		} else {
			continue
		}

		if err := kube.Update(ctx, &conn); err != nil {
			return fmt.Errorf("updating connection %s: %w", conn.Name, err)
		}
	}

	gateways := &gwapi.GatewayList{}
	if err := kube.List(ctx, gateways); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}

	for _, gw := range gateways.Items {
		gw.Spec.ASN = c.Fab.Spec.Config.Gateway.ASN

		gw.Spec.ProtocolIP = netip.PrefixFrom(nextProtoIP, 32).String()
		nextProtoIP = nextProtoIP.Next()

		gw.Spec.VTEPIP = netip.PrefixFrom(nextVTEPIP, 32).String()
		nextVTEPIP = nextVTEPIP.Next()

		gw.Spec.VTEPMAC = "CA:FE:BA:BE:01:02" // TODO replace with properly generated or configured MAC address
		gw.Spec.VTEPMTU = fabric.ServerFacingMTU

		if err := kube.Update(ctx, &gw); err != nil {
			return fmt.Errorf("updating gateway %s: %w", gw.Name, err)
		}
	}

	return nil
}
