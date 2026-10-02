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
	require.NoError(t, l.LoadAdd(ctx, apiutil.FabricGatewayGVKs, []byte(hydrateTestWiring)))
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

func TestHydrateRejectsDefaultFabric(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAdd(ctx, apiutil.FabricGatewayGVKs, []byte(`
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: default
spec:
  leafASNStart: 65101
  leafASNEnd: 65533
`)))

	c := &Config{Fab: fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}}
	require.ErrorContains(t, c.hydrate(ctx, l.GetClient()), "must not be in the wiring")
}

func TestHydrateUnknownDomain(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAdd(ctx, apiutil.FabricGatewayGVKs, []byte(hydrateTestFabricB+`
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
