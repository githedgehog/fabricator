// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func buildInterconnectWiring(t *testing.T) *apiutil.Loader {
	t.Helper()

	l := apiutil.NewLoader()
	b := &VLABBuilderDefault{
		ExtBGPCount:         1,
		ExtOrphanConnCount:  1,
		ExtraFabric:         true,
		InterconnectFabrics: true,
		VLABBuilderBase:     VLABBuilderBase{DefaultSwitchProfile: meta.SwitchProfileVS},
	}
	require.NoError(t, b.Build(t.Context(), l, fabapi.FabConfig{Fabric: fabapi.FabricConfig{Mode: meta.FabricModeSpineLeaf}}, nil))

	return l
}

// interconnectExternals returns the two annotated Externals of the generated wiring
func interconnectExternals(t *testing.T, kube kclient.Client) []vpcapi.External {
	t.Helper()

	exts := &vpcapi.ExternalList{}
	require.NoError(t, kube.List(t.Context(), exts))

	res := []vpcapi.External{}
	for _, ext := range exts.Items {
		if ext.Annotations[VLABInterconnectAnnotation] != "" {
			res = append(res, ext)
		}
	}
	require.Len(t, res, 2)

	return res
}

// (a) an interconnect External attached over a connection that is not an External one
func TestCreateVLABConfigInterconnectOverNonExternalConnection(t *testing.T) {
	l := buildInterconnectWiring(t)
	kube := l.GetClient()

	// pick a connection that carries no External link
	conns := &wiringapi.ConnectionList{}
	require.NoError(t, kube.List(t.Context(), conns))
	other := ""
	for _, conn := range conns.Items {
		if conn.Spec.External == nil && conn.Spec.Fabric != nil {
			other = conn.Name

			break
		}
	}
	require.NotEmpty(t, other)

	ic := interconnectExternals(t, kube)
	attachs := &vpcapi.ExternalAttachmentList{}
	require.NoError(t, kube.List(t.Context(), attachs))
	changed := false
	for _, attach := range attachs.Items {
		if attach.Spec.External == ic[0].Name {
			attach.Spec.Connection = other
			require.NoError(t, kube.Update(t.Context(), &attach))
			changed = true
		}
	}
	require.True(t, changed)

	require.NotPanics(t, func() {
		_, err := createVLABConfig(t.Context(), nil, nil, kube)
		t.Logf("createVLABConfig error: %v", err)
		require.Error(t, err)
	})
}

// (b) an asymmetric annotation A->B and B->C, where C is an External without annotation or attachment
func TestCreateVLABConfigInterconnectAsymmetricChain(t *testing.T) {
	l := buildInterconnectWiring(t)
	kube := l.GetClient()

	ic := interconnectExternals(t, kube)
	c := &vpcapi.External{
		ObjectMeta: kmetav1.ObjectMeta{Name: "ext-c", Namespace: ic[0].Namespace},
		Spec:       ic[0].Spec,
	}
	require.NoError(t, kube.Create(t.Context(), c))

	// B (second) now points to C, A (first) still points to B
	b := ic[1]
	b.Annotations[VLABInterconnectAnnotation] = "ext-c"
	require.NoError(t, kube.Update(t.Context(), &b))
	a := ic[0]
	a.Annotations[VLABInterconnectAnnotation] = b.Name
	require.NoError(t, kube.Update(t.Context(), &a))

	require.NotPanics(t, func() {
		_, err := createVLABConfig(t.Context(), nil, nil, kube)
		t.Logf("createVLABConfig error: %v", err)
		require.Error(t, err)
	})
}

// (b2) a third interconnect External C annotated to B, with its own External connection that sorts after B's:
// the connection loop cables only from the connection that sorts first, so C's link would silently not be cabled
func TestCreateVLABConfigInterconnectTwoExternalsToOnePeer(t *testing.T) {
	twoExternalsToOnePeer(t, "zz-ext-c-conn")
}

// (b3) same, with C's connection sorting before B's: B's port would be cabled twice
func TestCreateVLABConfigInterconnectTwoExternalsToOnePeerSortsFirst(t *testing.T) {
	twoExternalsToOnePeer(t, "000-ext-c-conn")
}

func twoExternalsToOnePeer(t *testing.T, connName string) {
	t.Helper()

	l := buildInterconnectWiring(t)
	kube := l.GetClient()

	ic := interconnectExternals(t, kube)
	attachs := &vpcapi.ExternalAttachmentList{}
	require.NoError(t, kube.List(t.Context(), attachs))
	var srcAttach vpcapi.ExternalAttachment
	for _, attach := range attachs.Items {
		if attach.Spec.External == ic[0].Name {
			srcAttach = attach
		}
	}
	require.NotEmpty(t, srcAttach.Name)
	srcConn := &wiringapi.Connection{}
	require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Namespace: srcAttach.Namespace, Name: srcAttach.Spec.Connection}, srcConn))
	require.NotNil(t, srcConn.Spec.External)

	// C: another External annotated to B's name, on a copy of A's connection with a different (unused) port
	c := &vpcapi.External{
		ObjectMeta: kmetav1.ObjectMeta{Name: "ext-c", Namespace: ic[0].Namespace, Annotations: map[string]string{VLABInterconnectAnnotation: ic[1].Name}},
		Spec:       ic[0].Spec,
	}
	require.NoError(t, kube.Create(t.Context(), c))
	cConn := &wiringapi.Connection{
		ObjectMeta: kmetav1.ObjectMeta{Name: connName, Namespace: srcConn.Namespace},
		Spec: wiringapi.ConnectionSpec{External: &wiringapi.ConnExternal{Link: wiringapi.ConnExternalLink{
			Switch: wiringapi.BasePortName{Port: "leaf-01/E1/60"},
		}}},
	}
	require.NoError(t, kube.Create(t.Context(), cConn))
	cAttach := srcAttach.DeepCopy()
	cAttach.ObjectMeta = kmetav1.ObjectMeta{Name: "zz-ext-c-attach", Namespace: srcAttach.Namespace}
	cAttach.Spec.External = "ext-c"
	cAttach.Spec.Connection = cConn.Name
	require.NoError(t, kube.Create(t.Context(), cAttach))

	require.NotPanics(t, func() {
		_, err := createVLABConfig(t.Context(), nil, nil, kube)
		t.Logf("createVLABConfig error: %v", err)
		require.Error(t, err)
	})
}
