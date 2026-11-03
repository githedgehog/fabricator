// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

func TestNodeIgnitionKernelArgs(t *testing.T) {
	for _, tt := range []struct {
		name  string
		roles []fabapi.FabNodeRole
		anns  map[string]string
		want  []string
	}{
		{
			name:  "gateway",
			roles: []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
			want:  []string{"iommu=pt", "intel_iommu=on", "hugepagesz=1G", "hugepages=16"},
		},
		{
			name:  "gateway-annotated",
			roles: []fabapi.FabNodeRole{fabapi.NodeRoleGateway},
			anns:  map[string]string{fabapi.AnnotationHugePages1G: "4"},
			want:  []string{"iommu=pt", "intel_iommu=on", "hugepagesz=1G", "hugepages=4"},
		},
		{
			name: "no-roles",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := &NodeInstallBuilder{
				Mode: BuildModeISO,
				Node: fabapi.FabNode{
					Annotations: tt.anns,
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
