// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestBackfillTopology(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := runtime.NewScheme()
	require.NoError(t, wiringapi.AddToScheme(scheme))
	require.NoError(t, vpcapi.AddToScheme(scheme))
	require.NoError(t, gwapi.AddToScheme(scheme))

	// written before topology existed
	old := &vpcapi.VPC{ObjectMeta: kmetav1.ObjectMeta{Name: "vpc-01", Namespace: kmetav1.NamespaceDefault}}

	// already in another fabric, must be left alone
	current := &wiringapi.Switch{
		ObjectMeta: kmetav1.ObjectMeta{Name: "leaf-b1", Namespace: kmetav1.NamespaceDefault},
		Spec: wiringapi.SwitchSpec{
			Role:     wiringapi.SwitchRoleServerLeaf,
			Topology: wiringapi.SwitchTopology{Fabric: "fab-b", Domains: []string{"plane-a"}},
		},
	}
	current.Default()

	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old, current).Build()
	before := &wiringapi.Switch{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKeyFromObject(current), before))

	require.NoError(t, backfillTopology(ctx, kube))

	vpc := &vpcapi.VPC{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKeyFromObject(old), vpc))
	require.Equal(t, wiringapi.DefaultFabric, vpc.Spec.Topology.Fabric)
	require.Equal(t, []string{wiringapi.DefaultFabricDomain}, vpc.Spec.Topology.Domains)
	require.Equal(t, wiringapi.ListLabelValue, vpc.Labels[wiringapi.ListLabelFabric(wiringapi.DefaultFabric)])

	sw := &wiringapi.Switch{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKeyFromObject(current), sw))
	require.Equal(t, before.ResourceVersion, sw.ResourceVersion)
}

func TestBackfillTopologyRetriesConflict(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := runtime.NewScheme()
	require.NoError(t, wiringapi.AddToScheme(scheme))
	require.NoError(t, vpcapi.AddToScheme(scheme))
	require.NoError(t, gwapi.AddToScheme(scheme))

	old := &vpcapi.VPC{ObjectMeta: kmetav1.ObjectMeta{Name: "vpc-01", Namespace: kmetav1.NamespaceDefault}}

	conflicts := 0
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, kube kclient.WithWatch, obj kclient.Object, opts ...kclient.UpdateOption) error {
			if conflicts == 0 {
				conflicts++

				return kapierrors.NewConflict(schema.GroupResource{Resource: "vpcs"}, obj.GetName(), nil)
			}

			return kube.Update(ctx, obj, opts...)
		},
	}).Build()

	require.NoError(t, backfillTopology(ctx, kube))
	require.Equal(t, 1, conflicts)

	vpc := &vpcapi.VPC{}
	require.NoError(t, kube.Get(ctx, kclient.ObjectKeyFromObject(old), vpc))
	require.Equal(t, wiringapi.DefaultFabric, vpc.Spec.Topology.Fabric)
}
