// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/api/valid"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	fabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

// Everything the builders generate is in the default fabric, so the wiring has
// to carry Fabric/default, with the spec the controller would seed it with.
func TestVLABBuildersGenerateDefaultFabric(t *testing.T) {
	t.Parallel()

	for name, builder := range map[string]VLABBuilder{
		"default": &VLABBuilderDefault{
			VLABBuilderBase: VLABBuilderBase{DefaultSwitchProfile: fmeta.SwitchProfileVS},
		},
		"gpu-rail": &VLABBuilderGPURail{
			ScalableUnits:        1,
			VPCs:                 1,
			ServersPerVPCPerUnit: 1,
			VLABBuilderBase:      VLABBuilderBase{DefaultSwitchProfile: fmeta.SwitchProfileVS},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
			f.Default()

			l := apiutil.NewLoader()
			require.NoError(t, builder.Build(ctx, l, f, nil))

			cfg, err := fabcomp.GetFabricConfig(f)
			require.NoError(t, err)

			fabrics := &wiringapi.FabricList{}
			require.NoError(t, l.List(ctx, fabrics))
			require.Len(t, fabrics.Items, 1)
			require.Equal(t, wiringapi.DefaultFabric, fabrics.Items[0].Name)
			require.Equal(t, wiringapi.DefaultFabricSpec(cfg), fabrics.Items[0].Spec)

			cfg, err = cfg.Init(fmeta.ExtraValidators{Peering: valid.Peering})
			require.NoError(t, err)

			fabrics.Items[0].Default()
			_, err = fabrics.Items[0].Validate(ctx, l.GetClient(), cfg)
			require.NoError(t, err)
		})
	}
}
