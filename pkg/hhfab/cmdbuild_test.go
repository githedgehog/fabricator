// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
)

func TestParseModeOverrides(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []string
		want map[string]recipe.BuildMode
		err  bool
	}{
		{
			name: "empty",
			want: map[string]recipe.BuildMode{},
		},
		{
			name: "valid",
			raw:  []string{"gateway-bm=iso", "gateway-1=manual"},
			want: map[string]recipe.BuildMode{
				"gateway-bm": recipe.BuildModeISO,
				"gateway-1":  recipe.BuildModeManual,
			},
		},
		{
			name: "no-separator",
			raw:  []string{"gateway-bm"},
			err:  true,
		},
		{
			name: "no-name",
			raw:  []string{"=iso"},
			err:  true,
		},
		{
			name: "no-mode",
			raw:  []string{"gateway-bm="},
			err:  true,
		},
		{
			name: "duplicate",
			raw:  []string{"gateway-bm=iso", "gateway-bm=manual"},
			err:  true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseModeOverrides(tt.raw)
			if tt.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBuildModeOverrides(t *testing.T) {
	hw := map[string]string{HHFabCfgType: HHFabCfgTypeHW}
	c := &Config{
		Controls: []fabapi.ControlNode{
			{Name: "control-1"},
		},
		Nodes: []fabapi.FabNode{
			{Name: "gateway-1"},
			{Name: "gateway-bm", Annotations: hw},
		},
	}

	overrides := c.hardwareModeOverrides(recipe.BuildModeISO)
	require.Equal(t, map[string]recipe.BuildMode{"gateway-bm": recipe.BuildModeISO}, overrides)

	opts := BuildOpts{BuildMode: recipe.BuildModeManual, ModeOverrides: overrides}
	require.Equal(t, recipe.BuildModeManual, buildMode("control-1", opts))
	require.Equal(t, recipe.BuildModeManual, buildMode("gateway-1", opts))
	require.Equal(t, recipe.BuildModeISO, buildMode("gateway-bm", opts))

	opts.ModeOverrides = c.hardwareModeOverrides(recipe.BuildModeManual)
	opts.BuildMode = recipe.BuildModeUSB
	require.Equal(t, recipe.BuildModeUSB, buildMode("gateway-1", opts))
	require.Equal(t, recipe.BuildModeManual, buildMode("gateway-bm", opts))

	err := c.build(t.Context(), BuildOpts{
		BuildMode:     recipe.BuildModeISO,
		ModeOverrides: map[string]recipe.BuildMode{"gateway-2": recipe.BuildModeISO},
	})
	require.ErrorContains(t, err, "unknown control or node")

	err = c.build(t.Context(), BuildOpts{
		BuildMode:     recipe.BuildModeISO,
		ModeOverrides: map[string]recipe.BuildMode{"gateway-1": "pxe"},
	})
	require.ErrorContains(t, err, "invalid build mode")
}
