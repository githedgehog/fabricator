package wiring

import (
	"fmt"

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

type SpineLeafBuilder struct {
	Defaulted bool // true if default should be called on created objects
	Hydrated  bool // true if wiring diagram should be hydrated
	// VLAB              bool  // true if VLAB mode is enabled
	ChainControlLink  bool  // true if not all switches attached directly to control node
	ControlLinksCount uint8 // number of control links to generate
	SpinesCount       uint8 // number of spines to generate
	FabricLinksCount  uint8 // number of links for each spine <> leaf pair
	MCLAGLeafsCount   uint8 // number of MCLAG server-leafs to generate
	OrphanLeafsCount  uint8 // number of non-MCLAG server-leafs to generate
	MCLAGSessionLinks uint8 // number of MCLAG session links to generate
	MCLAGPeerLinks    uint8 // number of MCLAG peer links to generate

	data         *wiring.Data
	ifaceTracker map[string]uint8 // next available interface ID for each switch
}

func (b *SpineLeafBuilder) Build() (*wiring.Data, error) {
	if b.SpinesCount == 0 {
		return nil, fmt.Errorf("spines count must be greater than 0")
	}
	if b.MCLAGLeafsCount%2 != 0 {
		return nil, fmt.Errorf("MCLAG leafs count must be even")
	}
	if b.FabricLinksCount == 0 {
		return nil, fmt.Errorf("fabric links count must be greater than 0")
	}
	if b.MCLAGLeafsCount+b.OrphanLeafsCount == 0 {
		return nil, fmt.Errorf("total leafs count must be greater than 0")
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGSessionLinks == 0 {
		return nil, fmt.Errorf("MCLAG session links count must be greater than 0")
	}
	if b.MCLAGLeafsCount > 0 && b.MCLAGPeerLinks == 0 {
		return nil, fmt.Errorf("MCLAG peer links count must be greater than 0")
	}

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
				"10.0.0.0/8",
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

		if _, err := b.createSwitch(leaf1Name, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d MCLAG %d", switchID, mclagID),
		}); err != nil {
			return nil, err
		}
		if _, err := b.createSwitch(leaf2Name, wiringapi.SwitchSpec{
			Role:        wiringapi.SwitchRoleServerLeaf,
			Description: fmt.Sprintf("VS-%02d MCLAG %d", switchID+1, mclagID),
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

		for leafID := uint8(1); leafID <= b.MCLAGLeafsCount+b.OrphanLeafsCount; leafID++ {
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

func (b *SpineLeafBuilder) nextSwitchPort(switchName string) string {
	ifaceID := b.ifaceTracker[switchName]
	portName := fmt.Sprintf("%s/Ethernet%d", switchName, ifaceID)
	ifaceID++
	b.ifaceTracker[switchName] = ifaceID

	return portName
}

func (b *SpineLeafBuilder) nextControlPort(serverName string) string {
	ifaceID := b.ifaceTracker[serverName]
	portName := fmt.Sprintf("%s/enp2s%d", serverName, ifaceID+1) // value for VLAB
	ifaceID++
	b.ifaceTracker[serverName] = ifaceID

	return portName
}

func (b *SpineLeafBuilder) nextServerPort(serverName string) string {
	ifaceID := b.ifaceTracker[serverName]
	portName := fmt.Sprintf("%s/enp2s%d", serverName, ifaceID+1) // value for VLAB
	ifaceID++
	b.ifaceTracker[serverName] = ifaceID

	return portName
}

func (b *SpineLeafBuilder) createRack(name string, spec wiringapi.RackSpec) (*wiringapi.Rack, error) {
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

func (b *SpineLeafBuilder) createSwitch(name string, spec wiringapi.SwitchSpec) (*wiringapi.Switch, error) {
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

func (b *SpineLeafBuilder) createServer(name string, spec wiringapi.ServerSpec) (*wiringapi.Server, error) {
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

func (b *SpineLeafBuilder) createConnection(spec wiringapi.ConnectionSpec) (*wiringapi.Connection, error) {
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

func (b *SpineLeafBuilder) createManagementConnection(switchName string) (*wiringapi.Connection, error) {
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

func (b *SpineLeafBuilder) createControlConnection(switchName string) (*wiringapi.Connection, error) {
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
