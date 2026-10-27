// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestMigrateVLABGatewayIfaces(t *testing.T) {
	gw := &gwapi.Gateway{Spec: gwapi.GatewaySpec{
		Driver: gwapi.GatewayDriverKernel,
		Interfaces: map[string]gwapi.GatewayInterface{
			"enp2s1": {IPs: []string{"172.30.128.1/31"}, MTU: 9000},
			"enp2s2": {IPs: []string{"172.30.128.3/31"}},
			"port3":  {PCI: "0000:07:00.0", Kernel: "enp7s0"},
		},
		Neighbors: []gwapi.GatewayBGPNeighbor{
			{Source: "enp2s1", IP: "172.30.128.0", ASN: 65100},
			{Source: "enp2s2", IP: "172.30.128.2", ASN: 65100},
		},
	}}

	renames, err := migrateVLABGatewayIfaces(gw)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"enp2s1": "port1", "enp2s2": "port2"}, renames)
	require.Equal(t, map[string]gwapi.GatewayInterface{
		"port1": {PCI: "0000:05:00.0", Kernel: "enp5s0", IPs: []string{"172.30.128.1/31"}, MTU: 9000},
		"port2": {PCI: "0000:06:00.0", Kernel: "enp6s0", IPs: []string{"172.30.128.3/31"}},
		"port3": {PCI: "0000:07:00.0", Kernel: "enp7s0"},
	}, gw.Spec.Interfaces)
	require.Equal(t, "port1", gw.Spec.Neighbors[0].Source)
	require.Equal(t, "port2", gw.Spec.Neighbors[1].Source)

	renames, err = migrateVLABGatewayIfaces(gw)
	require.NoError(t, err)
	require.Empty(t, renames, "not idempotent")

	_, err = migrateVLABGatewayIfaces(&gwapi.Gateway{Spec: gwapi.GatewaySpec{
		Interfaces: map[string]gwapi.GatewayInterface{"enp2s0": {}},
	}})
	require.Error(t, err)
}

func TestMigrateVLABGatewayPorts(t *testing.T) {
	oldGw := func(name string) *gwapi.Gateway {
		return &gwapi.Gateway{
			Name: name, Namespace: kmetav1.NamespaceDefault,
			Spec: gwapi.GatewaySpec{
				Driver:     gwapi.GatewayDriverKernel,
				Interfaces: map[string]gwapi.GatewayInterface{"enp2s1": {}},
			},
		}
	}
	newGw := func(name string) *gwapi.Gateway {
		gw := oldGw(name)
		gw.Spec.Interfaces = map[string]gwapi.GatewayInterface{"port1": {PCI: "0000:05:00.0", Kernel: "enp5s0"}}

		return gw
	}
	conn := func(port string) *wiringapi.Connection {
		return &wiringapi.Connection{
			Name: "spine-01--gateway--gateway-1", Namespace: kmetav1.NamespaceDefault,
			Spec: wiringapi.ConnectionSpec{Gateway: &wiringapi.ConnGateway{Links: []wiringapi.GatewayLink{{
				Switch:  wiringapi.ConnFabricLinkSwitch{BasePortName: wiringapi.BasePortName{Port: "spine-01/E1/5"}},
				Gateway: wiringapi.ConnGatewayLinkGateway{BasePortName: wiringapi.BasePortName{Port: port}},
			}}}},
		}
	}
	hwNode := &fabapi.FabNode{
		Name: "gateway-1", Namespace: comp.FabNamespace,
		Annotations: map[string]string{vlabcomp.NodeTypeAnnotation: vlabcomp.NodeTypeHW},
	}

	for _, tt := range []struct {
		name     string
		objs     []kclient.Object
		conflict bool
		wantIfc  string
		wantPort string
		wantWait bool
	}{
		{
			name:     "full",
			objs:     []kclient.Object{oldGw("gateway-1"), conn("gateway-1/enp2s1")},
			wantIfc:  "port1",
			wantPort: "gateway-1/port1",
			wantWait: true,
		},
		{
			name:     "conflict",
			objs:     []kclient.Object{oldGw("gateway-1"), conn("gateway-1/enp2s1")},
			conflict: true,
			wantIfc:  "port1",
			wantPort: "gateway-1/port1",
			wantWait: true,
		},
		{
			name:     "partial",
			objs:     []kclient.Object{newGw("gateway-1"), conn("gateway-1/enp2s1")},
			wantIfc:  "port1",
			wantPort: "gateway-1/port1",
			wantWait: true,
		},
		{
			name:     "done",
			objs:     []kclient.Object{newGw("gateway-1"), conn("gateway-1/port1")},
			wantIfc:  "port1",
			wantPort: "gateway-1/port1",
		},
		{
			name:     "hw",
			objs:     []kclient.Object{hwNode, oldGw("gateway-1"), conn("gateway-1/enp2s1")},
			wantIfc:  "enp2s1",
			wantPort: "gateway-1/enp2s1",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, gwapi.AddToScheme(scheme))
			require.NoError(t, wiringapi.AddToScheme(scheme))
			require.NoError(t, fabapi.AddToScheme(scheme))
			// the first update of each object conflicts, as if the fabric controller updated it in between
			conflicted := map[string]bool{}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objs...).WithInterceptorFuncs(interceptor.Funcs{
				// unlike the fake client, the real one decodes into the passed object as is, keeping its map entries
				Get: func(ctx context.Context, kube kclient.WithWatch, key kclient.ObjectKey, obj kclient.Object, opts ...kclient.GetOption) error {
					fresh := obj.DeepCopyObject().(kclient.Object) //nolint:forcetypeassert
					if err := kube.Get(ctx, key, fresh, opts...); err != nil {
						return err //nolint:wrapcheck
					}
					raw, err := json.Marshal(fresh)
					if err != nil {
						return err //nolint:wrapcheck
					}

					return json.Unmarshal(raw, obj)
				},
				Update: func(ctx context.Context, kube kclient.WithWatch, obj kclient.Object, opts ...kclient.UpdateOption) error {
					if tt.conflict && !conflicted[obj.GetName()] {
						conflicted[obj.GetName()] = true

						return kapierrors.NewConflict(schema.GroupResource{}, obj.GetName(), nil)
					}

					return kube.Update(ctx, obj, opts...)
				},
			}).Build()

			waited := false
			require.NoError(t, migrateVLABGatewayPorts(t.Context(), kube, func() error {
				waited = true

				return nil
			}))
			require.Equal(t, tt.wantWait, waited)

			gw := &gwapi.Gateway{}
			require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Name: "gateway-1", Namespace: kmetav1.NamespaceDefault}, gw))
			require.Contains(t, gw.Spec.Interfaces, tt.wantIfc)

			c := &wiringapi.Connection{}
			require.NoError(t, kube.Get(t.Context(), kclient.ObjectKeyFromObject(conn("")), c))
			require.Equal(t, tt.wantPort, c.Spec.Gateway.Links[0].Gateway.Port)
		})
	}
}
