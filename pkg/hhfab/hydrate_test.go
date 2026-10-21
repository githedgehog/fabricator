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

const hydrateTestNewObjects = `
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
  name: leaf-b2
spec:
  role: server-leaf
  profile: vs
  topology:
    fabric: fab-b
    domains: [plane-a, plane-b]
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: leaf-b1--mesh--leaf-b2
spec:
  mesh:
    links:
      - leaf1:
          port: leaf-b1/E1/2
        leaf2:
          port: leaf-b2/E1/2
  topology:
    fabric: fab-b
---
apiVersion: gateway.githedgehog.com/v1alpha1
kind: Gateway
metadata:
  name: gateway-c
spec:
  topology:
    fabric: fab-b
    domain: plane-a
`

func switchSpec(t *testing.T, kube kclient.Client, name string) wiringapi.SwitchSpec {
	t.Helper()

	sw := &wiringapi.Switch{}
	require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Namespace: "default", Name: name}, sw))

	return sw.Spec
}

func gatewaySpec(t *testing.T, kube kclient.Client, name string) gwapi.GatewaySpec {
	t.Helper()

	gw := &gwapi.Gateway{}
	require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Namespace: "default", Name: name}, gw))

	return gw.Spec
}

func TestHydrateMissingContinuesAfterExisting(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestWiring)))
	kube := l.GetClient()

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.NoError(t, c.hydrate(ctx, kube))

	before := map[string]wiringapi.SwitchSpec{}
	for _, name := range []string{"spine-01", "leaf-01", "spine-b1", "spine-b2", "leaf-b1"} {
		before[name] = switchSpec(t, kube, name)
	}
	gwBefore := gatewaySpec(t, kube, "gateway-b")

	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestNewObjects)))

	status, err := c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusPartial, status)

	require.NoError(t, c.hydrateMissing(ctx, kube))

	status, err = c.getHydration(ctx, kube)
	require.NoError(t, err)
	require.Equal(t, HydrationStatusFull, status)

	for name, spec := range before {
		require.Equal(t, spec, switchSpec(t, kube, name), "existing switch %s must not change", name)
	}
	require.Equal(t, gwBefore, gatewaySpec(t, kube, "gateway-b"), "existing gateway must not change")

	// every pool continues after what was in use: leaf ASNs per fabric, then the shared management, protocol and VTEP pools
	leaf02 := switchSpec(t, kube, "leaf-02")
	require.Equal(t, uint32(65102), leaf02.ASN)
	leafB2 := switchSpec(t, kube, "leaf-b2")
	require.Equal(t, uint32(64601), leafB2.ASN)

	leaf01 := before["leaf-01"]
	require.NotEqual(t, leaf01.IP, leaf02.IP)
	require.NotEqual(t, leaf01.VTEPIP, leaf02.VTEPIP)
	require.NotEqual(t, leaf01.ProtocolIP, leaf02.ProtocolIP)

	seen := map[string]string{}
	for name, spec := range map[string]wiringapi.SwitchSpec{
		"spine-01": before["spine-01"], "leaf-01": leaf01, "spine-b1": before["spine-b1"], "spine-b2": before["spine-b2"],
		"leaf-b1": before["leaf-b1"], "leaf-02": leaf02, "leaf-b2": leafB2,
	} {
		for kind, value := range map[string]string{"ip": spec.IP, "protocol": spec.ProtocolIP} {
			key := kind + " " + value
			require.Empty(t, seen[key], "%s of %s is already used by %s", kind, name, seen[key])
			seen[key] = name
		}
	}

	gwC := gatewaySpec(t, kube, "gateway-c")
	require.Equal(t, uint32(64701), gwC.ASN)
	require.NotEqual(t, gwBefore.VTEPMAC, gwC.VTEPMAC)
	require.NotEqual(t, gwBefore.ProtocolIP, gwC.ProtocolIP)
	require.NotEqual(t, gwBefore.VTEPIP, gwC.VTEPIP)

	conn := &wiringapi.Connection{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: "leaf-b1--mesh--leaf-b2"}, conn))
	require.Len(t, conn.Spec.Mesh.Links, 1)
	require.NotEmpty(t, conn.Spec.Mesh.Links[0].Leaf1.IP)
	require.NotEmpty(t, conn.Spec.Mesh.Links[0].Leaf2.IP)

	// a second run has nothing left to allocate
	require.NoError(t, c.hydrateMissing(ctx, kube))
	require.Equal(t, leaf02, switchSpec(t, kube, "leaf-02"))
	require.Equal(t, gwC, gatewaySpec(t, kube, "gateway-c"))
}

func TestHydrateMissingFromScratchMatchesHydrate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	wiring := hydrateTestWiring + "\n---\n" + hydrateTestNewObjects

	full := apiutil.NewLoader()
	require.NoError(t, full.LoadAddWiring(ctx, []byte(wiring)))
	missing := apiutil.NewLoader()
	require.NoError(t, missing.LoadAddWiring(ctx, []byte(wiring)))

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.NoError(t, c.hydrate(ctx, full.GetClient()))
	require.NoError(t, c.hydrateMissing(ctx, missing.GetClient()))

	for _, name := range []string{"spine-01", "leaf-01", "leaf-02", "spine-b1", "spine-b2", "leaf-b1", "leaf-b2"} {
		require.Equal(t, switchSpec(t, full.GetClient(), name), switchSpec(t, missing.GetClient(), name), name)
	}
	for _, name := range []string{"gateway-b", "gateway-c"} {
		require.Equal(t, gatewaySpec(t, full.GetClient(), name), gatewaySpec(t, missing.GetClient(), name), name)
	}
}

func TestHydrateMissingLeafASNRangeExhausted(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(`
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: default
spec:
  leafASNStart: 65101
  leafASNEnd: 65101
  domains:
    default:
      spineASN: 65100
      gatewayASN: 65534
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
`)))

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.ErrorContains(t, c.hydrateMissing(ctx, l.GetClient()), "leaf leaf-02 of fabric default: no free ASN left in the leaf ASN range ending at 65101")
}

func TestEnsureHydratedAutoOnPartiallyHydrated(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestWiring)))
	kube := l.GetClient()

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeIfNotPresent))

	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestNewObjects)))
	require.ErrorContains(t, c.ensureHydrated(ctx, kube, HydrateModeIfNotPresent), "already partially hydrated")
	require.ErrorContains(t, c.ensureHydrated(ctx, kube, HydrateModeNever), "not fully hydrated")

	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeAuto))
	require.NoError(t, c.ensureHydrated(ctx, kube, HydrateModeNever))
}

const hydrateTestMeshLinks = `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: leaf-b1--mesh--leaf-b2
spec:
  mesh:
    links:
      - leaf1:
          port: leaf-b1/E1/2
          ip: 172.30.128.0/31
        leaf2:
          port: leaf-b2/E1/2
      - leaf1:
          port: leaf-b1/E1/3
        leaf2:
          port: leaf-b2/E1/3
  topology:
    fabric: fab-b
`

func TestHydrateMissingKeepsHalfSetLinkAddress(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestWiring+"\n---\n"+hydrateTestMeshLinks)))
	kube := l.GetClient()

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.NoError(t, c.hydrateMissing(ctx, kube))

	conn := &wiringapi.Connection{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: "leaf-b1--mesh--leaf-b2"}, conn))
	links := conn.Spec.Mesh.Links
	require.Len(t, links, 2)

	// the address that was set stays, its /31 partner completes the link, and the other link gets a different /31
	require.Equal(t, "172.30.128.0/31", links[0].Leaf1.IP)
	require.Equal(t, "172.30.128.1/31", links[0].Leaf2.IP)
	require.Equal(t, "172.30.128.2/31", links[1].Leaf1.IP)
	require.Equal(t, "172.30.128.3/31", links[1].Leaf2.IP)
}

func TestHydrateMissingKeepsLowerCaseVTEPMAC(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestWiring)))
	kube := l.GetClient()

	gw := &gwapi.Gateway{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: "gateway-b"}, gw))
	gw.Spec.VTEPMAC = "ca:fe:ba:be:01:01"
	require.NoError(t, kube.Update(ctx, gw))

	require.NoError(t, l.LoadAddWiring(ctx, []byte(`
apiVersion: gateway.githedgehog.com/v1alpha1
kind: Gateway
metadata:
  name: gateway-c
spec:
  topology:
    fabric: fab-b
    domain: plane-a
`)))

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.NoError(t, c.hydrateMissing(ctx, kube))

	require.Equal(t, "ca:fe:ba:be:01:01", gatewaySpec(t, kube, "gateway-b").VTEPMAC)
	require.Equal(t, "CA:FE:BA:BE:01:02", gatewaySpec(t, kube, "gateway-c").VTEPMAC)
}

func TestHydrateMissingKeepsPresetDummyIP(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(hydrateTestWiring)))

	c := &Config{
		Fab:      fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}},
		Controls: []fabapi.ControlNode{{}},
		Nodes:    []fabapi.FabNode{{}},
	}
	c.Controls[0].Name, c.Nodes[0].Name = "control-1", "node-1"
	c.Controls[0].Spec.Dummy.IP = "172.30.90.4/31"
	c.Nodes[0].Spec.Dummy.IP = "172.30.90.6/31"

	require.NoError(t, c.hydrateMissing(ctx, l.GetClient()))

	require.NotEmpty(t, c.Controls[0].Spec.Management.IP)
	require.NotEmpty(t, c.Nodes[0].Spec.Management.IP)
	require.NotEqual(t, c.Controls[0].Spec.Management.IP, c.Nodes[0].Spec.Management.IP)
	require.EqualValues(t, "172.30.90.4/31", c.Controls[0].Spec.Dummy.IP)
	require.EqualValues(t, "172.30.90.6/31", c.Nodes[0].Spec.Dummy.IP)
}

func mclagTestWiring(group string) string {
	sw := func(name string) string {
		return `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: ` + name + `
spec:
  role: server-leaf
  profile: vs
  redundancy:
    group: ` + group + `
    type: mclag
`
	}

	return hydrateTestFabricB + `
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
---` + sw("leaf-01") + "---" + sw("leaf-02")
}

// the new peer can sort before or after the one that is hydrated, either way the pair ends up with the same values
func TestHydrateMissingMCLAGPeerOrder(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		hydrated string
		added    string
	}{
		{name: "new peer sorts first", hydrated: "leaf-02", added: "leaf-01"},
		{name: "new peer sorts last", hydrated: "leaf-01", added: "leaf-02"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			l := apiutil.NewLoader()
			require.NoError(t, l.LoadAddWiring(ctx, []byte(mclagTestWiring("mclag-1"))))
			kube := l.GetClient()

			sw := &wiringapi.Switch{}
			require.NoError(t, kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: tt.hydrated}, sw))
			sw.Spec.IP, sw.Spec.ProtocolIP = "172.30.0.20/21", "172.30.8.20/32"
			sw.Spec.ASN, sw.Spec.VTEPIP = 65150, "172.30.12.20/32"
			require.NoError(t, kube.Update(ctx, sw))

			c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
			require.NoError(t, c.hydrateMissing(ctx, kube))

			added := switchSpec(t, kube, tt.added)
			require.Equal(t, uint32(65150), added.ASN)
			require.Equal(t, "172.30.12.20/32", added.VTEPIP)
			require.NotEmpty(t, added.IP)
			require.NotEqual(t, "172.30.0.20/21", added.IP, "the peers still have their own management IPs")
			require.Equal(t, uint32(65150), switchSpec(t, kube, tt.hydrated).ASN)
		})
	}
}
