// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wiring

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	RACK    = "rack-1"
	CONTROL = "control-1"
)

type Builder struct {
	Defaulted         bool            // true if default should be called on created objects
	Hydrated          bool            // true if wiring diagram should be hydrated
	FabricMode        meta.FabricMode // fabric mode
	ChainControlLink  bool            // true if not all switches attached directly to control node
	ControlLinksCount uint8           // number of control links to generate
	SpinesCount       uint8           // number of spines to generate
	FabricLinksCount  uint8           // number of links for each spine <> leaf pair
	MCLAGLeafsCount   uint8           // number of MCLAG server-leafs to generate
	ESLAGLeafGroups   string          // eslag leaf groups - comma separated list of number of ESLAG switches in each group, should be 2-4 per group, e.g. 2,4,2 for 3 groups with 2, 4 and 2 switches
	OrphanLeafsCount  uint8           // number of non-MCLAG server-leafs to generate
	MCLAGSessionLinks uint8           // number of MCLAG session links to generate
	MCLAGPeerLinks    uint8           // number of MCLAG peer links to generate
	VPCLoopbacks      uint8           // number of VPC loopbacks to generate per leaf switch

	data         *wiring.Data
	ifaceTracker map[string]uint8 // next available interface ID for each switch
}

func (b *Builder) Build() (*wiring.Data, error) {
	if b.FabricMode == meta.FabricModeSpineLeaf {
		if b.ChainControlLink && b.ControlLinksCount == 0 {
			b.ControlLinksCount = 2
		}
		if b.SpinesCount == 0 {
			b.SpinesCount = 2
		}
		if b.FabricLinksCount == 0 {
			b.FabricLinksCount = 2
		}
		if b.MCLAGLeafsCount == 0 && b.OrphanLeafsCount == 0 && b.ESLAGLeafGroups == "" {
			b.MCLAGLeafsCount = 2
			b.ESLAGLeafGroups = "2"
			b.OrphanLeafsCount = 1
		}
	} else if b.FabricMode == meta.FabricModeCollapsedCore {
		if b.ChainControlLink {
			return nil, fmt.Errorf("control link chaining not supported for collapsed core fabric mode")
		}
		if b.SpinesCount > 0 {
			return nil, fmt.Errorf("spines not supported for collapsed core fabric mode")
		}
		if b.FabricLinksCount > 0 {
			return nil, fmt.Errorf("fabric links not supported for collapsed core fabric mode")
		}

		if b.MCLAGLeafsCount == 0 {
			b.MCLAGLeafsCount = 2
		}
		if b.MCLAGLeafsCount > 2 {
			return nil, fmt.Errorf("MCLAG leafs count must be 2 for collapsed core fabric mode")
		}
		if b.OrphanLeafsCount > 0 {
			return nil, fmt.Errorf("orphan leafs not supported for collapsed core fabric mode")
		}

		if b.ESLAGLeafGroups != "" {
			return nil, fmt.Errorf("ESLAG not supported for collapsed core fabric mode")
		}
	} else {
		return nil, fmt.Errorf("unsupported fabric mode %s", b.FabricMode)
	}

	if b.MCLAGSessionLinks == 0 {
		b.MCLAGSessionLinks = 2
	}
	if b.MCLAGPeerLinks == 0 {
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
				return nil, fmt.Errorf("invalid ESLAG leaf group %s", part)
			}

			if leafs < 2 || leafs > 4 {
				return nil, fmt.Errorf("ESLAG leaf group must have 2-4 leafs")
			}

			totalESLAGLeafs += uint8(leafs)
			eslagLeafGroups = append(eslagLeafGroups, uint8(leafs))
		}
	}

	if b.ChainControlLink && b.ControlLinksCount == 0 {
		return nil, fmt.Errorf("control links count must be greater than 0 if chaining control links")
	}
	if b.MCLAGLeafsCount%2 != 0 {
		return nil, fmt.Errorf("MCLAG leafs count must be even")
	}
	if b.MCLAGLeafsCount+b.OrphanLeafsCount+totalESLAGLeafs == 0 {
		return nil, fmt.Errorf("total leafs count must be greater than 0")
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGSessionLinks == 0 {
		return nil, fmt.Errorf("MCLAG session links count must be greater than 0")
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGPeerLinks == 0 {
		return nil, fmt.Errorf("MCLAG peer links count must be greater than 0")
	}

	slog.Info("Building wiring diagram", "fabricMode", b.FabricMode, "chainControlLink", b.ChainControlLink, "controlLinksCount", b.ControlLinksCount)
	if b.FabricMode == meta.FabricModeSpineLeaf {
		slog.Info("                    >>>", "spinesCount", b.SpinesCount, "fabricLinksCount", b.FabricLinksCount)
		slog.Info("                    >>>", "eslagLeafGroups", b.ESLAGLeafGroups)
	}
	slog.Info("                    >>>", "mclagLeafsCount", b.MCLAGLeafsCount, "mclagSessionLinks", b.MCLAGSessionLinks, "mclagPeerLinks", b.MCLAGPeerLinks)
	slog.Info("                    >>>", "orphanLeafsCount", b.OrphanLeafsCount)
	slog.Info("                    >>>", "vpcLoopbacks", b.VPCLoopbacks)

	var err error
	b.data, err = wiring.New()
	if err != nil {
		return nil, err
	}

	if err := b.data.Add(&wiringapi.VLANNamespace{
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
		return nil, errors.Wrapf(err, "error creating VLAN namespace")
	}

	if err := b.data.Add(&vpcapi.IPv4Namespace{
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
		return nil, errors.Wrapf(err, "error creating IPv4 namespace")
	}

	b.ifaceTracker = map[string]uint8{}

	if _, err := b.createRack(RACK, wiringapi.RackSpec{}); err != nil {
		return nil, err
	}

	if _, err := b.createServer(CONTROL, wiringapi.ServerSpec{
		Type:        wiringapi.ServerTypeControl,
		Description: "Control node",
	}); err != nil {
		return nil, err
	}

	switchID := uint8(1) // switch ID counter

	leafID := uint8(1)   // leaf ID counter
	serverID := uint8(1) // server ID counter

	for mclagID := uint8(1); mclagID <= b.MCLAGLeafsCount/2; mclagID++ {
		leaf1Name := fmt.Sprintf("leaf-%02d", leafID)
		leaf2Name := fmt.Sprintf("leaf-%02d", leafID+1)

		sg := fmt.Sprintf("mclag-%d", mclagID)
		if _, err := b.createSwitchGroup(sg); err != nil {
			return nil, err
		}

		if _, err := b.createSwitch(leaf1Name, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d MCLAG %d", switchID, mclagID),
			Groups:      []string{sg},
			Redundancy: wiringapi.SwitchRedundancy{
				Group: sg,
				Type:  meta.RedundancyTypeMCLAG,
			},
		}); err != nil {
			return nil, err
		}
		if _, err := b.createSwitch(leaf2Name, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d MCLAG %d", switchID+1, mclagID),
			Groups:      []string{sg},
			Redundancy: wiringapi.SwitchRedundancy{
				Group: sg,
				Type:  meta.RedundancyTypeMCLAG,
			},
		}); err != nil {
			return nil, err
		}

		if !b.ChainControlLink {
			if _, err := b.createManagementConnection(leaf1Name); err != nil {
				return nil, err
			}
			if _, err := b.createManagementConnection(leaf2Name); err != nil {
				return nil, err
			}
		} else if leafID < b.ControlLinksCount {
			if _, err := b.createControlConnection(leaf1Name); err != nil {
				return nil, err
			}
			if _, err := b.createControlConnection(leaf2Name); err != nil {
				return nil, err
			}
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

		if _, err := b.createConnection(wiringapi.ConnectionSpec{
			MCLAGDomain: &wiringapi.ConnMCLAGDomain{
				SessionLinks: sessionLinks,
				PeerLinks:    peerLinks,
			},
		}); err != nil {
			return nil, err
		}

		// 2 x mclag conn servers
		for i := 0; i < 2; i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d MCLAG %s %s", serverID, leaf1Name, leaf2Name),
			}); err != nil {
				return nil, err
			}

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
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
				return nil, err
			}

			serverID++
		}

		// unbundled conn server to leaf1
		{
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Unbundled %s", serverID, leaf1Name),
			}); err != nil {
				return nil, err
			}

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
				Unbundled: &wiringapi.ConnUnbundled{
					Link: wiringapi.ServerToSwitchLink{
						Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
						Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leaf1Name)},
					},
				},
			}); err != nil {
				return nil, err
			}

			serverID++
		}

		// bundled conn server to leaf2
		{
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Bundled %s", serverID, leaf2Name),
			}); err != nil {
				return nil, err
			}

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
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
				return nil, err
			}

			serverID++
		}
	}

	for eslagID := uint8(0); eslagID < uint8(len(eslagLeafGroups)); eslagID++ {
		sg := fmt.Sprintf("eslag-%d", eslagID+1)
		if _, err := b.createSwitchGroup(sg); err != nil {
			return nil, err
		}

		leafs := eslagLeafGroups[eslagID]
		leafNames := []string{}
		for eslagLeafID := uint8(0); eslagLeafID < leafs; eslagLeafID++ {
			leafName := fmt.Sprintf("leaf-%02d", leafID+eslagLeafID)
			leafNames = append(leafNames, leafName)

			if _, err := b.createSwitch(leafName, wiringapi.SwitchSpec{
				Role:        wiringapi.SwitchRoleServerLeaf,
				Description: fmt.Sprintf("VS-%02d ESLAG %d", switchID+eslagLeafID, eslagID+1),
				Groups:      []string{sg},
				Redundancy: wiringapi.SwitchRedundancy{
					Group: sg,
					Type:  meta.RedundancyTypeESLAG,
				},
			}); err != nil {
				return nil, err
			}

			if !b.ChainControlLink {
				if _, err := b.createManagementConnection(leafName); err != nil {
					return nil, err
				}
			} else if leafID < b.ControlLinksCount {
				if _, err := b.createControlConnection(leafName); err != nil {
					return nil, err
				}
			}
		}

		switchID += leafs
		leafID += leafs

		// 2 x eslag conn servers
		for i := 0; i < 2; i++ {
			serverName := fmt.Sprintf("server-%02d", serverID)

			leafNamesStr := strings.Join(leafNames, " ")

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d ESLAG %s", serverID, leafNamesStr),
			}); err != nil {
				return nil, err
			}

			links := []wiringapi.ServerToSwitchLink{}
			for _, leafName := range leafNames {
				links = append(links, wiringapi.ServerToSwitchLink{
					Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
					Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafName)},
				})
			}
			if _, err := b.createConnection(wiringapi.ConnectionSpec{
				ESLAG: &wiringapi.ConnESLAG{
					Links: links,
				},
			}); err != nil {
				return nil, err
			}

			serverID++
		}

		// unbundled conn server to leaf1
		{
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Unbundled %s", serverID, leafNames[0]),
			}); err != nil {
				return nil, err
			}

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
				Unbundled: &wiringapi.ConnUnbundled{
					Link: wiringapi.ServerToSwitchLink{
						Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
						Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafNames[0])},
					},
				},
			}); err != nil {
				return nil, err
			}

			serverID++
		}

		// bundled conn server to leaf2
		if leafs > 1 {
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Bundled %s", serverID, leafNames[1]),
			}); err != nil {
				return nil, err
			}

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
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
				return nil, err
			}

			serverID++
		}
	}

	for idx := uint8(1); idx <= b.OrphanLeafsCount; idx++ {
		leafName := fmt.Sprintf("leaf-%02d", leafID)

		if _, err := b.createSwitch(leafName, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d", switchID),
		}); err != nil {
			return nil, err
		}

		if !b.ChainControlLink {
			if _, err := b.createManagementConnection(leafName); err != nil {
				return nil, err
			}
		} else if leafID < b.ControlLinksCount {
			if _, err := b.createControlConnection(leafName); err != nil {
				return nil, err
			}
		}

		switchID++
		leafID++

		// unbundled conn server
		{
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Unbundled %s", serverID, leafName),
			}); err != nil {
				return nil, err
			}

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
				Unbundled: &wiringapi.ConnUnbundled{
					Link: wiringapi.ServerToSwitchLink{
						Server: wiringapi.BasePortName{Port: b.nextServerPort(serverName)},
						Switch: wiringapi.BasePortName{Port: b.nextSwitchPort(leafName)},
					},
				},
			}); err != nil {
				return nil, err
			}

			serverID++
		}

		// bundled conn server
		{
			serverName := fmt.Sprintf("server-%02d", serverID)

			if _, err := b.createServer(serverName, wiringapi.ServerSpec{
				Description: fmt.Sprintf("S-%02d Bundled %s", serverID, leafName),
			}); err != nil {
				return nil, err
			}

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
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
				return nil, err
			}

			serverID++
		}
	}

	for spineID := uint8(1); spineID <= b.SpinesCount; spineID++ {
		spineName := fmt.Sprintf("spine-%02d", spineID)

		if _, err := b.createSwitch(spineName, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleSpine,
			Description: fmt.Sprintf("VS-%02d", switchID),
		}); err != nil {
			return nil, err
		}

		if !b.ChainControlLink {
			if _, err := b.createManagementConnection(spineName); err != nil {
				return nil, err
			}
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

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
				Fabric: &wiringapi.ConnFabric{
					Links: links,
				},
			}); err != nil {
				return nil, err
			}
		}
	}

	if b.VPCLoopbacks > 0 {
		for _, sw := range b.data.Switch.All() {
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

			if _, err := b.createConnection(wiringapi.ConnectionSpec{
				VPCLoopback: &wiringapi.ConnVPCLoopback{
					Links: loops,
				},
			}); err != nil {
				return nil, err
			}
		}
	}

	if b.Hydrated {
		// TODO extract to a single place
		if err := Hydrate(b.data, &HydrateConfig{
			Subnet:       "172.30.0.0/16",
			SpineASN:     65100,
			LeafASNStart: 65101,
		}); err != nil {
			return nil, err
		}
	}

	return b.data, nil
}

func (b *Builder) nextSwitchPort(switchName string) string {
	ifaceID := b.ifaceTracker[switchName]
	portName := fmt.Sprintf("%s/Ethernet%d", switchName, ifaceID)
	ifaceID++
	b.ifaceTracker[switchName] = ifaceID

	return portName
}

func (b *Builder) nextControlPort(serverName string) string {
	ifaceID := b.ifaceTracker[serverName]
	portName := fmt.Sprintf("%s/enp2s%d", serverName, ifaceID+1) // value for VLAB
	ifaceID++
	b.ifaceTracker[serverName] = ifaceID

	return portName
}

func (b *Builder) nextServerPort(serverName string) string {
	ifaceID := b.ifaceTracker[serverName]
	portName := fmt.Sprintf("%s/enp2s%d", serverName, ifaceID+1) // value for VLAB
	ifaceID++
	b.ifaceTracker[serverName] = ifaceID

	return portName
}

func (b *Builder) createRack(name string, spec wiringapi.RackSpec) (*wiringapi.Rack, error) {
	rack := &wiringapi.Rack{
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindRack,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Spec: spec,
	}

	return rack, errors.Wrapf(b.data.Add(rack), "error creating rack %s", name)
}

func (b *Builder) createSwitchGroup(name string) (*wiringapi.SwitchGroup, error) {
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

	return sg, errors.Wrapf(b.data.Add(sg), "error creating switch group %s", name)
}

func (b *Builder) createSwitch(name string, spec wiringapi.SwitchSpec) (*wiringapi.Switch, error) {
	spec.Profile = "vs" // TODO temp hack

	sw := &wiringapi.Switch{
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindSwitch,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				wiringapi.LabelRack: RACK,
			},
		},
		Spec: spec,
	}

	if b.Defaulted {
		sw.Default()
	}

	return sw, errors.Wrapf(b.data.Add(sw), "error creating switch %s", name)
}

func (b *Builder) createServer(name string, spec wiringapi.ServerSpec) (*wiringapi.Server, error) {
	server := &wiringapi.Server{
		TypeMeta: metav1.TypeMeta{
			Kind:       wiringapi.KindServer,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				wiringapi.LabelRack: RACK,
			},
		},
		Spec: spec,
	}

	if b.Defaulted {
		server.Default()
	}

	return server, errors.Wrapf(b.data.Add(server), "error creating server %s", name)
}

func (b *Builder) createConnection(spec wiringapi.ConnectionSpec) (*wiringapi.Connection, error) {
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

	if b.Defaulted {
		conn.Default()
	}

	return conn, errors.Wrapf(b.data.Add(conn), "error creating connection %s", name)
}

func (b *Builder) createManagementConnection(switchName string) (*wiringapi.Connection, error) {
	return b.createConnection(wiringapi.ConnectionSpec{
		Management: &wiringapi.ConnMgmt{
			Link: wiringapi.ConnMgmtLink{
				Server: wiringapi.ConnMgmtLinkServer{
					BasePortName: wiringapi.BasePortName{Port: b.nextControlPort(CONTROL)},
				},
				Switch: wiringapi.ConnMgmtLinkSwitch{
					BasePortName: wiringapi.BasePortName{Port: fmt.Sprintf("%s/Management0", switchName)},
					ONIEPortName: "eth0",
				},
			},
		},
	})
}

func (b *Builder) createControlConnection(switchName string) (*wiringapi.Connection, error) {
	port := b.nextSwitchPort(switchName)
	oniePortName := fmt.Sprintf("eth%d", b.ifaceTracker[switchName])

	return b.createConnection(wiringapi.ConnectionSpec{
		Management: &wiringapi.ConnMgmt{
			Link: wiringapi.ConnMgmtLink{
				Server: wiringapi.ConnMgmtLinkServer{
					BasePortName: wiringapi.BasePortName{Port: b.nextControlPort(CONTROL)},
				},
				Switch: wiringapi.ConnMgmtLinkSwitch{
					BasePortName: wiringapi.BasePortName{Port: port},
					ONIEPortName: oniePortName,
				},
			},
		},
	})
}
