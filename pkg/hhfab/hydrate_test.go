// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const hydrateTestFabricB = `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: fab-b
spec:
  leafASNStart: 64600
  leafASNEnd: 64699
  domains:
    plane-a:
      spineASN: 64700
      gatewayASN: 64701
    plane-b:
      spineASN: 64702
      gatewayASN: 64703
`

const hydrateTestWiring = hydrateTestFabricB + `
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: default
spec:
  leafASNStart: 65101
  leafASNEnd: 65533
  domains:
    default:
      spineASN: 65100
      gatewayASN: 65534
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: spine-01
spec:
  role: spine
  profile: vs
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-01
spec:
  role: server-leaf
  profile: vs
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: spine-b1
spec:
  role: spine
  profile: vs
  topology:
    fabric: fab-b
    domains: [plane-a]
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: spine-b2
spec:
  role: spine
  profile: vs
  topology:
    fabric: fab-b
    domains: [plane-b]
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-b1
spec:
  role: server-leaf
  profile: vs
  topology:
    fabric: fab-b
    domains: [plane-a, plane-b]
---
apiVersion: gateway.githedgehog.com/v1alpha1
kind: Gateway
metadata:
  name: gateway-b
spec:
  topology:
    fabric: fab-b
    domain: plane-b
`

func TestHydrateFabricsAndDomains(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestWiring)))
	kube := l.GetClient()

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.NoError(t, c.hydrate(ctx, kube))

	status, err := c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusFull, status)

	for name, asn := range map[string]uint32{
		"spine-01": 65100,
		"leaf-01":  65101,
		"spine-b1": 64700,
		"spine-b2": 64702,
		"leaf-b1":  64600,
	} {
		sw := &wiringapi.Switch{}
		require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: name}, sw))
		require.Equal(t, asn, sw.Spec.ASN, name)
	}

	gw := &gwapi.Gateway{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: "gateway-b"}, gw))
	require.Equal(t, uint32(64703), gw.Spec.ASN)
}

func TestHydrateUnknownDomain(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestFabricB+`
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: spine-b1
spec:
  role: spine
  profile: vs
  topology:
    fabric: fab-b
`)))

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.ErrorContains(t, c.hydrate(ctx, l.GetClient()), "domain default not found in fabric fab-b")
}

// default fabric is unnumbered, fab-b is annotated to be numbered
const hydrateTestLinksWiring = `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: default
spec:
  leafASNStart: 65101
  leafASNEnd: 65533
  domains:
    default:
      spineASN: 65100
      gatewayASN: 65534
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: fab-b
  annotations:
    hydrate.hhfab.githedgehog.com/numbered-links: "true"
spec:
  leafASNStart: 64600
  leafASNEnd: 64699
  domains:
    default:
      spineASN: 64700
      gatewayASN: 64701
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: spine-01
spec:
  role: spine
  profile: vs
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-01
spec:
  role: server-leaf
  profile: vs
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-02
spec:
  role: server-leaf
  profile: vs
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: spine-b1
spec:
  role: spine
  profile: vs
  topology:
    fabric: fab-b
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-b1
spec:
  role: server-leaf
  profile: vs
  topology:
    fabric: fab-b
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-b2
spec:
  role: server-leaf
  profile: vs
  topology:
    fabric: fab-b
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: spine-01--fabric--leaf-01
spec:
  fabric:
    links:
      - spine:
          port: spine-01/E1/1
        leaf:
          port: leaf-01/E1/1
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: leaf-01--mesh--leaf-02
spec:
  mesh:
    links:
      - leaf1:
          port: leaf-01/E1/2
        leaf2:
          port: leaf-02/E1/2
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: spine-b1--fabric--leaf-b1
spec:
  topology:
    fabric: fab-b
  fabric:
    links:
      - spine:
          port: spine-b1/E1/1
        leaf:
          port: leaf-b1/E1/1
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: leaf-b1--mesh--leaf-b2
spec:
  topology:
    fabric: fab-b
  mesh:
    links:
      - leaf1:
          port: leaf-b1/E1/2
        leaf2:
          port: leaf-b2/E1/2
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: spine-01--gateway--gateway-1
spec:
  gateway:
    links:
      - switch:
          port: spine-01/E1/3
        gateway:
          port: gateway-1/enp2s1
---
apiVersion: gateway.githedgehog.com/v1alpha1
kind: Gateway
metadata:
  name: gateway-1
spec:
  interfaces:
    enp2s1: {}
`

// fabric connection of the unnumbered default fabric and IPs to set on its link by hand
const (
	hydrateTestUnnumberedConn = "spine-01--fabric--leaf-01"
	hydrateTestSpineIP        = "172.30.129.0/31"
	hydrateTestLeafIP         = "172.30.129.1/31"
)

func hydrateTestSetUnnumberedLinkIPs(t *testing.T, kube kclient.Client, spineIP, leafIP string) {
	t.Helper()

	conn := &wiringapi.Connection{}
	require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Namespace: "default", Name: hydrateTestUnnumberedConn}, conn))
	conn.Spec.Fabric.Links[0].Spine.IP = spineIP
	conn.Spec.Fabric.Links[0].Leaf.IP = leafIP
	require.NoError(t, kube.Update(t.Context(), conn))
}

// hydrateTestLinkIPs returns the IPs of the first link of each fabric, mesh and gateway connection by its name
func hydrateTestLinkIPs(t *testing.T, kube kclient.Reader) map[string][]string {
	t.Helper()

	conns := &wiringapi.ConnectionList{}
	require.NoError(t, kube.List(t.Context(), conns))

	ips := map[string][]string{}
	for _, conn := range conns.Items {
		switch {
		case conn.Spec.Fabric != nil:
			ips[conn.Name] = []string{conn.Spec.Fabric.Links[0].Spine.IP, conn.Spec.Fabric.Links[0].Leaf.IP}
		case conn.Spec.Mesh != nil:
			ips[conn.Name] = []string{conn.Spec.Mesh.Links[0].Leaf1.IP, conn.Spec.Mesh.Links[0].Leaf2.IP}
		case conn.Spec.Gateway != nil:
			ips[conn.Name] = []string{conn.Spec.Gateway.Links[0].Switch.IP, conn.Spec.Gateway.Links[0].Gateway.IP}
		}
	}

	return ips
}

func TestHydrateNumberedLinks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestLinksWiring)))
	kube := l.GetClient()

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}

	status, err := c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusNone, status)

	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeIfNotPresent))

	// connections are hydrated in name order and unnumbered links don't take any IPs
	hydrated := map[string][]string{
		"leaf-01--mesh--leaf-02":       {"", ""},
		"leaf-b1--mesh--leaf-b2":       {"172.30.128.0/31", "172.30.128.1/31"},
		"spine-01--fabric--leaf-01":    {"", ""},
		"spine-01--gateway--gateway-1": {"172.30.128.2/31", "172.30.128.3/31"},
		"spine-b1--fabric--leaf-b1":    {"172.30.128.4/31", "172.30.128.5/31"},
	}
	require.Equal(t, hydrated, hydrateTestLinkIPs(t, kube))

	gwKey := kclient.ObjectKey{Namespace: "default", Name: "gateway-1"}
	gwNeighbors := []gwapi.GatewayBGPNeighbor{{Source: "enp2s1", IP: "172.30.128.2", ASN: 65100}}
	gw := &gwapi.Gateway{}
	require.NoError(t, kube.Get(ctx, gwKey, gw))
	require.Equal(t, gwNeighbors, gw.Spec.Neighbors)
	require.Equal(t, []string{"172.30.128.3/31"}, gw.Spec.Interfaces["enp2s1"].IPs)

	// IPs on an unnumbered link are not something to hydrate, so they are kept unless overridden
	hydrateTestSetUnnumberedLinkIPs(t, kube, hydrateTestSpineIP, hydrateTestLeafIP)

	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeIfNotPresent))
	require.Equal(t, []string{hydrateTestSpineIP, hydrateTestLeafIP}, hydrateTestLinkIPs(t, kube)[hydrateTestUnnumberedConn])

	// a neighbor left from a gateway link that has since moved to another port
	require.NoError(t, kube.Get(ctx, gwKey, gw))
	gw.Spec.Neighbors = append(gw.Spec.Neighbors, gwapi.GatewayBGPNeighbor{Source: "enp2s2", IP: "172.30.128.10", ASN: 65100})
	require.NoError(t, kube.Update(ctx, gw))

	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeOverride))
	require.Equal(t, hydrated, hydrateTestLinkIPs(t, kube))

	// re-hydrating rebuilds the gateway neighbors from the gateway links instead of appending to them
	require.NoError(t, kube.Get(ctx, gwKey, gw))
	require.Equal(t, gwNeighbors, gw.Spec.Neighbors)
}

func TestHydrateUnnumberedLinksOneSided(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestLinksWiring)))
	kube := l.GetClient()

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeIfNotPresent))

	// a link with an IP on just one side is broken, so even in an unnumbered fabric it isn't hydrated
	hydrateTestSetUnnumberedLinkIPs(t, kube, hydrateTestSpineIP, "")

	status, err := c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusPartial, status)

	require.ErrorContains(t, c.ensureHydrated(ctx, kube, HydrateModeNever), "not fully hydrated")
	require.ErrorContains(t, c.ensureHydrated(ctx, kube, HydrateModeIfNotPresent), "partially hydrated")

	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeOverride))
	require.Equal(t, []string{"", ""}, hydrateTestLinkIPs(t, kube)[hydrateTestUnnumberedConn])
}

func TestHydrateUnnumberedLinksPartial(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestLinksWiring)))
	kube := l.GetClient()

	// IPs on an unnumbered link of an otherwise empty wiring are something already there, not nothing
	hydrateTestSetUnnumberedLinkIPs(t, kube, hydrateTestSpineIP, hydrateTestLeafIP)

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}

	status, err := c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusPartial, status)

	require.ErrorContains(t, c.ensureHydrated(ctx, kube, HydrateModeIfNotPresent), "partially hydrated")

	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeOverride))
	require.Equal(t, []string{"", ""}, hydrateTestLinkIPs(t, kube)[hydrateTestUnnumberedConn])
}

func TestHydrateNumberedLinksInvalid(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(`
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: default
  annotations:
    hydrate.hhfab.githedgehog.com/numbered-links: "yes"
spec:
  leafASNStart: 65101
  leafASNEnd: 65533
  domains:
    default:
      spineASN: 65100
      gatewayASN: 65534
`)))

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	_, err := c.getHydration(ctx, l.GetClient())
	require.ErrorContains(t, err, `fabric default annotation hydrate.hhfab.githedgehog.com/numbered-links value "yes"`)
}
