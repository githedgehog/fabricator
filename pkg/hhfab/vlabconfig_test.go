// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetNICID(t *testing.T) {
	for _, tt := range []struct {
		nic  string
		want uint
		err  bool
	}{
		{
			nic: "eth0",
			err: true,
		},
		{
			nic: "eno0",
			err: true,
		},
		{
			nic: "enp1s1",
			err: true,
		},
		{
			nic: "x1",
			err: true,
		},
		{
			nic: "M2",
			err: true,
		},
		{
			nic: "E1",
			err: true,
		},
		{
			nic: "E1/",
			err: true,
		},
		{
			nic: "E1/1/",
			err: true,
		},
		{
			nic: "E1/1/1",
			err: true,
		},
		{
			nic: "E2",
			err: true,
		},
		{
			nic: "E2/",
			err: true,
		},
		{
			nic: "E2/1/",
			err: true,
		},
		{
			nic: "E2/1/1",
			err: true,
		},
		{
			nic: "Management0",
			err: true,
		},
		{
			nic: "Management1",
			err: true,
		},
		{
			nic:  "M1",
			want: 0,
		},
		{
			nic:  "E1/1",
			want: 1,
		},
		{
			nic:  "E1/2",
			want: 2,
		},
		{
			nic:  "E1/99",
			want: 99,
		},
		{
			nic:  "enp2s0",
			want: 0,
		},
		{
			nic:  "enp2s1",
			want: 1,
		},
		{
			nic:  "enp2s2",
			want: 2,
		},
		{
			nic:  "enp2s99",
			want: 99,
		},
		{
			nic:  "enp2s0np1",
			want: 0,
		},
		{
			nic:  "enp2s0np2",
			want: 0,
		},
		{
			nic:  "enp2s0np3",
			want: 0,
		},
		{
			nic:  "enp2s99np42",
			want: 99,
		},
		{
			nic: "enp2s99np42np1",
			err: true,
		},
	} {
		t.Run(tt.nic, func(t *testing.T) {
			got, err := getNICID(tt.nic)

			if tt.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCreateVLABConfigInterconnect(t *testing.T) {
	l := apiutil.NewLoader()
	b := &VLABBuilderDefault{
		ExtBGPCount:         1,
		ExtOrphanConnCount:  1,
		ExtraFabric:         true,
		InterconnectFabrics: true,
		VLABBuilderBase:     VLABBuilderBase{DefaultSwitchProfile: meta.SwitchProfileVS},
	}
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()
	require.NoError(t, b.Build(t.Context(), l, f, nil))

	cfg, err := createVLABConfig(t.Context(), nil, nil, l.GetClient())
	require.NoError(t, err)

	// leaf-03 is the last leaf of the main fabric, and also has the connection to the virtual external
	ports := []string{}
	for _, name := range []string{"leaf-03--interconnect", "leaf-04--interconnect"} {
		conn := &wiringapi.Connection{}
		require.NoError(t, l.GetClient().Get(t.Context(), kclient.ObjectKey{Name: name}, conn))
		ports = append(ports, conn.Spec.External.Link.Switch.Port)
	}
	require.Equal(t, NICTypeDirect+NICTypeSep+ports[1], cfg.VMs["leaf-03"].NICs[strings.SplitN(ports[0], "/", 2)[1]])
	require.Equal(t, NICTypeDirect+NICTypeSep+ports[0], cfg.VMs["leaf-04"].NICs[strings.SplitN(ports[1], "/", 2)[1]])

	require.Equal(t, []string{"ext-bgp-01"}, slices.Collect(maps.Keys(cfg.Externals.VRFs)))
	require.Len(t, cfg.Externals.NICs, 1)
}
