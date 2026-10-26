// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp/gateway"
)

func TestNodeIgnitionKernelArgs(t *testing.T) {
	for _, tt := range []struct {
		name  string
		roles []fabapi.FabNodeRole
		want  []string
	}{
		{
			name:  "gateway",
			roles: []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
			want:  gateway.KernelArgs(),
		},
		{
			name: "no-roles",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := &NodeInstallBuilder{
				Mode: BuildModeISO,
				Node: fabapi.FabNode{
					Spec: fabapi.FabNodeSpec{
						Roles: tt.roles,
						Management: fabapi.ControlNodeManagement{
							IP:        "172.30.0.8/21",
							Interface: "enp2s0",
						},
						Dummy: fabapi.ControlNodeDummy{
							IP: "172.30.90.0/31",
						},
					},
				},
			}

			raw, err := b.buildIgnition()
			require.NoError(t, err)

			ign := struct {
				KernelArguments struct {
					ShouldExist []string `json:"shouldExist"`
				} `json:"kernelArguments"`
			}{}
			require.NoError(t, json.Unmarshal(raw, &ign))
			require.Equal(t, tt.want, ign.KernelArguments.ShouldExist)
		})
	}
}
