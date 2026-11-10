// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"context"
	"slices"
	"strings"
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

	objs := make([]kclient.Object, 0, 24)
	extConns := map[string]*wiringapi.Connection{}
	for _, c := range []struct{ name, swPort, fabric string }{
		{"leaf-1--external--ext", "leaf-1/E1/5", ""},
		{"leaf-2--external--ext", "leaf-2/E1/5", ""},
		{"extra-leaf-1--external--ext-1", "extra-leaf-1/E1/5", "extra"},
		{"extra-leaf-1--external--ext-2", "extra-leaf-1/E1/7", "extra"},
	} {
		extConn := &wiringapi.Connection{
			Name:      c.name,
			Namespace: kmetav1.NamespaceDefault,
			Spec: wiringapi.ConnectionSpec{
				Topology: wiringapi.ConnectionTopology{Fabric: c.fabric},
				External: &wiringapi.ConnExternal{
					Link: wiringapi.ConnExternalLink{Switch: wiringapi.BasePortName{Port: c.swPort}},
				},
			},
		}
		extConn.Default()
		extConns[c.swPort] = extConn
		objs = append(objs, extConn)
	}

	// ext-b and ext-c have no neighbor ASN, which is optional, so they must not pair up. The ic-*
	// externals are the two ends of links between fabrics, numbered and unnumbered, and the
	// unnumbered ones have two links that must be drawn as one. The ic-shared-* externals reuse
	// ASNs: the VLAN tells the first link apart, the others can't be told apart and stay unpaired
	// without losing the first. The ic-reused-* links reuse IPs and are told apart by the VLAN.
	// The ic-leaf-* externals have no local ASN, so each side presents its border leaf's.
	externals := map[string]bool{}
	for _, ext := range []struct {
		name, fabric, swPort, switchIP, neighborIP string
		localASN, neighborASN                      uint32
		vlan                                       uint16
	}{
		{"ext-a", "", "leaf-1/E1/5", "100.1.0.1/31", "100.1.0.0", 0, 64102, 0},
		{"ext-b", "", "leaf-1/E1/5", "", "", 0, 0, 0},
		{"ext-c", "extra", "extra-leaf-1/E1/5", "", "", 0, 0, 0},
		{"ic-num-main", "", "leaf-1/E1/5", "100.64.0.1/30", "100.64.0.2", 64910, 64911, 0},
		{"ic-num-extra", "extra", "extra-leaf-1/E1/5", "100.64.0.2/30", "100.64.0.1", 64911, 64910, 0},
		{"ic-unnum-main", "", "leaf-1/E1/5", "", "", 64920, 64921, 0},
		{"ic-unnum-main", "", "leaf-2/E1/5", "", "", 64920, 64921, 0},
		{"ic-unnum-extra", "extra", "extra-leaf-1/E1/5", "", "", 64921, 64920, 0},
		{"ic-unnum-extra", "extra", "extra-leaf-1/E1/7", "", "", 64921, 64920, 0},
		{"ic-shared-main-1", "", "leaf-1/E1/5", "", "", 64930, 64931, 10},
		{"ic-shared-main-1", "", "leaf-2/E1/5", "", "", 64930, 64931, 20},
		{"ic-shared-extra-1", "extra", "extra-leaf-1/E1/5", "", "", 64931, 64930, 10},
		{"ic-shared-main-2", "", "leaf-1/E1/5", "", "", 64930, 64931, 20},
		{"ic-shared-extra-2", "extra", "extra-leaf-1/E1/5", "", "", 64931, 64930, 20},
		{"ic-shared-main-3", "", "leaf-2/E1/5", "", "", 64930, 64931, 20},
		{"ic-shared-extra-3", "extra", "extra-leaf-1/E1/7", "", "", 64931, 64930, 20},
		{"ic-reused-main-1", "", "leaf-1/E1/5", "100.64.1.1/30", "100.64.1.2", 0, 0, 10},
		{"ic-reused-extra-1", "extra", "extra-leaf-1/E1/5", "100.64.1.2/30", "100.64.1.1", 0, 0, 10},
		{"ic-reused-main-2", "", "leaf-1/E1/5", "100.64.1.1/30", "100.64.1.2", 0, 0, 20},
		{"ic-reused-extra-2", "extra", "extra-leaf-1/E1/5", "100.64.1.2/30", "100.64.1.1", 0, 0, 20},
		{"ic-leaf-main", "", "leaf-2/E1/5", "", "", 0, 65201, 30},
		{"ic-leaf-extra", "extra", "extra-leaf-1/E1/7", "", "", 0, 65102, 30},
	} {
		if !externals[ext.name] {
			externals[ext.name] = true
			external := &vpcapi.External{Name: ext.name, Namespace: kmetav1.NamespaceDefault, Spec: vpcapi.ExternalSpec{
				Topology: vpcapi.ExternalTopology{Fabric: ext.fabric},
				LocalASN: ext.localASN,
			}}
			external.Default()
			objs = append(objs, external)
		}
		extConn := extConns[ext.swPort]
		attach := &vpcapi.ExternalAttachment{
			Name:      extConn.Name + "--" + ext.name,
			Namespace: kmetav1.NamespaceDefault,
			Spec: vpcapi.ExternalAttachmentSpec{
				Topology:   vpcapi.ExternalAttachmentTopology{Fabric: ext.fabric},
				External:   ext.name,
				Connection: extConn.Name,
				Switch:     vpcapi.ExternalAttachmentSwitch{IP: ext.switchIP, VLAN: ext.vlan},
				Neighbor:   vpcapi.ExternalAttachmentNeighbor{ASN: ext.neighborASN, IP: ext.neighborIP},
			},
		}
		attach.Default()
		objs = append(objs, attach)
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
			Role: wiringapi.SwitchRoleServerLeaf, Profile: "test-profile", ProtocolIP: "172.30.8.2/32", ASN: 65102,
		}},
		&wiringapi.Switch{Name: "extra-leaf-1", Namespace: kmetav1.NamespaceDefault, Spec: wiringapi.SwitchSpec{
			Role: wiringapi.SwitchRoleServerLeaf, Profile: "test-profile", ProtocolIP: "172.30.9.1/32", ASN: 65201,
			Topology: wiringapi.SwitchTopology{Fabric: "extra"},
		}},
		conn,
		meshConn,
		gwConn,
		agent("spine-1", map[string]map[string]string{"default": {
			"172.30.128.1": "established", "E1/2": "idle",
			"E1/3/1": "active", "172.30.128.3": "connect",
			"172.30.128.5": "openSent",
		}}),
		agent("leaf-1", map[string]map[string]string{
			"default": {
				"172.30.128.0": "openConfirm", "E1/2": "openConfirm",
				"E1/3/1": "openConfirm", "172.30.128.2": "openConfirm",
				"E1/6": "established",
			},
			"VrfEext-a": {"100.1.0.0": "established"},
			"VrfEext-b": {"E1/5": "idle"},
		}),
		agent("leaf-2", nil),
		agent("extra-leaf-1", nil),
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

		// links are collected from maps, so their order must not change between runs
		for range 10 {
			again, err := GetTopologyFor(ctx, kube)
			require.NoError(t, err)
			require.Equal(t, topo.Links, again.Links)
		}

		byPort := map[string]Link{}
		byExternal := map[string]Link{}
		interconnects := []string{}
		for _, link := range topo.Links {
			switch link.Type {
			case EdgeTypeExternal:
				byExternal[link.Target] = link
			case EdgeTypeInterconnect:
				pair := []string{link.Source, link.Target}
				slices.Sort(pair)
				interconnects = append(interconnects, strings.Join(pair, "<>"))
			default:
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
		require.Equal(t, "idle", unnumbered.Properties[PropBGPState])
		require.Equal(t, "true", unnumbered.Properties[PropUnnumbered])
		require.Empty(t, unnumbered.Properties[PropSrcLinkIP])

		breakout := byPort["spine-1/E1/3"]
		require.Equal(t, "active", breakout.Properties[PropBGPState])
		require.Equal(t, "true", breakout.Properties[PropUnnumbered])
		require.Empty(t, breakout.Properties[PropSrcLinkIP])

		subport := byPort["spine-1/E1/4/2"]
		require.Equal(t, "connect", subport.Properties[PropBGPState])
		require.Empty(t, subport.Properties[PropUnnumbered])
		require.Equal(t, "172.30.128.2/31", subport.Properties[PropSrcLinkIP])

		mesh := byPort["leaf-1/E1/6"]
		require.Equal(t, "established", mesh.Properties[PropBGPState])
		require.Equal(t, "true", mesh.Properties[PropUnnumbered])

		gateway := byPort["spine-1/E1/7"]
		require.Equal(t, "openSent", gateway.Properties[PropBGPState])
		require.Empty(t, gateway.Properties[PropUnnumbered])
		require.Equal(t, "172.30.128.4/31", gateway.Properties[PropSrcLinkIP])

		require.Contains(t, byPort, "spine-1/E1/5")
		require.Empty(t, byPort["spine-1/E1/5"].Properties[PropUnnumbered])

		require.Equal(t, "established", byExternal["ext-a"].Properties[PropBGPState])
		require.Empty(t, byExternal["ext-a"].Properties[PropUnnumbered])
		require.Equal(t, "idle", byExternal["ext-b"].Properties[PropBGPState])
		require.Equal(t, "true", byExternal["ext-b"].Properties[PropUnnumbered])

		slices.Sort(interconnects)
		require.Equal(t, []string{
			"ic-leaf-extra<>ic-leaf-main",
			"ic-num-extra<>ic-num-main",
			"ic-reused-extra-1<>ic-reused-main-1",
			"ic-reused-extra-2<>ic-reused-main-2",
			"ic-shared-extra-1<>ic-shared-main-1",
			"ic-unnum-extra<>ic-unnum-main",
		}, interconnects)
	}
}
