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
	"reflect"
	"slices"
	"strings"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/api/valid"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type HydrateMode string

const (
	HydrateModeNever        HydrateMode = "never"
	HydrateModeIfNotPresent HydrateMode = "if-not-present"
	HydrateModeOverride     HydrateMode = "override"
	// HydrateModeAuto only allocates the values that are not set yet, continuing each pool after the ones in use
	HydrateModeAuto HydrateMode = "auto"
)

var HydrateModes = []HydrateMode{
	HydrateModeNever,
	HydrateModeIfNotPresent,
	HydrateModeOverride,
	HydrateModeAuto,
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
	if fabricCfg, err = fabricCfg.Init(fmeta.ExtraValidators{
		Peering: valid.Peering,
	}); err != nil {
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

		if err := l.LoadAddWiring(ctx, data); err != nil {
			return nil, fmt.Errorf("loading include %q: %w", relName, err)
		}
	}

	if err := injectUpgradeDefaultFabric(ctx, l, c.Fab, os.Getenv(UpgradeFromEnv)); err != nil {
		return nil, err
	}

	return l, nil
}

func (c *Config) ensureHydrated(ctx context.Context, kube kclient.Client, mode HydrateMode) error {
	h, err := c.getHydration(ctx, kube)
	if err != nil {
		return fmt.Errorf("checking if hydrated: %w", err)
	}

	switch mode {
	case HydrateModeNever:
		if h != HydrationStatusFull {
			return fmt.Errorf("wiring is not fully hydrated while hydration is disabled, cleanup and/or change hydration mode") //nolint:err113
		}

		return nil
	case HydrateModeAuto, HydrateModeIfNotPresent, HydrateModeOverride:
		if mode == HydrateModeIfNotPresent && h == HydrationStatusFull {
			return nil
		}

		if mode == HydrateModeIfNotPresent && h != HydrationStatusNone {
			return fmt.Errorf("wiring is already partially hydrated, cleanup or change hydration mode") //nolint:err113
		}

		if mode == HydrateModeAuto {
			if err := c.hydrateMissing(ctx, kube); err != nil {
				return fmt.Errorf("hydrating missing values: %w", err)
			}
		} else if err := c.hydrate(ctx, kube); err != nil {
			return fmt.Errorf("hydrating: %w", err)
		}

		slog.Info("Wiring hydrated successfully", "mode", mode)

		uh, err := c.getHydration(ctx, kube)
		if err != nil {
			return fmt.Errorf("checking status after hydration: %w", err)
		}

		if uh != HydrationStatusFull {
			return fmt.Errorf("wiring is not fully hydrated after hydration") //nolint:err113
		}

		return nil
	}

	return fmt.Errorf("unknown hydration mode %q or invalid hydration status", mode) //nolint:err113
}

func fabricSpecs(ctx context.Context, kube kclient.Reader) (map[string]wiringapi.FabricSpec, error) {
	fabrics := &wiringapi.FabricList{}
	if err := kube.List(ctx, fabrics); err != nil {
		return nil, fmt.Errorf("listing fabrics: %w", err)
	}

	specs := map[string]wiringapi.FabricSpec{}
	for _, f := range fabrics.Items {
		specs[f.Name] = f.Spec
	}

	return specs, nil
}

func fabricDomain(fabrics map[string]wiringapi.FabricSpec, fabricName, domainName string) (wiringapi.FabricDomainSpec, error) {
	f, ok := fabrics[fabricName]
	if !ok {
		return wiringapi.FabricDomainSpec{}, fmt.Errorf("fabric %s not found", fabricName) //nolint:err113
	}
	domain, ok := f.Domains[domainName]
	if !ok {
		return wiringapi.FabricDomainSpec{}, fmt.Errorf("domain %s not found in fabric %s", domainName, fabricName) //nolint:err113
	}

	return domain, nil
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
		return status, fmt.Errorf("management DHCP start %s is not in the management subnet %s", mgmtDHCPStart, mgmtSubnet) //nolint:err113
	}

	mgmtDHCPEnd, err := c.Fab.Spec.Config.Fabric.ManagementDHCPEnd.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing management DHCP end: %w", err)
	}
	if !mgmtSubnet.Contains(mgmtDHCPEnd) {
		return status, fmt.Errorf("management DHCP end %s is not in the management subnet %s", mgmtDHCPStart, mgmtSubnet) //nolint:err113
	}

	if mgmtDHCPStart.Compare(mgmtDHCPEnd) >= 0 {
		return status, fmt.Errorf("management DHCP start %s should be less than the management DHCP end %s", mgmtDHCPStart, mgmtDHCPEnd) //nolint:err113
	}

	dummySubnet, err := c.Fab.Spec.Config.Control.DummySubnet.Parse()
	if err != nil {
		return status, fmt.Errorf("parsing dummy subnet: %w", err)
	}
	if dummySubnet.Bits() > 24 {
		return status, fmt.Errorf("dummy subnet %s should be at least a /24", dummySubnet) //nolint:err113
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
				return fmt.Errorf("control node %s management IP %s is not in the management subnet %s", name, controlIP, mgmtSubnet) //nolint:err113
			}

			if controlIP.Addr().Compare(mgmtDHCPStart) >= 0 {
				return fmt.Errorf("control node %s management IP %s should be less than the management DHCP start %s", name, controlIP, mgmtDHCPStart) //nolint:err113
			}

			if _, exist := mgmtIPs[controlIP.Addr()]; exist {
				return fmt.Errorf("control node %s management IP %s is already in use", name, controlIP) //nolint:err113
			}

			mgmtIPs[controlIP.Addr()] = true

			dummyIP, err := dummy.IP.Parse()
			if err != nil {
				return fmt.Errorf("parsing control node %s dummy IP %s: %w", name, dummy.IP, err)
			}
			if dummyIP.Bits() != 31 {
				return fmt.Errorf("control node %s dummy IP %s must be a /31", name, dummyIP) //nolint:err113
			}

			if !dummySubnet.Contains(dummyIP.Addr()) {
				return fmt.Errorf("control node %s dummy IP %s is not in the dummy subnet %s", name, dummyIP, dummySubnet) //nolint:err113
			}

			if _, exist := dummyIPs[dummyIP.Addr()]; exist {
				return fmt.Errorf("control node %s dummy IP %s is already in use", name, dummyIP) //nolint:err113
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

	fabrics, err := fabricSpecs(ctx, kube)
	if err != nil {
		return status, err
	}

	leafASNs := map[uint32]bool{}

	mclagPeer := map[string]*wiringapi.Switch{}

	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return status, fmt.Errorf("listing switches: %w", err)
	}

	for _, sw := range switches.Items {
		if sw.Spec.Role == "" {
			return status, fmt.Errorf("switch %s role is not set", sw.Name) //nolint:err113
		}
		if !slices.Contains(wiringapi.SwitchRoles, sw.Spec.Role) {
			return status, fmt.Errorf("switch %s role %q is invalid", sw.Name, sw.Spec.Role) //nolint:err113
		}

		total++
		if sw.Spec.IP != "" {
			swIP, err := netip.ParsePrefix(sw.Spec.IP)
			if err != nil {
				return status, fmt.Errorf("parsing switch %s IP %s: %w", sw.Name, sw.Spec.IP, err)
			}

			if !mgmtSubnet.Contains(swIP.Addr()) {
				return status, fmt.Errorf("switch %s IP %s is not in the management subnet %s", sw.Name, swIP, mgmtSubnet) //nolint:err113
			}

			if swIP.Addr().Compare(mgmtDHCPStart) >= 0 {
				return status, fmt.Errorf("switch %s IP %s should be less than the management DHCP start %s", sw.Name, swIP, mgmtDHCPStart) //nolint:err113
			}

			if _, exist := mgmtIPs[swIP.Addr()]; exist {
				return status, fmt.Errorf("switch %s (management) IP %s is already in use", sw.Name, swIP) //nolint:err113
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
				return status, fmt.Errorf("switch %s protocol IP %s must be a /32", sw.Name, swProtoIP) //nolint:err113
			}

			if !protocolSubnet.Contains(swProtoIP.Addr()) {
				return status, fmt.Errorf("switch %s protocol IP %s is not in the protocol subnet %s", sw.Name, swProtoIP, protocolSubnet) //nolint:err113
			}

			if _, exist := protocolIPs[swProtoIP.Addr()]; exist {
				return status, fmt.Errorf("switch %s protocol IP %s is already in use", sw.Name, swProtoIP) //nolint:err113
			}
			protocolIPs[swProtoIP.Addr()] = true
		} else {
			missing++
		}

		total++
		if sw.Spec.ASN > 0 {
			swFabric := sw.Spec.Topology.Fabric
			if sw.Spec.Role.IsLeaf() {
				f, ok := fabrics[swFabric]
				if !ok {
					return status, fmt.Errorf("switch %s fabric %s not found", sw.Name, swFabric) //nolint:err113
				}
				if sw.Spec.ASN < f.LeafASNStart || sw.Spec.ASN > f.LeafASNEnd {
					return status, fmt.Errorf("leaf %s ASN %d is not in the fabric %s leaf ASN range %d-%d", sw.Name, sw.Spec.ASN, swFabric, f.LeafASNStart, f.LeafASNEnd) //nolint:err113
				}

				if sw.Spec.Redundancy.Type == fmeta.RedundancyTypeMCLAG {
					if peer, exist := mclagPeer[sw.Spec.Redundancy.Group]; exist {
						if peer.Spec.ASN != sw.Spec.ASN {
							return status, fmt.Errorf("mclag peers should have same ASNs: %s and %s", sw.Name, peer.Name) //nolint:err113
						}
					} else {
						mclagPeer[sw.Spec.Redundancy.Group] = &sw

						if _, exist := leafASNs[sw.Spec.ASN]; exist {
							return status, fmt.Errorf("leaf %s ASN %d is already in use", sw.Name, sw.Spec.ASN) //nolint:err113
						}
					}
				} else if _, exist := leafASNs[sw.Spec.ASN]; exist {
					return status, fmt.Errorf("leaf %s ASN %d is already in use", sw.Name, sw.Spec.ASN) //nolint:err113
				}
				leafASNs[sw.Spec.ASN] = true
			}

			if sw.Spec.Role.IsSpine() {
				domain, err := fabricDomain(fabrics, swFabric, sw.Spec.Topology.Domains[0])
				if err != nil {
					return status, fmt.Errorf("spine %s: %w", sw.Name, err)
				}
				if sw.Spec.ASN != domain.SpineASN {
					return status, fmt.Errorf("spine %s ASN %d is not %d", sw.Name, sw.Spec.ASN, domain.SpineASN) //nolint:err113
				}
			}
		} else {
			missing++
		}

		if sw.Spec.VTEPIP != "" && sw.Spec.Role.IsSpine() {
			return status, fmt.Errorf("spine %s should not have VTEP IP", sw.Name) //nolint:err113
		}

		if sw.Spec.Role.IsLeaf() {
			total++
			if sw.Spec.VTEPIP != "" {
				swVTEPIP, err := netip.ParsePrefix(sw.Spec.VTEPIP)
				if err != nil {
					return status, fmt.Errorf("parsing switch %s VTEP IP %s: %w", sw.Name, sw.Spec.VTEPIP, err)
				}
				if swVTEPIP.Bits() != 32 {
					return status, fmt.Errorf("switch %s VTEP IP %s must be a /32", sw.Name, swVTEPIP) //nolint:err113
				}

				if !vtepSubnet.Contains(swVTEPIP.Addr()) {
					return status, fmt.Errorf("switch %s VTEP IP %s is not in the VTEP subnet %s", sw.Name, swVTEPIP, vtepSubnet) //nolint:err113
				}

				if sw.Spec.Redundancy.Type == fmeta.RedundancyTypeMCLAG {
					if peer, exist := mclagPeer[sw.Spec.Redundancy.Group]; exist {
						if peer.Spec.VTEPIP != sw.Spec.VTEPIP {
							return status, fmt.Errorf("mclag peers should have same VTEP IPs: %s and %s", sw.Name, peer.Name) //nolint:err113
						}
					} else {
						mclagPeer[sw.Spec.Redundancy.Group] = &sw

						if _, exist := vtepIPs[swVTEPIP.Addr()]; exist {
							return status, fmt.Errorf("switch %s VTEP IP %s is already in use", sw.Name, swVTEPIP) //nolint:err113
						}
					}
				} else if _, exist := vtepIPs[swVTEPIP.Addr()]; exist {
					return status, fmt.Errorf("switch %s VTEP IP %s is already in use", sw.Name, swVTEPIP) //nolint:err113
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
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s is not a /31", conn.Name, idx, spinePrefix) //nolint:err113
				}

				spineIP := spinePrefix.Addr()
				if !fabricSubnet.Contains(spineIP) {
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s is not in the fabric subnet %s", conn.Name, idx, spineIP, fabricSubnet) //nolint:err113
				}
				if _, exist := fabricIPs[spineIP]; exist {
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s is already in use", conn.Name, idx, spineIP) //nolint:err113
				}
				fabricIPs[spineIP] = true

				leafPrefix, err := netip.ParsePrefix(link.Leaf.IP)
				if err != nil {
					return status, fmt.Errorf("parsing fabric connection %s link %d leaf IP %s: %w", conn.Name, idx, link.Leaf.IP, err)
				}
				if leafPrefix.Bits() != 31 {
					return status, fmt.Errorf("fabric connection %s link %d leaf IP %s is not a /31", conn.Name, idx, leafPrefix) //nolint:err113
				}

				leafIP := leafPrefix.Addr()
				if !fabricSubnet.Contains(leafIP) {
					return status, fmt.Errorf("fabric connection %s link %d leaf IP %s is not in the fabric subnet %s", conn.Name, idx, leafIP, fabricSubnet) //nolint:err113
				}
				if _, exist := fabricIPs[leafIP]; exist {
					return status, fmt.Errorf("fabric connection %s link %d leaf IP %s is already in use", conn.Name, idx, leafIP) //nolint:err113
				}
				fabricIPs[leafIP] = true

				if spinePrefix.Masked() != leafPrefix.Masked() {
					return status, fmt.Errorf("fabric connection %s link %d spine IP %s and leaf IP %s are not in the same subnet", conn.Name, idx, spineIP, leafIP) //nolint:err113
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
					return status, fmt.Errorf("mesh connection %s link %d leaf1 IP %s is not a /31", conn.Name, idx, leaf1Prefix) //nolint:err113
				}

				leaf1IP := leaf1Prefix.Addr()
				if !fabricSubnet.Contains(leaf1IP) {
					return status, fmt.Errorf("mesh connection %s link %d leaf1 IP %s is not in the fabric subnet %s", conn.Name, idx, leaf1IP, fabricSubnet) //nolint:err113
				}
				if _, exist := fabricIPs[leaf1IP]; exist {
					return status, fmt.Errorf("mesh connection %s link %d leaf1 IP %s is already in use", conn.Name, idx, leaf1IP) //nolint:err113
				}
				fabricIPs[leaf1IP] = true

				leaf2Prefix, err := netip.ParsePrefix(link.Leaf2.IP)
				if err != nil {
					return status, fmt.Errorf("parsing mesh connection %s link %d leaf2 IP %s: %w", conn.Name, idx, link.Leaf2.IP, err)
				}
				if leaf2Prefix.Bits() != 31 {
					return status, fmt.Errorf("mesh connection %s link %d leaf2 IP %s is not a /31", conn.Name, idx, leaf2Prefix) //nolint:err113
				}

				leaf2IP := leaf2Prefix.Addr()
				if !fabricSubnet.Contains(leaf2IP) {
					return status, fmt.Errorf("mesh connection %s link %d leaf2 IP %s is not in the fabric subnet %s", conn.Name, idx, leaf2IP, fabricSubnet) //nolint:err113
				}
				if _, exist := fabricIPs[leaf2IP]; exist {
					return status, fmt.Errorf("mesh connection %s link %d leaf2 IP %s is already in use", conn.Name, idx, leaf2IP) //nolint:err113
				}
				fabricIPs[leaf2IP] = true

				if leaf1Prefix.Masked() != leaf2Prefix.Masked() {
					return status, fmt.Errorf("mesh connection %s link %d spine IP %s and leaf IP %s are not in the same subnet", conn.Name, idx, leaf1IP, leaf2IP) //nolint:err113
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
					return status, fmt.Errorf("gateway connection %s link %d switch IP %s is not a /31", conn.Name, idx, switchPrefix) //nolint:err113
				}

				switchIP := switchPrefix.Addr()
				if !fabricSubnet.Contains(switchIP) {
					return status, fmt.Errorf("gateway connection %s link %d switch IP %s is not in the fabric subnet %s", conn.Name, idx, switchIP, fabricSubnet) //nolint:err113
				}
				if _, exist := fabricIPs[switchIP]; exist {
					return status, fmt.Errorf("gateway connection %s link %d switch IP %s is already in use", conn.Name, idx, switchIP) //nolint:err113
				}
				fabricIPs[switchIP] = true

				gwPrefix, err := netip.ParsePrefix(link.Gateway.IP)
				if err != nil {
					return status, fmt.Errorf("parsing gateway connection %s link %d gateway IP %s: %w", conn.Name, idx, link.Gateway.IP, err)
				}
				if gwPrefix.Bits() != 31 {
					return status, fmt.Errorf("gateway connection %s link %d gateway IP %s is not a /31", conn.Name, idx, gwPrefix) //nolint:err113
				}

				gwIP := gwPrefix.Addr()
				if !fabricSubnet.Contains(gwIP) {
					return status, fmt.Errorf("gateway connection %s link %d gateway IP %s is not in the fabric subnet %s", conn.Name, idx, gwIP, fabricSubnet) //nolint:err113
				}
				if _, exist := fabricIPs[gwIP]; exist {
					return status, fmt.Errorf("gateway connection %s link %d gateway IP %s is already in use", conn.Name, idx, gwIP) //nolint:err113
				}
				fabricIPs[gwIP] = true

				if switchPrefix.Masked() != gwPrefix.Masked() {
					return status, fmt.Errorf("gateway connection %s link %d spine IP %s and gateway IP %s are not in the same subnet", conn.Name, idx, switchIP, gwIP) //nolint:err113
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
			domain, err := fabricDomain(fabrics, gw.Spec.Topology.Fabric, gw.Spec.Topology.Domain)
			if err != nil {
				return status, fmt.Errorf("gateway %s: %w", gw.Name, err)
			}
			if gw.Spec.ASN != domain.GatewayASN {
				return status, fmt.Errorf("gateway %s ASN %d is not %d", gw.Name, gw.Spec.ASN, domain.GatewayASN) //nolint:err113
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
				return status, fmt.Errorf("gateway %s protocol IP %s must be a /32", gw.Name, gwProtoIP) //nolint:err113
			}

			if !protocolSubnet.Contains(gwProtoIP.Addr()) {
				return status, fmt.Errorf("gateway %s protocol IP %s is not in the protocol subnet %s", gw.Name, gwProtoIP, protocolSubnet) //nolint:err113
			}

			if _, exist := protocolIPs[gwProtoIP.Addr()]; exist {
				return status, fmt.Errorf("gateway %s protocol IP %s is already in use", gw.Name, gwProtoIP) //nolint:err113
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
				return status, fmt.Errorf("gateway %s vtep IP %s must be a /32", gw.Name, gwVTEPIP) //nolint:err113
			}

			if !vtepSubnet.Contains(gwVTEPIP.Addr()) {
				return status, fmt.Errorf("gateway %s vtep IP %s is not in the vtep subnet %s", gw.Name, gwVTEPIP, vtepSubnet) //nolint:err113
			}

			if _, exist := vtepIPs[gwVTEPIP.Addr()]; exist {
				return status, fmt.Errorf("gateway %s vtep IP %s is already in use", gw.Name, gwVTEPIP) //nolint:err113
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

	return status, fmt.Errorf("invalid hydration status: total=%d, missing=%d", total, missing) //nolint:err113
}

// addrPool hands out addresses in order, skipping the ones already in use. Without any in use it gives the same
// sequence as a plain counter, which is what hydrating from scratch needs.
type addrPool struct {
	next netip.Addr
	step int
	used map[netip.Addr]bool
}

func newAddrPool(start netip.Addr, step int) *addrPool {
	return &addrPool{next: start, step: step, used: map[netip.Addr]bool{}}
}

func (p *addrPool) advance(addr netip.Addr) netip.Addr {
	for range p.step {
		addr = addr.Next()
	}

	return addr
}

func (p *addrPool) take() netip.Addr {
	for p.used[p.next] {
		p.next = p.advance(p.next)
	}

	addr := p.next
	p.used[addr] = true
	p.next = p.advance(p.next)

	return addr
}

// markUsed takes an address or a prefix and ignores empty and invalid values, as those are validated elsewhere
func (p *addrPool) markUsed(value string) {
	if value == "" {
		return
	}

	if prefix, err := netip.ParsePrefix(value); err == nil {
		p.used[prefix.Addr()] = true

		return
	}

	if addr, err := netip.ParseAddr(value); err == nil {
		p.used[addr] = true
	}
}

func isEvenAddr(addr netip.Addr) bool {
	b := addr.AsSlice()

	return b[len(b)-1]&1 == 0
}

// linkPartner returns the other address of the /31 the given address is in
func linkPartner(value string) (netip.Addr, error) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parsing link address %q: %w", value, err)
	}
	if prefix.Bits() != 31 {
		return netip.Addr{}, fmt.Errorf("link address %s is not a /31", value) //nolint:err113
	}

	b := prefix.Addr().AsSlice()
	b[len(b)-1] ^= 1
	addr, _ := netip.AddrFromSlice(b)

	return addr, nil
}

// fillLink sets the two ends of a link, which are the two addresses of a /31. Hydrating from scratch always takes
// the next free /31. With onlyMissing it keeps the ends that are set, a half set link gets the other address of its
// /31 (reserved when the used addresses were collected), and a link with no ends gets the next free /31.
func (p *addrPool) fillLink(a, b *string, onlyMissing bool) (bool, error) {
	if onlyMissing {
		switch {
		case *a != "" && *b != "":
			return false, nil
		case *a != "" || *b != "":
			set, missing := a, b
			if *a == "" {
				set, missing = b, a
			}

			partner, err := linkPartner(*set)
			if err != nil {
				return false, err
			}
			p.used[partner] = true
			*missing = partner.String() + "/31"

			return true, nil
		}
	}

	for p.used[p.next] || p.used[p.next.Next()] || !isEvenAddr(p.next) {
		p.next = p.next.Next()
	}
	first, second := p.next, p.next.Next()
	p.used[first], p.used[second] = true, true
	p.next = second.Next()
	*a, *b = first.String()+"/31", second.String()+"/31"

	return true, nil
}

// markLinkUsed marks the addresses of a link as used, and the other address of the /31 of a half set one, as that is
// the one fillLink gives its missing end
func (p *addrPool) markLinkUsed(a, b string) {
	p.markUsed(a)
	p.markUsed(b)

	if (a == "") != (b == "") {
		set := a
		if set == "" {
			set = b
		}
		if partner, err := linkPartner(set); err == nil {
			p.used[partner] = true
		}
	}
}

// asnPool hands out the ASNs of a range in order, skipping the ones in use. It only enforces the end of the range
// when limited, as hydrating from scratch leaves that to the validation that follows.
type asnPool struct {
	next    uint32
	end     uint32
	limited bool
	used    map[uint32]bool
}

func (p *asnPool) take() (uint32, error) {
	for p.used[p.next] {
		p.next++
	}
	if p.limited && p.next > p.end {
		return 0, fmt.Errorf("no free ASN left in the leaf ASN range ending at %d", p.end) //nolint:err113
	}

	asn := p.next
	p.used[asn] = true
	p.next++

	return asn, nil
}

const (
	gatewayVTEPMACPrefix = "CA:FE:BA:BE:01:"
	maxGatewayVTEPMACs   = 254
)

// hydrate allocates all the values from scratch, replacing the ones already set. The staged objects are
// left out of that, so the values of the installed ones do not depend on them, and get theirs afterwards.
func (c *Config) hydrate(ctx context.Context, kube kclient.Client) error {
	if err := c.hydrateValues(ctx, kube, false, true); err != nil {
		return err
	}

	return c.hydrateMissing(ctx, kube)
}

// hydrateMissing allocates only the values that are not set yet, continuing each pool after the ones in use, so
// objects added to a hydrated wiring get values that do not collide with the existing ones
func (c *Config) hydrateMissing(ctx context.Context, kube kclient.Client) error {
	return c.hydrateValues(ctx, kube, true, false)
}

func (c *Config) hydrateValues(ctx context.Context, kube kclient.Client, onlyMissing, skipStaged bool) error {
	mgmtSubnet, err := c.Fab.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing management subnet: %w", err)
	}

	controlVIP, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}

	if controlVIP.Addr() != mgmtSubnet.Masked().Addr().Next() {
		return fmt.Errorf("control VIP %s is not the first IP of the management subnet %s", controlVIP, mgmtSubnet) //nolint:err113
	}

	firstMgmtIP := controlVIP.Addr()
	for range 4 { // reserve few IPs for future use
		firstMgmtIP = firstMgmtIP.Next()
	}
	mgmtPool := newAddrPool(firstMgmtIP, 1)

	dummySubnet, err := c.Fab.Spec.Config.Control.DummySubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing dummy subnet: %w", err)
	}
	dummyPool := newAddrPool(dummySubnet.Masked().Addr(), 2)

	vtepSubnet, err := c.Fab.Spec.Config.Fabric.VTEPSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing VTEP subnet: %w", err)
	}
	vtepPool := newAddrPool(vtepSubnet.Masked().Addr(), 1)

	protocolSubnet, err := c.Fab.Spec.Config.Fabric.ProtocolSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing protocol subnet: %w", err)
	}
	protoPool := newAddrPool(protocolSubnet.Masked().Addr(), 1)

	fabricSubnet, err := c.Fab.Spec.Config.Fabric.FabricSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing fabric subnet: %w", err)
	}
	fabricPool := newAddrPool(fabricSubnet.Masked().Addr(), 1)

	fabrics, err := fabricSpecs(ctx, kube)
	if err != nil {
		return err
	}
	leafASNs := map[string]*asnPool{}
	for name, f := range fabrics {
		leafASNs[name] = &asnPool{next: f.LeafASNStart, end: f.LeafASNEnd, limited: onlyMissing, used: map[uint32]bool{}}
	}

	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}

	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns); err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}

	gateways := &gwapi.GatewayList{}
	if err := kube.List(ctx, gateways); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}
	if skipStaged {
		switches.Items = slices.DeleteFunc(switches.Items, func(sw wiringapi.Switch) bool { return apiutil.IsStaged(&sw) })
		conns.Items = slices.DeleteFunc(conns.Items, func(conn wiringapi.Connection) bool { return apiutil.IsStaged(&conn) })
		gateways.Items = slices.DeleteFunc(gateways.Items, func(gw gwapi.Gateway) bool { return apiutil.IsStaged(&gw) })
	}
	if len(gateways.Items) > maxGatewayVTEPMACs {
		return fmt.Errorf("too many gateways (%d) for our current VTEPMAC hydration scheme, maximum is %d", len(gateways.Items), maxGatewayVTEPMACs) //nolint:err113
	}
	usedMACOctets := map[uint8]bool{}

	// the ASN and VTEP IP an MCLAG group already has, whichever of its peers carries them
	groupASN := map[string]uint32{}
	groupVTEP := map[string]string{}

	if onlyMissing {
		for _, control := range c.Controls {
			mgmtPool.markUsed(string(control.Spec.Management.IP))
			dummyPool.markUsed(string(control.Spec.Dummy.IP))
		}
		for _, node := range c.Nodes {
			mgmtPool.markUsed(string(node.Spec.Management.IP))
			dummyPool.markUsed(string(node.Spec.Dummy.IP))
		}

		for _, sw := range switches.Items {
			mgmtPool.markUsed(sw.Spec.IP)
			protoPool.markUsed(sw.Spec.ProtocolIP)
			vtepPool.markUsed(sw.Spec.VTEPIP)
			if pool, ok := leafASNs[sw.Spec.Topology.Fabric]; ok && sw.Spec.Role.IsLeaf() && sw.Spec.ASN != 0 {
				pool.used[sw.Spec.ASN] = true
			}
			if sw.Spec.Redundancy.Type == fmeta.RedundancyTypeMCLAG {
				if sw.Spec.ASN != 0 {
					groupASN[sw.Spec.Redundancy.Group] = sw.Spec.ASN
				}
				if sw.Spec.VTEPIP != "" {
					groupVTEP[sw.Spec.Redundancy.Group] = sw.Spec.VTEPIP
				}
			}
		}

		for _, gw := range gateways.Items {
			protoPool.markUsed(gw.Spec.ProtocolIP)
			vtepPool.markUsed(gw.Spec.VTEPIP)

			var octet uint8
			if _, err := fmt.Sscanf(strings.ToUpper(gw.Spec.VTEPMAC), gatewayVTEPMACPrefix+"%02x", &octet); err == nil {
				usedMACOctets[octet] = true
			}
		}

		for _, conn := range conns.Items {
			switch {
			case conn.Spec.Fabric != nil:
				for _, link := range conn.Spec.Fabric.Links {
					fabricPool.markLinkUsed(link.Spine.IP, link.Leaf.IP)
				}
			case conn.Spec.Mesh != nil:
				for _, link := range conn.Spec.Mesh.Links {
					fabricPool.markLinkUsed(link.Leaf1.IP, link.Leaf2.IP)
				}
			case conn.Spec.Gateway != nil:
				for _, link := range conn.Spec.Gateway.Links {
					fabricPool.markLinkUsed(link.Switch.IP, link.Gateway.IP)
				}
			}
		}
	}

	slices.SortFunc(c.Controls, func(a, b fabapi.ControlNode) int {
		return cmp.Compare(a.Name, b.Name)
	})

	for idx := range c.Controls {
		control := &c.Controls[idx]
		if !onlyMissing || control.Spec.Management.IP == "" {
			control.Spec.Management.IP = meta.Prefix(netip.PrefixFrom(mgmtPool.take(), mgmtSubnet.Bits()).String())
		}
		if !onlyMissing || control.Spec.Dummy.IP == "" {
			control.Spec.Dummy.IP = meta.Prefix(netip.PrefixFrom(dummyPool.take(), 31).String())
		}
	}

	for idx := range c.Nodes {
		node := &c.Nodes[idx]
		if !onlyMissing || node.Spec.Management.IP == "" {
			node.Spec.Management.IP = meta.Prefix(netip.PrefixFrom(mgmtPool.take(), mgmtSubnet.Bits()).String())
		}
		if !onlyMissing || node.Spec.Dummy.IP == "" {
			node.Spec.Dummy.IP = meta.Prefix(netip.PrefixFrom(dummyPool.take(), 31).String())
		}
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

	origSwitches := map[string]wiringapi.SwitchSpec{}
	for idx := range switches.Items {
		sw := &switches.Items[idx]
		origSwitches[sw.Name] = *sw.Spec.DeepCopy()
		if sw.Spec.Role == "" {
			return fmt.Errorf("switch %s role is not set", sw.Name) //nolint:err113
		}
		if !slices.Contains(wiringapi.SwitchRoles, sw.Spec.Role) {
			return fmt.Errorf("switch %s role %q is invalid", sw.Name, sw.Spec.Role) //nolint:err113
		}

		if !onlyMissing || sw.Spec.IP == "" {
			sw.Spec.IP = netip.PrefixFrom(mgmtPool.take(), mgmtSubnet.Bits()).String()
		}

		if !onlyMissing || sw.Spec.ProtocolIP == "" {
			sw.Spec.ProtocolIP = netip.PrefixFrom(protoPool.take(), 32).String()
		}

		swFabric := sw.Spec.Topology.Fabric
		if _, ok := fabrics[swFabric]; !ok {
			return fmt.Errorf("switch %s fabric %s not found", sw.Name, swFabric) //nolint:err113
		}

		if sw.Spec.Role.IsSpine() {
			domain, err := fabricDomain(fabrics, swFabric, sw.Spec.Topology.Domains[0])
			if err != nil {
				return fmt.Errorf("spine %s: %w", sw.Name, err)
			}
			sw.Spec.ASN = domain.SpineASN
		}

		if sw.Spec.Redundancy.Type == fmeta.RedundancyTypeMCLAG {
			if peer, exist := mclagPeer[sw.Spec.Redundancy.Group]; exist {
				if !onlyMissing || sw.Spec.ASN == 0 {
					sw.Spec.ASN = peer.Spec.ASN
				}
				if !onlyMissing || sw.Spec.VTEPIP == "" {
					sw.Spec.VTEPIP = peer.Spec.VTEPIP
				}

				continue
			}

			mclagPeer[sw.Spec.Redundancy.Group] = sw

			// the first peer by name can be the new one, so it takes what the group already has before drawing from the pools
			if onlyMissing {
				if sw.Spec.ASN == 0 {
					sw.Spec.ASN = groupASN[sw.Spec.Redundancy.Group]
				}
				if sw.Spec.VTEPIP == "" {
					sw.Spec.VTEPIP = groupVTEP[sw.Spec.Redundancy.Group]
				}
			}
		}

		if sw.Spec.Role.IsLeaf() {
			if !onlyMissing || sw.Spec.ASN == 0 {
				asn, err := leafASNs[swFabric].take()
				if err != nil {
					return fmt.Errorf("leaf %s of fabric %s: %w", sw.Name, swFabric, err)
				}
				sw.Spec.ASN = asn
			}

			if !onlyMissing || sw.Spec.VTEPIP == "" {
				sw.Spec.VTEPIP = netip.PrefixFrom(vtepPool.take(), 32).String()
			}
		}
	}

	for _, sw := range switches.Items {
		if onlyMissing && reflect.DeepEqual(origSwitches[sw.Name], sw.Spec) {
			continue
		}

		if err := kube.Update(ctx, &sw); err != nil {
			return fmt.Errorf("updating switch %s: %w", sw.Name, err)
		}
	}

	slices.SortFunc(conns.Items, func(a, b wiringapi.Connection) int {
		return cmp.Compare(a.Name, b.Name)
	})

	for _, conn := range conns.Items {
		changed := !onlyMissing

		if conn.Spec.Fabric != nil { //nolint:gocritic
			cf := conn.Spec.Fabric

			for idx := range cf.Links {
				link := &cf.Links[idx]
				linkChanged, err := fabricPool.fillLink(&link.Spine.IP, &link.Leaf.IP, onlyMissing)
				if err != nil {
					return fmt.Errorf("connection %s: %w", conn.Name, err)
				}
				changed = changed || linkChanged
			}
		} else if conn.Spec.Mesh != nil {
			cm := conn.Spec.Mesh

			for idx := range cm.Links {
				link := &cm.Links[idx]
				linkChanged, err := fabricPool.fillLink(&link.Leaf1.IP, &link.Leaf2.IP, onlyMissing)
				if err != nil {
					return fmt.Errorf("connection %s: %w", conn.Name, err)
				}
				changed = changed || linkChanged
			}
		} else if conn.Spec.Gateway != nil {
			cg := conn.Spec.Gateway

			for idx := range cg.Links {
				link := &cg.Links[idx]
				linkChanged, err := fabricPool.fillLink(&link.Switch.IP, &link.Gateway.IP, onlyMissing)
				if err != nil {
					return fmt.Errorf("connection %s: %w", conn.Name, err)
				}
				if !linkChanged {
					continue
				}
				changed = true

				gwName := link.Gateway.DeviceName()
				gw := &gwapi.Gateway{}
				if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: gwName}, gw); err != nil {
					return fmt.Errorf("getting gateway %s: %w", gwName, err)
				}
				if gw.Spec.Interfaces == nil {
					gw.Spec.Interfaces = map[string]gwapi.GatewayInterface{}
				}

				iface, exist := gw.Spec.Interfaces[link.Gateway.LocalPortName()]
				if !exist {
					return fmt.Errorf("gateway %s does not have interface %s", gwName, link.Gateway.LocalPortName()) //nolint:err113
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

		if !changed {
			continue
		}

		if err := kube.Update(ctx, &conn); err != nil {
			return fmt.Errorf("updating connection %s: %w", conn.Name, err)
		}
	}

	// the gateway links above updated the gateways in the cluster, so read them again before filling in their own values
	gateways = &gwapi.GatewayList{}
	if err := kube.List(ctx, gateways); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}
	const MaxVTEPMACs = 254
	if len(gateways.Items) > MaxVTEPMACs {
		return fmt.Errorf("too many gateways (%d) for our current VTEPMAC hydration scheme, maximum is %d", len(gateways.Items), MaxVTEPMACs) //nolint:err113
	}
	if skipStaged {
		gateways.Items = slices.DeleteFunc(gateways.Items, func(gw gwapi.Gateway) bool { return apiutil.IsStaged(&gw) })
	}

	nextMACFinalOctet := uint8(1)

	slices.SortFunc(gateways.Items, func(a, b gwapi.Gateway) int {
		return cmp.Compare(a.Name, b.Name)
	})

	for _, gw := range gateways.Items {
		origGW := *gw.Spec.DeepCopy()
		domain, err := fabricDomain(fabrics, gw.Spec.Topology.Fabric, gw.Spec.Topology.Domain)
		if err != nil {
			return fmt.Errorf("gateway %s: %w", gw.Name, err)
		}
		gw.Spec.ASN = domain.GatewayASN

		if !onlyMissing || gw.Spec.ProtocolIP == "" {
			gw.Spec.ProtocolIP = netip.PrefixFrom(protoPool.take(), 32).String()
		}

		if !onlyMissing || gw.Spec.VTEPIP == "" {
			gw.Spec.VTEPIP = netip.PrefixFrom(vtepPool.take(), 32).String()
		}

		if !onlyMissing || gw.Spec.VTEPMAC == "" {
			for usedMACOctets[nextMACFinalOctet] {
				nextMACFinalOctet++
			}
			gw.Spec.VTEPMAC = fmt.Sprintf("%s%02x", gatewayVTEPMACPrefix, nextMACFinalOctet)
			usedMACOctets[nextMACFinalOctet] = true
			nextMACFinalOctet++
		}
		gw.Spec.VTEPMTU = fabric.ServerFacingMTU

		if onlyMissing && reflect.DeepEqual(origGW, gw.Spec) {
			continue
		}

		if err := kube.Update(ctx, &gw); err != nil {
			return fmt.Errorf("updating gateway %s: %w", gw.Name, err)
		}
	}

	return nil
}
