// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type VLABBuilder struct {
	SpinesCount        uint8  // number of spines to generate
	FabricLinksCount   uint8  // number of links for each spine <> leaf pair
	MeshLinksCount     uint8  // number of mesh links for each leaf <> leaf pair
	MCLAGLeafsCount    uint8  // number of MCLAG server-leafs to generate
	ESLAGLeafGroups    string // eslag leaf groups - comma separated list of number of ESLAG switches in each group, should be 2-4 per group, e.g. 2,4,2 for 3 groups with 2, 4 and 2 switches
	OrphanLeafsCount   uint8  // number of non-MCLAG server-leafs to generate
	MCLAGSessionLinks  uint8  // number of MCLAG session links to generate
	MCLAGPeerLinks     uint8  // number of MCLAG peer links to generate
	MCLAGServers       uint8  // number of MCLAG servers to generate for MCLAG switches
	ESLAGServers       uint8  // number of ESLAG servers to generate for ESLAG switches
	UnbundledServers   uint8  // number of unbundled servers to generate for switches (only for one of the first switch in the redundancy group or orphan switch)
	BundledServers     uint8  // number of bundled servers to generate for switches (only for one of the second switch in the redundancy group or orphan switch)
	NoSwitches         bool   // do not generate any switches
	GatewayUplinks     uint8  // number of uplinks for gateway node to the spines
	ExtCount           uint8  // number of "externals" to generate
	ExtMCLAGConnCount  uint8  // number of external connections to generate from MCLAG leaves
	ExtESLAGConnCount  uint8  // number of external connections to generate from ESLAG leaves
	ExtOrphanConnCount uint8  // number of external connections to generate from orphan leaves

	data         *apiutil.Loader
	ifaceTracker map[string]uint8 // next available interface ID for each switch
	switchID     uint             // switch ID counter
}

func (b *VLABBuilder) Build(ctx context.Context, l *apiutil.Loader, fabricMode meta.FabricMode, nodes []fabapi.FabNode) error {
	if l == nil {
		return fmt.Errorf("loader is nil") //nolint:goerr113
	}
	b.data = l

	switch fabricMode {
	case meta.FabricModeSpineLeaf:
		if b.MeshLinksCount > 0 && b.FabricLinksCount > 0 {
			return fmt.Errorf("cannot use both mesh and fabric links at the same time") //nolint:goerr113
		}
		if !b.NoSwitches {
			if b.MeshLinksCount == 0 {
				// existing defaults for spine-link
				if b.SpinesCount == 0 && b.MeshLinksCount == 0 {
					b.SpinesCount = 2
				}
				if b.FabricLinksCount == 0 && b.MeshLinksCount == 0 {
					b.FabricLinksCount = 2
				}
				if b.MCLAGLeafsCount == 0 && b.OrphanLeafsCount == 0 && b.ESLAGLeafGroups == "" {
					b.MCLAGLeafsCount = 2
					b.ESLAGLeafGroups = "2"
					b.OrphanLeafsCount = 1
				}
			} else {
				// new defaults for mesh
				if b.SpinesCount != 0 {
					return fmt.Errorf("spines not supported for when using mesh connections") //nolint:goerr113
				}
				if b.MCLAGLeafsCount == 0 && b.OrphanLeafsCount == 0 && b.ESLAGLeafGroups == "" {
					b.ESLAGLeafGroups = "2"
					b.OrphanLeafsCount = 1
				}
			}
		}
	case meta.FabricModeCollapsedCore:
		if b.SpinesCount > 0 {
			return fmt.Errorf("spines not supported for collapsed core fabric mode") //nolint:goerr113
		}
		if b.FabricLinksCount > 0 {
			return fmt.Errorf("fabric links not supported for collapsed core fabric mode") //nolint:goerr113
		}

		if !b.NoSwitches && b.MCLAGLeafsCount == 0 {
			b.MCLAGLeafsCount = 2
		}
		if b.MCLAGLeafsCount > 2 {
			return fmt.Errorf("MCLAG leafs count must be 2 for collapsed core fabric mode") //nolint:goerr113
		}
		if b.OrphanLeafsCount > 0 {
			return fmt.Errorf("orphan leafs not supported for collapsed core fabric mode") //nolint:goerr113
		}

		if b.ESLAGLeafGroups != "" {
			return fmt.Errorf("ESLAG not supported for collapsed core fabric mode") //nolint:goerr113
		}
	default:
		return fmt.Errorf("unsupported fabric mode %s", fabricMode) //nolint:goerr113
	}

	if b.MCLAGLeafsCount > 0 {
		if b.MCLAGSessionLinks == 0 {
			b.MCLAGSessionLinks = 2
		}
		if b.MCLAGPeerLinks == 0 {
			b.MCLAGPeerLinks = 2
		}
	}

	isGw := false
	gw := fabapi.FabNode{}
	for _, node := range nodes {
		if slices.Contains(node.Spec.Roles, fabapi.NodeRoleGateway) {
			if isGw {
				return fmt.Errorf("multiple gateway nodes not supported") //nolint:goerr113
			}

			isGw = true
			gw = node
		}
	}

	if isGw {
		if fabricMode != meta.FabricModeSpineLeaf {
			return fmt.Errorf("gateway node only supported for spine-leaf fabric mode") //nolint:goerr113
		}

		if b.GatewayUplinks == 0 {
			return fmt.Errorf("gateway uplinks count must be greater than 0") //nolint:goerr113
		}

		totalESLAGLeafs := 0
		if b.ESLAGLeafGroups != "" {
			for _, g := range strings.Split(b.ESLAGLeafGroups, ",") {
				if v, err := strconv.Atoi(strings.TrimSpace(g)); err == nil {
					totalESLAGLeafs += v
				}
			}
		}

		if b.MeshLinksCount > 0 {
			if b.GatewayUplinks > uint8(totalESLAGLeafs)+b.OrphanLeafsCount { //nolint:gosec
				return fmt.Errorf("gateway uplinks count must be ≤ total leaf switches (ESLAG + orphan)") //nolint:goerr113
			}
		} else {
			if b.GatewayUplinks > b.SpinesCount {
				return fmt.Errorf("gateway uplinks count must be ≤ spines count") //nolint:goerr113
			}
		}
	}

	totalESLAGLeafs := uint8(0)
	eslagLeafGroups := []uint8{}

	if b.ESLAGLeafGroups != "" {
		parts := strings.Split(b.ESLAGLeafGroups, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			leafs, err := strconv.ParseUint(part, 10, 8)
			if err != nil {
				return fmt.Errorf("invalid ESLAG leaf group %s", part) //nolint:goerr113
			}

			if leafs < 2 || leafs > 4 {
				return fmt.Errorf("ESLAG leaf group must have 2-4 leafs") //nolint:goerr113
			}

			totalESLAGLeafs += uint8(leafs)
			eslagLeafGroups = append(eslagLeafGroups, uint8(leafs))
		}
	}

	if b.MCLAGLeafsCount%2 != 0 {
		return fmt.Errorf("MCLAG leafs count must be even") //nolint:goerr113
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGSessionLinks == 0 {
		return fmt.Errorf("MCLAG session links count must be greater than 0") //nolint:goerr113
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGPeerLinks == 0 {
		return fmt.Errorf("MCLAG peer links count must be greater than 0") //nolint:goerr113
	}

	if b.ExtESLAGConnCount > totalESLAGLeafs {
		return fmt.Errorf("external ESLAG connections count must be less than or equal to total ESLAG leaves") //nolint:goerr113
	}
	if b.ExtMCLAGConnCount > b.MCLAGLeafsCount {
		return fmt.Errorf("external MCLAG connections count must be less than or equal to MCLAG leaves") //nolint:goerr113
	}
	if b.ExtOrphanConnCount > b.OrphanLeafsCount {
		return fmt.Errorf("external orphan connections count must be less than or equal to orphan leaves") //nolint:goerr113
	}

	// warn about https://github.com/githedgehog/internal/issues/145 if there are multiple external connections
	if b.ExtMCLAGConnCount+b.ExtESLAGConnCount+b.ExtOrphanConnCount > 1 {
		slog.Warn("Multiple external connections are not supported if using virtual switches",
			"extMCLAGConnCount", b.ExtMCLAGConnCount,
			"extESLAGConnCount", b.ExtESLAGConnCount,
			"extOrphanConnCount", b.ExtOrphanConnCount,
		)
	}

	slog.Info("Building VLAB wiring diagram", "fabricMode", fabricMode)
	if fabricMode == meta.FabricModeSpineLeaf {
		slog.Info(">>>", "spinesCount", b.SpinesCount, "fabricLinksCount", b.FabricLinksCount, "meshLinksCount", b.MeshLinksCount)
		slog.Info(">>>", "eslagLeafGroups", b.ESLAGLeafGroups)
		if isGw {
			slog.Info(">>>", "gatewayUplinks", b.GatewayUplinks)
		}
	}
	slog.Info(">>>", "mclagLeafsCount", b.MCLAGLeafsCount, "mclagSessionLinks", b.MCLAGSessionLinks, "mclagPeerLinks", b.MCLAGPeerLinks)
	slog.Info(">>>", "orphanLeafsCount", b.OrphanLeafsCount)
	slog.Info(">>>", "mclagServers", b.MCLAGServers, "eslagServers", b.ESLAGServers, "unbundledServers", b.UnbundledServers, "bundledServers", b.BundledServers)
	slog.Info(">>>", "externalCount", b.ExtCount, "externalMclagConnCount", b.ExtMCLAGConnCount, "externalEslagConnCount", b.ExtESLAGConnCount, "externalOrphanConnCount", b.ExtOrphanConnCount)

	if err := b.data.Add(ctx, &wiringapi.VLANNamespace{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       wiringapi.KindVLANNamespace,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name: "default",
		},
		Spec: wiringapi.VLANNamespaceSpec{
			Ranges: []meta.VLANRange{
				{From: 1000, To: 2999},
			},
		},
	}); err != nil {
		return fmt.Errorf("creating VLAN namespace: %w", err) //nolint:goerr113
	}

	if err := b.data.Add(ctx, &vpcapi.IPv4Namespace{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       vpcapi.KindIPv4Namespace,
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name: "default",
		},
		Spec: vpcapi.IPv4NamespaceSpec{
			Subnets: []string{
				"10.0.0.0/16",
			},
		},
	}); err != nil {
		return fmt.Errorf("creating IPv4 namespace: %w", err) //nolint:goerr113
	}

	b.ifaceTracker = map[string]uint8{}

	for _, node := range nodes {
		if slices.Contains(node.Spec.Roles, fabapi.NodeRoleGateway) {
			gwName := node.Name

			ifaces := map[string]gwapi.GatewayInterface{}
			for i := uint8(1); i <= b.GatewayUplinks; i++ {
				ifaceName := fmt.Sprintf("enp2s%d", i)
				ifaces[ifaceName] = gwapi.GatewayInterface{}
			}

			if _, err := b.createGateway(ctx, gwName, gwapi.GatewaySpec{
				Interfaces: ifaces,
				// Neighbors will be later hydrated in based on the gateway connections
			}); err != nil {
				return err
			}
		}
	}

	if _, err := b.createSwitchGroup(ctx, "empty"); err != nil {
		return err
	}

	switchID := uint8(1) // switch ID counter

	leafID := uint8(1)   // leaf ID counter
	serverID := uint8(1) // server ID counter
	externalConns := []wiringapi.Connection{}
	extMCLAGConns := uint8(0)
	extESLAGConns := uint8(0)
	extOrphanConns := uint8(0)

	for mclagID := uint8(1); mclagID <= b.MCLAGLeafsCount/2; mclagID++ {
		leaf1Name := fmt.Sprintf("leaf-%02d", leafID)
		leaf2Name := fmt.Sprintf("leaf-%02d", leafID+1)

		sg := fmt.Sprintf("mclag-%d", mclagID)
		if _, err := b.createSwitchGroup(ctx, sg); err != nil {
			return err
		}

		if _, err := b.createSwitch(ctx, leaf1Name, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d MCLAG %d", switchID, mclagID),
			Groups:      []string{sg},
			Redundancy: wiringapi.SwitchRedundancy{
				Group: sg,
				Type:  meta.RedundancyTypeMCLAG,
			},
		}); err != nil {
			return err
		}
		if _, err := b.createSwitch(ctx, leaf2Name, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d MCLAG %d", switchID+1, mclagID),
			Groups:      []string{sg},
			Redundancy: wiringapi.SwitchRedundancy{
				Group: sg,
				Type:  meta.RedundancyTypeMCLAG,
			},
		}); err != nil {
			return err
		}

		switchID += 2
		leafID += 2

		sessionLinks := []wiringapi.SwitchToSwitchLink{}
		for i := uint8(0); i < b.MCLAGSessionLinks; i++ {
			sessionLinks = append(sessionLinks, wiringapi.SwitchToSwitchLink{
				Switch1: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf1Name)},
				Switch2: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf2Name)},
			})
		}

		peerLinks := []wiringapi.SwitchToSwitchLink{}
		for i := uint8(0); i < b.MCLAGPeerLinks; i++ {
			peerLinks = append(peerLinks, wiringapi.SwitchToSwitchLink{
				Switch1: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf1Name)},
				Switch2: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf2Name)},
			})
		}

		if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
			MCLAGDomain: &wiringapi.ConnMCLAGDomain{
				SessionLinks: sessionLinks,
				PeerLinks:    peerLinks,
			},
		}); err != nil {
			return err
		}
		if b.ExtMCLAGConnCount > 0 {
			var err error
			if extMCLAGConns < b.ExtMCLAGConnCount {
				externalConns, err = b.addExternalConnection(ctx, externalConns, leaf1Name)
				if err != nil {
					return err
				}
				extMCLAGConns++
			}
			if extMCLAGConns < b.ExtMCLAGConnCount {
				externalConns, err = b.addExternalConnection(ctx, externalConns, leaf2Name)
				if err != nil {
					return err
				}
				extMCLAGConns++
			}
		}

		for i := 0; i < int(b.MCLAGServers); i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d MCLAG %s %s", serverID, leaf1Name, leaf2Name),
			}); err != nil {
				return err
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				MCLAG: &wiringapi.ConnMCLAG{
					Links: []wiringapi.ServerToSwitchLink{
						{
							Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
							Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf1Name)},
						},
						{
							Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
							Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf2Name)},
						},
					},
				},
			}); err != nil {
				return err
			}

			serverID++
		}

		for i := 0; i < int(b.UnbundledServers); i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Unbundled %s", serverID, leaf1Name),
			}); err != nil {
				return err
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Unbundled: &wiringapi.ConnUnbundled{
					Link: wiringapi.ServerToSwitchLink{
						Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
						Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf1Name)},
					},
				},
			}); err != nil {
				return err
			}

			serverID++
		}

		for i := 0; i < int(b.BundledServers); i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Bundled %s", serverID, leaf2Name),
			}); err != nil {
				return err
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Bundled: &wiringapi.ConnBundled{
					Links: []wiringapi.ServerToSwitchLink{
						{
							Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
							Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf2Name)},
						},
						{
							Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
							Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf2Name)},
						},
					},
				},
			}); err != nil {
				return err
			}

			serverID++
		}
	}

	for eslagID := uint8(0); eslagID < uint8(len(eslagLeafGroups)); eslagID++ { //nolint:gosec
		sg := fmt.Sprintf("eslag-%d", eslagID+1)
		if _, err := b.createSwitchGroup(ctx, sg); err != nil {
			return err
		}

		leafs := eslagLeafGroups[eslagID]
		leafNames := []string{}
		for eslagLeafID := uint8(0); eslagLeafID < leafs; eslagLeafID++ {
			leafName := fmt.Sprintf("leaf-%02d", leafID+eslagLeafID)
			leafNames = append(leafNames, leafName)

			if _, err := b.createSwitch(ctx, leafName, wiringapi.SwitchSpec{
				Role:        wiringapi.SwitchRoleServerLeaf,
				Description: fmt.Sprintf("VS-%02d ESLAG %d", switchID+eslagLeafID, eslagID+1),
				Groups:      []string{sg},
				Redundancy: wiringapi.SwitchRedundancy{
					Group: sg,
					Type:  meta.RedundancyTypeESLAG,
				},
			}); err != nil {
				return err
			}
			if extESLAGConns < b.ExtESLAGConnCount {
				var err error
				externalConns, err = b.addExternalConnection(ctx, externalConns, leafName)
				if err != nil {
					return err
				}
				extESLAGConns++
			}
		}

		switchID += leafs
		leafID += leafs

		for i := 0; i < int(b.ESLAGServers); i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			leafNamesStr := strings.Join(leafNames, " ")

			if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d ESLAG %s", serverID, leafNamesStr),
			}); err != nil {
				return err
			}

			links := []wiringapi.ServerToSwitchLink{}
			for _, leafName := range leafNames {
				links = append(links, wiringapi.ServerToSwitchLink{
					Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
					Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafName)},
				})
			}
			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				ESLAG: &wiringapi.ConnESLAG{
					Links: links,
				},
			}); err != nil {
				return err
			}

			serverID++
		}

		for i := 0; i < int(b.UnbundledServers); i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Unbundled %s", serverID, leafNames[0]),
			}); err != nil {
				return err
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Unbundled: &wiringapi.ConnUnbundled{
					Link: wiringapi.ServerToSwitchLink{
						Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
						Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafNames[0])},
					},
				},
			}); err != nil {
				return err
			}

			serverID++
		}

		if leafs > 1 {
			for i := 0; i < int(b.BundledServers); i++ {
				serverName := fmt.Sprintf("server-%02d", serverID)

				if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
					Description: fmt.Sprintf("S-%02d Bundled %s", serverID, leafNames[1]),
				}); err != nil {
					return err
				}

				if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
					Bundled: &wiringapi.ConnBundled{
						Links: []wiringapi.ServerToSwitchLink{
							{
								Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
								Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafNames[1])},
							},
							{
								Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
								Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafNames[1])},
							},
						},
					},
				}); err != nil {
					return err
				}

				serverID++
			}
		}
	}

	for idx := uint8(1); idx <= b.OrphanLeafsCount; idx++ {
		leafName := fmt.Sprintf("leaf-%02d", leafID)

		if _, err := b.createSwitch(ctx, leafName, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d", switchID),
		}); err != nil {
			return err
		}

		if extOrphanConns < b.ExtOrphanConnCount {
			var err error
			externalConns, err = b.addExternalConnection(ctx, externalConns, leafName)
			if err != nil {
				return err
			}
			extOrphanConns++
		}

		switchID++
		leafID++

		for i := 0; i < int(b.UnbundledServers); i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Unbundled %s", serverID, leafName),
			}); err != nil {
				return err
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Unbundled: &wiringapi.ConnUnbundled{
					Link: wiringapi.ServerToSwitchLink{
						Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
						Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafName)},
					},
				},
			}); err != nil {
				return err
			}

			serverID++
		}

		for i := 0; i < int(b.BundledServers); i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(ctx, serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Bundled %s", serverID, leafName),
			}); err != nil {
				return err
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Bundled: &wiringapi.ConnBundled{
					Links: []wiringapi.ServerToSwitchLink{
						{
							Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
							Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafName)},
						},
						{
							Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
							Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafName)},
						},
					},
				},
			}); err != nil {
				return err
			}

			serverID++
		}
	}

	for spineID := uint8(1); spineID <= b.SpinesCount; spineID++ {
		spineName := fmt.Sprintf("spine-%02d", spineID)

		if _, err := b.createSwitch(ctx, spineName, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleSpine,
			Description: fmt.Sprintf("VS-%02d", switchID),
		}); err != nil {
			return err
		}

		switchID++

		for leafID := uint8(1); leafID <= b.MCLAGLeafsCount+b.OrphanLeafsCount+totalESLAGLeafs; leafID++ {
			leafName := fmt.Sprintf("leaf-%02d", leafID)

			links := []wiringapi.FabricLink{}
			for spinePortID := uint8(0); spinePortID < b.FabricLinksCount; spinePortID++ {
				spinePort := b.nextSwitchPort(spineName)
				leafPort := b.nextSwitchPort(leafName)

				links = append(links, wiringapi.FabricLink{
					Spine: wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: spinePort}},
					Leaf:  wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: leafPort}},
				})
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Fabric: &wiringapi.ConnFabric{
					Links: links,
				},
			}); err != nil {
				return err
			}
		}

		if isGw && spineID <= b.GatewayUplinks {
			switchPort := b.nextSwitchPort(spineName)
			gwPort := fmt.Sprintf("%s/enp2s%d", gw.Name, spineID)

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Gateway: &wiringapi.ConnGateway{
					Links: []wiringapi.GatewayLink{
						{
							Switch:  wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: switchPort}},
							Gateway: wiringapi.ConnGatewayLinkGateway{BasePortName: wiringapi.BasePortName{Port: gwPort}},
						},
					},
				},
			}); err != nil {
				return err
			}
		}
	}

	if b.MeshLinksCount > 0 {
		for leaf1ID := uint8(1); leaf1ID <= b.MCLAGLeafsCount+b.OrphanLeafsCount+totalESLAGLeafs; leaf1ID++ {
			leaf1Name := fmt.Sprintf("leaf-%02d", leaf1ID)

			for leaf2ID := leaf1ID + 1; leaf2ID <= b.MCLAGLeafsCount+b.OrphanLeafsCount+totalESLAGLeafs; leaf2ID++ {
				leaf2Name := fmt.Sprintf("leaf-%02d", leaf2ID)

				links := []wiringapi.MeshLink{}
				for leaf1PortID := uint8(0); leaf1PortID < b.MeshLinksCount; leaf1PortID++ {
					leaf1Port := b.nextSwitchPort(leaf1Name)
					leaf2Port := b.nextSwitchPort(leaf2Name)

					links = append(links, wiringapi.MeshLink{
						Leaf1: wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: leaf1Port}},
						Leaf2: wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: leaf2Port}},
					})
				}

				if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
					Mesh: &wiringapi.ConnMesh{
						Links: links,
					},
				}); err != nil {
					return err
				}
			}
		}
	}

	if b.MeshLinksCount > 0 && isGw {
		connectedLeafs := uint8(0)
		for leafID := uint8(1); leafID <= b.MCLAGLeafsCount+b.OrphanLeafsCount+totalESLAGLeafs && connectedLeafs < b.GatewayUplinks; leafID++ {
			leafName := fmt.Sprintf("leaf-%02d", leafID)
			switchPort := b.nextSwitchPort(leafName)
			gwPort := fmt.Sprintf("%s/enp2s%d", gw.Name, connectedLeafs+1)

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				Gateway: &wiringapi.ConnGateway{
					Links: []wiringapi.GatewayLink{
						{
							Switch:  wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: switchPort}},
							Gateway: wiringapi.ConnGatewayLinkGateway{BasePortName: wiringapi.BasePortName{Port: gwPort}},
						},
					},
				},
			}); err != nil {
				return err
			}

			connectedLeafs++
		}
	}

	if b.ExtCount > 0 {
		externals := []vpcapi.External{}
		extAsn := 64102
		inboundCommPrefix := 65102
		communityRuleID := 1000

		for i := uint8(1); i <= b.ExtCount; i++ {
			externalName := fmt.Sprintf("external-%02d", i)
			externalSpec := vpcapi.ExternalSpec{
				IPv4Namespace:     "default",
				InboundCommunity:  fmt.Sprintf("%d:%d", inboundCommPrefix, communityRuleID),
				OutboundCommunity: fmt.Sprintf("%d:%d", extAsn, communityRuleID),
			}
			ext, err := b.createExternal(ctx, externalName, externalSpec)
			if err != nil {
				return err
			}
			externals = append(externals, *ext)
			communityRuleID += 100
		}

		// create attachments per external and external connection
		connOctet := uint8(0)
		for _, conn := range externalConns {
			connOctet++
			vlanID := uint16(10)
			for _, ext := range externals {
				extAttachName := fmt.Sprintf("%s--%s", conn.Spec.External.Link.Switch.DeviceName(), ext.Name)
				extAttachSpec := vpcapi.ExternalAttachmentSpec{
					External:   ext.Name,
					Connection: conn.Name,
					Switch: vpcapi.ExternalAttachmentSwitch{
						VLAN: vlanID,
						IP:   fmt.Sprintf("100.%d.%d.1/24", connOctet, vlanID),
					},
					Neighbor: vpcapi.ExternalAttachmentNeighbor{
						ASN: uint32(extAsn),
						IP:  fmt.Sprintf("100.%d.%d.6", connOctet, vlanID),
					},
				}
				vlanID += 10
				if _, err := b.createExternalAttach(ctx, extAttachName, extAttachSpec); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (b *VLABBuilder) addExternalConnection(ctx context.Context, extConnList []wiringapi.Connection, switchName string) ([]wiringapi.Connection, error) {
	extConnSpec := wiringapi.ConnExternal{
		Link: wiringapi.ConnExternalLink{
			Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(switchName)},
		},
	}
	extConn, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
		External: &extConnSpec,
	})
	if err != nil {
		return extConnList, err
	}

	return append(extConnList, *extConn), nil
}

func (b *VLABBuilder) nextSwitchPort(switchName string) string {
	ifaceID := b.ifaceTracker[switchName]
	portName := fmt.Sprintf("%s/E1/%d", switchName, ifaceID+1)

	offset := 1
	if ifaceID >= 48 {
		offset = 4
	}
	ifaceID += uint8(offset) //nolint:gosec

	if ifaceID > 76 {
		slog.Error("Too many interfaces for switch", "switch", switchName)
	}

	b.ifaceTracker[switchName] = ifaceID

	return portName
}

func (b *VLABBuilder) nextServerPort(serverName string) string {
	ifaceID := b.ifaceTracker[serverName]
	portName := fmt.Sprintf("%s/enp2s%d", serverName, ifaceID+1) // value for VLAB
	ifaceID++
	b.ifaceTracker[serverName] = ifaceID

	return portName
}

func (b *VLABBuilder) createSwitchGroup(ctx context.Context, name string) (*wiringapi.SwitchGroup, error) { //nolint:unparam
	sg := &wiringapi.SwitchGroup{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       wiringapi.KindSwitchGroup,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Spec: wiringapi.SwitchGroupSpec{},
	}

	if err := b.data.Add(ctx, sg); err != nil {
		return nil, fmt.Errorf("creating switch group %s: %w", name, err) //nolint:goerr113
	}

	return sg, nil
}

func (b *VLABBuilder) createSwitch(ctx context.Context, name string, spec wiringapi.SwitchSpec) (*wiringapi.Switch, error) { //nolint:unparam
	spec.Profile = meta.SwitchProfileVS
	spec.Boot.MAC = fmt.Sprintf(VLABSwitchMACTmpl, b.switchID)
	b.switchID++

	sw := &wiringapi.Switch{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       wiringapi.KindSwitch,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}

	if err := b.data.Add(ctx, sw); err != nil {
		return nil, fmt.Errorf("creating switch %s: %w", name, err) //nolint:goerr113
	}

	return sw, nil
}

func (b *VLABBuilder) createServer(ctx context.Context, name string, spec wiringapi.ServerSpec) (*wiringapi.Server, error) { //nolint:unparam
	server := &wiringapi.Server{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       wiringapi.KindServer,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}

	if err := b.data.Add(ctx, server); err != nil {
		return nil, fmt.Errorf("creating server %s: %w", name, err) //nolint:goerr113
	}

	return server, nil
}

func (b *VLABBuilder) createGateway(ctx context.Context, name string, spec gwapi.GatewaySpec) (*gwapi.Gateway, error) {
	gw := &gwapi.Gateway{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       "Gateway",
			APIVersion: gwapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: comp.FabNamespace,
		},
		Spec: spec,
	}

	if err := b.data.Add(ctx, gw); err != nil {
		return nil, fmt.Errorf("creating gateway %s: %w", name, err) //nolint:goerr113
	}

	return gw, nil
}

func (b *VLABBuilder) createConnection(ctx context.Context, spec wiringapi.ConnectionSpec) (*wiringapi.Connection, error) {
	name := spec.GenerateName()

	conn := &wiringapi.Connection{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       wiringapi.KindConnection,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Spec: spec,
	}

	if err := b.data.Add(ctx, conn); err != nil {
		return nil, fmt.Errorf("creating connection %s: %w", name, err) //nolint:goerr113
	}

	return conn, nil
}

func (b *VLABBuilder) createExternal(ctx context.Context, name string, spec vpcapi.ExternalSpec) (*vpcapi.External, error) {
	external := &vpcapi.External{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       "External",
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}

	if err := b.data.Add(ctx, external); err != nil {
		return nil, fmt.Errorf("creating external %s: %w", name, err) //nolint:goerr113
	}

	return external, nil
}

func (b *VLABBuilder) createExternalAttach(ctx context.Context, name string, spec vpcapi.ExternalAttachmentSpec) (*vpcapi.ExternalAttachment, error) {
	externalAttach := &vpcapi.ExternalAttachment{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       "ExternalAttachment",
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}

	if err := b.data.Add(ctx, externalAttach); err != nil {
		return nil, fmt.Errorf("creating external attachment %s: %w", name, err) //nolint:goerr113
	}

	return externalAttach, nil
}
