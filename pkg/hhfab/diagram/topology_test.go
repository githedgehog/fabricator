// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
)

// TestTopologyBGPState covers a fabric connection whose links exercise every keying rule
// at once: numbered (keyed by peer IP) vs unnumbered (keyed by local port), and a
// breakout-capable port named without a subport (E1/3, which the agent resolves to E1/3/1)
// vs one naming a subport (E1/4/2, already agent-side). The topology has to report all of
// them without knowing which rule applies to which link. Two externals on one connection
// share the port, each in its own VRF, and still have to keep their own state. Mesh and
// gateway links are looked up the same way, and a gateway link without IPs is not hydrated
// rather than unnumbered.
func TestTopologyBGPState(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		wiringapi.AddToScheme, fabapi.AddToScheme, agentapi.AddToScheme,
		vpcapi.AddToScheme, dhcpapi.AddToScheme, gwapi.AddToScheme,
	} {
		require.NoError(t, add(scheme))
	}

	fabricator := &fabapi.Fabricator{
		Name:      fabapi.FabName,
		Namespace: fabapi.FabNamespace,
		Spec:      fabapi.FabricatorSpec{Config: fab.DefaultConfig},
	}
	fabricator.Default()

	port := func(p, ip string) wiringapi.ConnFabricLinkSwitch {
		return wiringapi.ConnFabricLinkSwitch{
			Port: p,
			IP:   ip,
		}
	}
	profile := wiringapi.SwitchProfileSpec{
		Ports: map[string]wiringapi.SwitchProfilePort{
			"E1/1": {NOSName: "Ethernet1", Profile: "fixed"},
			"E1/2": {NOSName: "Ethernet2", Profile: "fixed"},
			"E1/3": {BaseNOSName: "Ethernet3", Profile: "breakout"},
			"E1/4": {BaseNOSName: "Ethernet4", Profile: "breakout"},
			"E1/5": {NOSName: "Ethernet5", Profile: "fixed"},
			"E1/6": {NOSName: "Ethernet6", Profile: "fixed"},
			"E1/7": {NOSName: "Ethernet7", Profile: "fixed"},
		},
		PortProfiles: map[string]wiringapi.SwitchProfilePortProfile{
			"fixed": {Speed: &wiringapi.SwitchProfilePortProfileSpeed{
				Default: "100G", Supported: []string{"100G"},
			}},
			"breakout": {Breakout: &wiringapi.SwitchProfilePortProfileBreakout{
				Default: "4x100G",
				Supported: map[string]wiringapi.SwitchProfilePortProfileBreakoutMode{
					"4x100G": {Offsets: []string{"0", "1", "2", "3"}},
				},
			}},
		},
	}

	// an agent reports an unnumbered session under the local port and a numbered one
	// under the peer IP, per VRF
	agent := func(name string, neighbors map[string]map[string]string) *agentapi.Agent {
		state := map[string]map[string]agentapi.SwitchStateBGPNeighbor{}
		for vrf, vrfNeighbors := range neighbors {
			state[vrf] = map[string]agentapi.SwitchStateBGPNeighbor{}
			for key, sessionState := range vrfNeighbors {
				state[vrf][key] = agentapi.SwitchStateBGPNeighbor{
					SessionState: agentapi.BGPNeighborSessionState(sessionState),
				}
			}
		}

		return &agentapi.Agent{
			Name:      name,
			Namespace: kmetav1.NamespaceDefault,
			Spec:      agentapi.AgentSpec{SwitchProfile: &profile},
			Status: agentapi.AgentStatus{State: agentapi.SwitchState{
				BGPNeighbors: state,
			}},
		}
	}

	conn := &wiringapi.Connection{
		Name:      "spine-1--fabric--leaf-1",
		Namespace: kmetav1.NamespaceDefault,
		Spec: wiringapi.ConnectionSpec{Fabric: &wiringapi.ConnFabric{Links: []wiringapi.FabricLink{
			{Spine: port("spine-1/E1/1", "172.30.128.0/31"), Leaf: port("leaf-1/E1/1", "172.30.128.1/31")},
			{Spine: port("spine-1/E1/2", ""), Leaf: port("leaf-1/E1/2", "")},
			{Spine: port("spine-1/E1/3", ""), Leaf: port("leaf-1/E1/3", "")},
			{Spine: port("spine-1/E1/4/2", "172.30.128.2/31"), Leaf: port("leaf-1/E1/4/2", "172.30.128.3/31")},
		}}},
	}
	conn.Default() // sets the switch labels apiutil filters connections on

	meshConn := &wiringapi.Connection{
		Name:      "leaf-1--mesh--leaf-2",
		Namespace: kmetav1.NamespaceDefault,
		Spec: wiringapi.ConnectionSpec{Mesh: &wiringapi.ConnMesh{Links: []wiringapi.MeshLink{
			{Leaf1: port("leaf-1/E1/6", ""), Leaf2: port("leaf-2/E1/6", "")},
		}}},
	}
	meshConn.Default()

	gwPort := func(p, ip string) wiringapi.ConnGatewayLinkGateway {
		return wiringapi.ConnGatewayLinkGateway{Port: p, IP: ip}
	}
	gwConn := &wiringapi.Connection{
		Name:      "spine-1--gateway--gw-1",
		Namespace: kmetav1.NamespaceDefault,
		Spec: wiringapi.ConnectionSpec{Gateway: &wiringapi.ConnGateway{Links: []wiringapi.GatewayLink{
			{Switch: port("spine-1/E1/7", "172.30.128.4/31"), Gateway: gwPort("gw-1/enp2s1", "172.30.128.5/31")},
			{Switch: port("spine-1/E1/5", ""), Gateway: gwPort("gw-1/enp2s2", "")},
		}}},
	}
	gwConn.Default()

	extConn := &wiringapi.Connection{
		Name:      "leaf-1--external--ext",
		Namespace: kmetav1.NamespaceDefault,
		Spec: wiringapi.ConnectionSpec{External: &wiringapi.ConnExternal{
			Link: wiringapi.ConnExternalLink{Switch: wiringapi.BasePortName{Port: "leaf-1/E1/5"}},
		}},
	}
	extConn.Default()

	objs := make([]kclient.Object, 0, 4)
	for _, ext := range []struct{ name, switchIP, neighborIP string }{
		{"ext-a", "100.1.0.1/31", "100.1.0.0"},
		{"ext-b", "", ""},
	} {
		external := &vpcapi.External{Name: ext.name, Namespace: kmetav1.NamespaceDefault}
		external.Default()
		attach := &vpcapi.ExternalAttachment{
			Name:      extConn.Name + "--" + ext.name,
			Namespace: kmetav1.NamespaceDefault,
			Spec: vpcapi.ExternalAttachmentSpec{
				External:   ext.name,
				Connection: extConn.Name,
				Switch:     vpcapi.ExternalAttachmentSwitch{IP: ext.switchIP},
				Neighbor:   vpcapi.ExternalAttachmentNeighbor{ASN: 64102, IP: ext.neighborIP},
			},
		}
		attach.Default()
		objs = append(objs, external, attach)
	}

	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		fabricator,
		&wiringapi.SwitchProfile{Name: "test-profile", Namespace: kmetav1.NamespaceDefault, Spec: profile},
		&wiringapi.Switch{Name: "spine-1", Namespace: kmetav1.NamespaceDefault, Spec: wiringapi.SwitchSpec{
			Role: wiringapi.SwitchRoleSpine, Profile: "test-profile", ProtocolIP: "172.30.8.0/32",
		}},
		&wiringapi.Switch{Name: "leaf-1", Namespace: kmetav1.NamespaceDefault, Spec: wiringapi.SwitchSpec{
			Role: wiringapi.SwitchRoleServerLeaf, Profile: "test-profile", ProtocolIP: "172.30.8.1/32",
		}},
		&wiringapi.Switch{Name: "leaf-2", Namespace: kmetav1.NamespaceDefault, Spec: wiringapi.SwitchSpec{
			Role: wiringapi.SwitchRoleServerLeaf, Profile: "test-profile", ProtocolIP: "172.30.8.2/32",
		}},
		conn,
		meshConn,
		gwConn,
		extConn,
		agent("spine-1", map[string]map[string]string{"default": {
			"172.30.128.1": "established", "E1/2": "established",
			"E1/3/1": "established", "172.30.128.3": "established",
			"172.30.128.5": "established",
		}}),
		agent("leaf-1", map[string]map[string]string{
			"default": {
				"172.30.128.0": "established", "E1/2": "idle",
				"E1/3/1": "idle", "172.30.128.2": "idle",
				"E1/6": "established",
			},
			"VrfEext-a": {"100.1.0.0": "established"},
			"VrfEext-b": {"E1/5": "idle"},
		}),
		agent("leaf-2", nil),
	).WithObjects(objs...).Build()

	// a switch without an agent fails the call for all switches, so the second run goes
	// through the per-switch fallback and must report the same state
	for _, noAgent := range []bool{false, true} {
		if noAgent {
			require.NoError(t, kube.Create(ctx, &wiringapi.Switch{Name: "leaf-3", Namespace: kmetav1.NamespaceDefault, Spec: wiringapi.SwitchSpec{
				Role: wiringapi.SwitchRoleServerLeaf, Profile: "test-profile", ProtocolIP: "172.30.8.3/32",
			}}))
		}

		topo, err := GetTopologyFor(ctx, kube)
		require.NoError(t, err)

		byPort := map[string]Link{}
		byExternal := map[string]Link{}
		for _, link := range topo.Links {
			if link.Type == EdgeTypeExternal {
				byExternal[link.Target] = link
			} else {
				byPort[link.Properties[PropSourcePort]] = link
			}
		}
		require.Contains(t, byPort, "spine-1/E1/1")
		require.Contains(t, byPort, "spine-1/E1/2")
		require.Contains(t, byPort, "spine-1/E1/3")
		require.Contains(t, byPort, "spine-1/E1/4/2")

		numbered := byPort["spine-1/E1/1"]
		require.Equal(t, "established", numbered.Properties[PropBGPState])
		require.Empty(t, numbered.Properties[PropUnnumbered])
		require.Equal(t, "172.30.128.0/31", numbered.Properties[PropSrcLinkIP])

		unnumbered := byPort["spine-1/E1/2"]
		require.Equal(t, "established", unnumbered.Properties[PropBGPState])
		require.Equal(t, "true", unnumbered.Properties[PropUnnumbered])
		require.Empty(t, unnumbered.Properties[PropSrcLinkIP])

		breakout := byPort["spine-1/E1/3"]
		require.Equal(t, "established", breakout.Properties[PropBGPState])
		require.Equal(t, "true", breakout.Properties[PropUnnumbered])
		require.Empty(t, breakout.Properties[PropSrcLinkIP])

		subport := byPort["spine-1/E1/4/2"]
		require.Equal(t, "established", subport.Properties[PropBGPState])
		require.Empty(t, subport.Properties[PropUnnumbered])
		require.Equal(t, "172.30.128.2/31", subport.Properties[PropSrcLinkIP])

		mesh := byPort["leaf-1/E1/6"]
		require.Equal(t, "established", mesh.Properties[PropBGPState])
		require.Equal(t, "true", mesh.Properties[PropUnnumbered])

		gateway := byPort["spine-1/E1/7"]
		require.Equal(t, "established", gateway.Properties[PropBGPState])
		require.Empty(t, gateway.Properties[PropUnnumbered])
		require.Equal(t, "172.30.128.4/31", gateway.Properties[PropSrcLinkIP])

		require.Contains(t, byPort, "spine-1/E1/5")
		require.Empty(t, byPort["spine-1/E1/5"].Properties[PropUnnumbered])

		require.Equal(t, "established", byExternal["ext-a"].Properties[PropBGPState])
		require.Equal(t, "idle", byExternal["ext-b"].Properties[PropBGPState])
	}
}
