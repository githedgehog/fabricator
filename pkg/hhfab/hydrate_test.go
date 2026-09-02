// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

// hydrateFixture builds an unhydrated spine-leaf wiring with one fabric, one mesh and
// one gateway connection.
func hydrateFixture(t *testing.T, unnumbered bool) (*Config, kclient.Client) {
	t.Helper()

	fabricator := fabapi.Fabricator{
		ObjectMeta: kmetav1.ObjectMeta{Name: "default", Namespace: "fab"},
		Spec:       fabapi.FabricatorSpec{Config: fab.DefaultConfig},
	}
	fabricator.Spec.Config.Gateway.Enable = true
	fabricator.Default()

	c := &Config{
		Fab: fabricator,
		Controls: []fabapi.ControlNode{
			{ObjectMeta: kmetav1.ObjectMeta{Name: "control-1", Namespace: "fab"}},
		},
		Nodes: []fabapi.FabNode{
			{ObjectMeta: kmetav1.ObjectMeta{Name: "gw-1", Namespace: "fab"}, Spec: fabapi.FabNodeSpec{
				Roles: []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
			}},
		},
		UnnumberedFabricLinks: unnumbered,
	}

	meta := func(name string) kmetav1.ObjectMeta {
		return kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault}
	}
	port := func(p string) wiringapi.ConnFabricLinkSwitch {
		return wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: p}}
	}

	l := apiutil.NewLoader()
	require.NoError(t, l.Add(context.Background(),
		&wiringapi.Switch{ObjectMeta: meta("spine-1"), Spec: wiringapi.SwitchSpec{Role: wiringapi.SwitchRoleSpine}},
		&wiringapi.Switch{ObjectMeta: meta("leaf-1"), Spec: wiringapi.SwitchSpec{Role: wiringapi.SwitchRoleServerLeaf}},
		&wiringapi.Switch{ObjectMeta: meta("leaf-2"), Spec: wiringapi.SwitchSpec{Role: wiringapi.SwitchRoleServerLeaf}},
		&wiringapi.Connection{ObjectMeta: meta("spine-1--fabric--leaf-1"), Spec: wiringapi.ConnectionSpec{
			Fabric: &wiringapi.ConnFabric{Links: []wiringapi.FabricLink{
				{Spine: port("spine-1/E1/1"), Leaf: port("leaf-1/E1/1")},
				{Spine: port("spine-1/E1/2"), Leaf: port("leaf-1/E1/2")},
			}},
		}},
		&wiringapi.Connection{ObjectMeta: meta("leaf-1--mesh--leaf-2"), Spec: wiringapi.ConnectionSpec{
			Mesh: &wiringapi.ConnMesh{Links: []wiringapi.MeshLink{
				{Leaf1: port("leaf-1/E1/3"), Leaf2: port("leaf-2/E1/3")},
			}},
		}},
		&wiringapi.Connection{ObjectMeta: meta("spine-1--gateway--gw-1"), Spec: wiringapi.ConnectionSpec{
			Gateway: &wiringapi.ConnGateway{Links: []wiringapi.GatewayLink{{
				Switch:  port("spine-1/E1/4"),
				Gateway: wiringapi.ConnGatewayLinkGateway{BasePortName: wiringapi.BasePortName{Port: "gw-1/enp2s1"}},
			}}},
		}},
		&gwapi.Gateway{ObjectMeta: meta("gw-1"), Spec: gwapi.GatewaySpec{
			Interfaces: map[string]gwapi.GatewayInterface{"enp2s1": {}},
		}},
	))

	return c, l.GetClient()
}

// linkIPs returns the fabric+mesh link IPs and the gateway link IPs, in wiring order.
func linkIPs(ctx context.Context, t *testing.T, kube kclient.Client) (fabricMesh, gateway []string) {
	t.Helper()

	conns := &wiringapi.ConnectionList{}
	require.NoError(t, kube.List(ctx, conns))

	fabricMesh, gateway = []string{}, []string{}
	for _, conn := range conns.Items {
		switch {
		case conn.Spec.Fabric != nil:
			for _, link := range conn.Spec.Fabric.Links {
				fabricMesh = append(fabricMesh, link.Spine.IP, link.Leaf.IP)
			}
		case conn.Spec.Mesh != nil:
			for _, link := range conn.Spec.Mesh.Links {
				fabricMesh = append(fabricMesh, link.Leaf1.IP, link.Leaf2.IP)
			}
		case conn.Spec.Gateway != nil:
			for _, link := range conn.Spec.Gateway.Links {
				gateway = append(gateway, link.Switch.IP, link.Gateway.IP)
			}
		}
	}

	return fabricMesh, gateway
}

func TestHydrateNumbered(t *testing.T) {
	ctx := context.Background()
	c, kube := hydrateFixture(t, false)

	h, err := c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusNone, h)

	require.NoError(t, c.hydrate(ctx, kube))

	fabricMesh, gateway := linkIPs(ctx, t, kube)
	require.Equal(t, []string{
		"172.30.128.0/31", "172.30.128.1/31",
		"172.30.128.2/31", "172.30.128.3/31",
		"172.30.128.4/31", "172.30.128.5/31",
	}, fabricMesh)
	require.Equal(t, []string{"172.30.128.6/31", "172.30.128.7/31"}, gateway)
	// the node recipe keeps IPv6 link-local on these ports for BGP unnumbered
	require.Equal(t, []string{"enp2s1"}, c.Nodes[0].Spec.GatewayPorts)

	h, err = c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusFull, h)

	// flipping the toggle on an already hydrated wiring must report the leftover /31s as
	// outstanding work, so that if-not-present refuses and override clears them
	c.UnnumberedFabricLinks = true
	h, err = c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusPartial, h)

	// an older hhfab appended on every hydrate, so a wiring can arrive with several
	// neighbors on one port; all of them are stale once the link is renumbered
	gw := &gwapi.Gateway{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: "gw-1"}, gw))
	gw.Spec.Neighbors = append(gw.Spec.Neighbors, gwapi.GatewayBGPNeighbor{
		Source: "enp2s1", IP: "172.30.128.6", ASN: 65100,
	})
	require.NoError(t, kube.Update(ctx, gw))

	require.NoError(t, c.hydrate(ctx, kube))

	fabricMesh, gateway = linkIPs(ctx, t, kube)
	require.Equal(t, []string{"", "", "", "", "", ""}, fabricMesh)
	// the freed /31s renumber the gateway link, which must not leave old neighbors behind
	require.Equal(t, []string{"172.30.128.0/31", "172.30.128.1/31"}, gateway)

	require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: "gw-1"}, gw))
	require.Equal(t, []gwapi.GatewayBGPNeighbor{
		{Source: "enp2s1", IP: "172.30.128.0", ASN: 65100},
	}, gw.Spec.Neighbors)
	require.Equal(t, []string{"172.30.128.1/31"}, gw.Spec.Interfaces["enp2s1"].IPs)

	h, err = c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusFull, h)
}

func TestHydrateUnnumbered(t *testing.T) {
	ctx := context.Background()
	c, kube := hydrateFixture(t, true)

	h, err := c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusNone, h)

	require.NoError(t, c.hydrate(ctx, kube))

	fabricMesh, gateway := linkIPs(ctx, t, kube)
	require.Equal(t, []string{"", "", "", "", "", ""}, fabricMesh)
	// gateway links stay numbered and start at the base of the fabric subnet
	require.Equal(t, []string{"172.30.128.0/31", "172.30.128.1/31"}, gateway)

	h, err = c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusFull, h)
}
