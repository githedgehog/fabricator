// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type VLABBuilder struct {
	SpinesCount       uint8  // number of spines to generate
	FabricLinksCount  uint8  // number of links for each spine <> leaf pair
	MCLAGLeafsCount   uint8  // number of MCLAG server-leafs to generate
	ESLAGLeafGroups   string // eslag leaf groups - comma separated list of number of ESLAG switches in each group, should be 2-4 per group, e.g. 2,4,2 for 3 groups with 2, 4 and 2 switches
	OrphanLeafsCount  uint8  // number of non-MCLAG server-leafs to generate
	MCLAGSessionLinks uint8  // number of MCLAG session links to generate
	MCLAGPeerLinks    uint8  // number of MCLAG peer links to generate
	VPCLoopbacks      uint8  // number of VPC loopbacks to generate per leaf switch
	MCLAGServers      uint8  // number of MCLAG servers to generate for MCLAG switches
	ESLAGServers      uint8  // number of ESLAG servers to generate for ESLAG switches
	UnbundledServers  uint8  // number of unbundled servers to generate for switches (only for one of the first switch in the redundancy group or orphan switch)
	BundledServers    uint8  // number of bundled servers to generate for switches (only for one of the second switch in the redundancy group or orphan switch)
	NoSwitches        bool   // do not generate any switches

	data         *apiutil.Loader
	ifaceTracker map[string]uint8 // next available interface ID for each switch
	switchID     uint             // switch ID counter
}

func (b *VLABBuilder) Build(ctx context.Context, l *apiutil.Loader, fabricMode meta.FabricMode) error {
	if l == nil {
		return errors.Errorf("loader is nil")
	}
	b.data = l

	if fabricMode == meta.FabricModeSpineLeaf {
		if !b.NoSwitches {
			if b.MCLAGLeafsCount == 0 && b.OrphanLeafsCount == 0 && b.ESLAGLeafGroups == "" && b.SpinesCount == 0 && b.FabricLinksCount == 0 {
				b.MCLAGLeafsCount = 2
				b.ESLAGLeafGroups = "2"
				b.OrphanLeafsCount = 1
				b.MCLAGServers = 2
				b.ESLAGServers = 2
				b.UnbundledServers = 1
				b.BundledServers = 1
				b.SpinesCount = 2
				b.FabricLinksCount = 2
			}
		}
	} else if fabricMode == meta.FabricModeCollapsedCore {
		if b.SpinesCount > 0 {
			return errors.Errorf("spines not supported for collapsed core fabric mode")
		}
		if b.FabricLinksCount > 0 {
			return errors.Errorf("fabric links not supported for collapsed core fabric mode")
		}

		if !b.NoSwitches && b.MCLAGLeafsCount == 0 {
			b.MCLAGLeafsCount = 2
		}
		if b.MCLAGLeafsCount > 2 {
			return errors.Errorf("MCLAG leafs count must be 2 for collapsed core fabric mode")
		}
		if b.OrphanLeafsCount > 0 {
			return errors.Errorf("orphan leafs not supported for collapsed core fabric mode")
		}

		if b.ESLAGLeafGroups != "" {
			return errors.Errorf("ESLAG not supported for collapsed core fabric mode")
		}
	} else {
		return errors.Errorf("unsupported fabric mode %s", fabricMode)
	}

	if b.MCLAGSessionLinks == 0 && b.MCLAGLeafsCount > 0 {
		b.MCLAGSessionLinks = 2
	}
	if b.MCLAGPeerLinks == 0 && b.MCLAGLeafsCount > 0 {
		b.MCLAGPeerLinks = 2
	}
	if b.VPCLoopbacks == 0 {
		b.VPCLoopbacks = 2
	}

	totalESLAGLeafs := uint8(0)
	eslagLeafGroups := []uint8{}

	if b.ESLAGLeafGroups != "" {
		parts := strings.Split(b.ESLAGLeafGroups, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			leafs, err := strconv.ParseUint(part, 10, 8)
			if err != nil {
				return errors.Errorf("invalid ESLAG leaf group %s", part)
			}

			if leafs < 2 || leafs > 4 {
				return errors.Errorf("ESLAG leaf group must have 2-4 leafs")
			}

			totalESLAGLeafs += uint8(leafs)
			eslagLeafGroups = append(eslagLeafGroups, uint8(leafs))
		}
	}

	if b.MCLAGLeafsCount%2 != 0 {
		return errors.Errorf("MCLAG leafs count must be even")
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGSessionLinks == 0 {
		return errors.Errorf("MCLAG session links count must be greater than 0")
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGPeerLinks == 0 {
		return errors.Errorf("MCLAG peer links count must be greater than 0")
	}

	slog.Info("Building VLAB wiring diagram", "fabricMode", fabricMode)
	if fabricMode == meta.FabricModeSpineLeaf {
		slog.Info(">>>", "spinesCount", b.SpinesCount, "fabricLinksCount", b.FabricLinksCount)
		slog.Info(">>>", "eslagLeafGroups", b.ESLAGLeafGroups)
	}
	slog.Info(">>>", "mclagLeafsCount", b.MCLAGLeafsCount, "mclagSessionLinks", b.MCLAGSessionLinks, "mclagPeerLinks", b.MCLAGPeerLinks)
	slog.Info(">>>", "orphanLeafsCount", b.OrphanLeafsCount, "vpcLoopbacks", b.VPCLoopbacks)
	slog.Info(">>>", "mclagServers", b.MCLAGServers, "eslagServers", b.ESLAGServers, "unbundledServers", b.UnbundledServers, "bundledServers", b.BundledServers)

	if err := b.data.Add(ctx, &wiringapi.VLANNamespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindVLANNamespace,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: wiringapi.VLANNamespaceSpec{
			Ranges: []meta.VLANRange{
				{From: 1000, To: 2999},
			},
		},
	}); err != nil {
		return errors.Wrapf(err, "error creating VLAN namespace")
	}

	if err := b.data.Add(ctx, &vpcapi.IPv4Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       vpcapi.KindIPv4Namespace,
			APIVersion: vpcapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: vpcapi.IPv4NamespaceSpec{
			Subnets: []string{
				"10.0.0.0/16",
			},
		},
	}); err != nil {
		return errors.Wrapf(err, "error creating IPv4 namespace")
	}

	b.ifaceTracker = map[string]uint8{}

	if _, err := b.createSwitchGroup(ctx, "empty"); err != nil {
		return err
	}

	switchID := uint8(1) // switch ID counter

	leafID := uint8(1)   // leaf ID counter
	serverID := uint8(1) // server ID counter

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
	}

	if b.VPCLoopbacks > 0 {
		switches := &wiringapi.SwitchList{}
		if err := b.data.List(ctx, switches); err != nil {
			return errors.Wrap(err, "error listing switches")
		}

		for _, sw := range switches.Items {
			if !sw.Spec.Role.IsLeaf() {
				continue
			}

			loops := []wiringapi.SwitchToSwitchLink{}
			for i := uint8(0); i < b.VPCLoopbacks; i++ {
				loops = append(loops, wiringapi.SwitchToSwitchLink{
					Switch1: wiringapi.BasePortName{Port: b.nextSwitchPort(sw.Name)},
					Switch2: wiringapi.BasePortName{Port: b.nextSwitchPort(sw.Name)},
				})
			}

			if _, err := b.createConnection(ctx, wiringapi.ConnectionSpec{
				VPCLoopback: &wiringapi.ConnVPCLoopback{
					Links: loops,
				},
			}); err != nil {
				return err
			}
		}
	}

	return nil
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
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindSwitchGroup,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Spec: wiringapi.SwitchGroupSpec{},
	}

	return sg, errors.Wrapf(b.data.Add(ctx, sg), "error creating switch group %s", name)
}

func (b *VLABBuilder) createSwitch(ctx context.Context, name string, spec wiringapi.SwitchSpec) (*wiringapi.Switch, error) { //nolint:unparam
	spec.Profile = meta.SwitchProfileVS
	spec.Boot.MAC = fmt.Sprintf(VLABSwitchMACTmpl, b.switchID)
	b.switchID++

	sw := &wiringapi.Switch{
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindSwitch,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}

	return sw, errors.Wrapf(b.data.Add(ctx, sw), "error creating switch %s", name)
}

func (b *VLABBuilder) createServer(ctx context.Context, name string, spec wiringapi.ServerSpec) (*wiringapi.Server, error) { //nolint:unparam
	server := &wiringapi.Server{
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindServer,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}

	return server, errors.Wrapf(b.data.Add(ctx, server), "error creating server %s", name)
}

func (b *VLABBuilder) createConnection(ctx context.Context, spec wiringapi.ConnectionSpec) (*wiringapi.Connection, error) { //nolint:unparam
	name := spec.GenerateName()

	conn := &wiringapi.Connection{
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindConnection,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Spec: spec,
	}

	return conn, errors.Wrapf(b.data.Add(ctx, conn), "error creating connection %s", name)
}
