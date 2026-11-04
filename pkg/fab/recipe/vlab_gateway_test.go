// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSetVLABHugePagesOnce(t *testing.T) {
	gwRoles := []fabapi.FabNodeRole{fabapi.NodeRoleGateway}
	hw := map[string]string{vlabcomp.NodeTypeAnnotation: vlabcomp.NodeTypeHW}
	hp := func(val string) map[string]string {
		return map[string]string{gwapi.AnnotationHugePages1G: val}
	}

	scheme := runtime.NewScheme()
	require.NoError(t, gwapi.AddToScheme(scheme))
	require.NoError(t, fabapi.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&fabapi.FabNode{Name: "gateway-1", Namespace: comp.FabNamespace, Spec: fabapi.FabNodeSpec{Roles: gwRoles}},
		&fabapi.FabNode{Name: "gateway-2", Namespace: comp.FabNamespace, Annotations: hw, Spec: fabapi.FabNodeSpec{Roles: gwRoles}},
		&fabapi.FabNode{Name: "gateway-3", Namespace: comp.FabNamespace, Annotations: hp("8"), Spec: fabapi.FabNodeSpec{Roles: gwRoles}},
		&fabapi.FabNode{Name: "gateway-4", Namespace: comp.FabNamespace, Spec: fabapi.FabNodeSpec{Roles: gwRoles}},
		&fabapi.FabNode{Name: "gateway-5", Namespace: comp.FabNamespace, Annotations: hp("2"), Spec: fabapi.FabNodeSpec{Roles: gwRoles}},
		&gwapi.Gateway{Name: "gateway-1", Namespace: kmetav1.NamespaceDefault},
		&gwapi.Gateway{Name: "gateway-2", Namespace: kmetav1.NamespaceDefault},
		&gwapi.Gateway{Name: "gateway-3", Namespace: kmetav1.NamespaceDefault, Annotations: hp("8")},
		&gwapi.Gateway{Name: "gateway-4", Namespace: kmetav1.NamespaceDefault, Annotations: hp("6")},
		&gwapi.Gateway{Name: "gateway-5", Namespace: kmetav1.NamespaceDefault},
	).Build()

	require.NoError(t, setVLABHugePagesOnce(t.Context(), kube))

	// the value set on either the node or the gateway goes to the other one
	for name, want := range map[string]string{"gateway-1": "4", "gateway-2": "", "gateway-3": "8", "gateway-4": "6", "gateway-5": "2"} {
		node := &fabapi.FabNode{}
		require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Namespace: comp.FabNamespace, Name: name}, node))
		require.Equal(t, want, node.Annotations[gwapi.AnnotationHugePages1G], "node %s", name)

		gw := &gwapi.Gateway{}
		require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: name}, gw))
		require.Equal(t, want, gw.Annotations[gwapi.AnnotationHugePages1G], "gateway %s", name)
	}
}
