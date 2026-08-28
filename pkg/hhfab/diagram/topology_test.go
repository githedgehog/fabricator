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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
)

// TestTopologyBGPState covers a fabric connection carrying one numbered and one
// unnumbered link. The agent keys the two sessions differently — peer IP for the
// numbered one, local port for the unnumbered one — and the topology has to report
// both, without knowing which rule applies.
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
		ObjectMeta: kmetav1.ObjectMeta{Name: fabapi.FabName, Namespace: fabapi.FabNamespace},
		Spec:       fabapi.FabricatorSpec{Config: fab.DefaultConfig},
	}
	fabricator.Default()

	meta := func(name string) kmetav1.ObjectMeta {
		return kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault}
	}
	port := func(p, ip string) wiringapi.ConnFabricLinkSwitch {
		return wiringapi.ConnFabricLinkSwitch{
			BasePortName: wiringapi.BasePortName{Port: p},
			IP:           ip,
		}
	}
	// an agent reports an unnumbered session under the local port and a numbered one
	// under the peer IP
	agent := func(name string, peers map[string]string, neighbors map[string]string) *agentapi.Agent {
		state := map[string]agentapi.SwitchStateBGPNeighbor{}
		for key, sessionState := range neighbors {
			state[key] = agentapi.SwitchStateBGPNeighbor{
				SessionState: agentapi.BGPNeighborSessionState(sessionState),
			}
		}

		switches := map[string]wiringapi.SwitchSpec{}
		for peer, protocolIP := range peers {
			switches[peer] = wiringapi.SwitchSpec{ProtocolIP: protocolIP}
		}

		return &agentapi.Agent{
			ObjectMeta: meta(name),
			Spec: agentapi.AgentSpec{
				SwitchProfile: &wiringapi.SwitchProfileSpec{},
				Switches:      switches,
			},
			Status: agentapi.AgentStatus{State: agentapi.SwitchState{
				BGPNeighbors: map[string]map[string]agentapi.SwitchStateBGPNeighbor{"default": state},
			}},
		}
	}

	conn := &wiringapi.Connection{
		ObjectMeta: meta("spine-1--fabric--leaf-1"),
		Spec: wiringapi.ConnectionSpec{Fabric: &wiringapi.ConnFabric{Links: []wiringapi.FabricLink{
			{Spine: port("spine-1/E1/1", "172.30.128.0/31"), Leaf: port("leaf-1/E1/1", "172.30.128.1/31")},
			{Spine: port("spine-1/E1/2", ""), Leaf: port("leaf-1/E1/2", "")},
		}}},
	}
	conn.Default() // sets the switch labels apiutil filters connections on

	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		fabricator,
		&wiringapi.Switch{ObjectMeta: meta("spine-1"), Spec: wiringapi.SwitchSpec{Role: wiringapi.SwitchRoleSpine}},
		&wiringapi.Switch{ObjectMeta: meta("leaf-1"), Spec: wiringapi.SwitchSpec{Role: wiringapi.SwitchRoleServerLeaf}},
		conn,
		agent("spine-1", map[string]string{"leaf-1": "172.30.8.1/32"},
			map[string]string{"172.30.128.1": "established", "E1/2": "established"}),
		agent("leaf-1", map[string]string{"spine-1": "172.30.8.0/32"},
			map[string]string{"172.30.128.0": "established", "E1/2": "idle"}),
	).Build()

	topo, err := GetTopologyFor(ctx, kube)
	require.NoError(t, err)

	byPort := map[string]Link{}
	for _, link := range topo.Links {
		byPort[link.Properties[PropSourcePort]] = link
	}
	require.Contains(t, byPort, "spine-1/E1/1")
	require.Contains(t, byPort, "spine-1/E1/2")

	numbered := byPort["spine-1/E1/1"]
	require.Equal(t, "established", numbered.Properties[PropBGPState])
	require.Empty(t, numbered.Properties[PropUnnumbered])
	require.Equal(t, "172.30.128.0/31", numbered.Properties[PropSrcLinkIP])

	unnumbered := byPort["spine-1/E1/2"]
	require.Equal(t, "established", unnumbered.Properties[PropBGPState])
	require.Equal(t, "true", unnumbered.Properties[PropUnnumbered])
	require.Empty(t, unnumbered.Properties[PropSrcLinkIP])
}
