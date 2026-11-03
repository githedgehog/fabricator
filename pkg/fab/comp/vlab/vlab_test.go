// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package vlab

import (
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

func TestRootPortNICs(t *testing.T) {
	require.Equal(t, "0000:05:00.0", RootPortPCI(0))
	require.Equal(t, "enp5s0", RootPortKernel(0))
	// PCI bus is hex while the kernel name has it in decimal
	require.Equal(t, "0000:0c:00.0", RootPortPCI(MaxRootPorts-1))
	require.Equal(t, "enp12s0", RootPortKernel(MaxRootPorts-1))
}

func TestSetGatewayHugePages(t *testing.T) {
	const hp1G, hp2M = fabapi.AnnotationHugePages1G, fabapi.AnnotationHugePages2M

	for _, tt := range []struct {
		name      string
		node      map[string]string
		gw        map[string]string
		noGW      bool
		want      map[string]string
		nodeDirty bool
		gwDirty   bool
	}{
		{
			name:      "defaults",
			want:      map[string]string{hp1G: "4"},
			nodeDirty: true,
			gwDirty:   true,
		},
		{
			name:    "from-node",
			node:    map[string]string{hp1G: "8", hp2M: "512"},
			want:    map[string]string{hp1G: "8", hp2M: "512"},
			gwDirty: true,
		},
		{
			name:      "from-gateway",
			gw:        map[string]string{hp1G: "6", hp2M: "1024"},
			want:      map[string]string{hp1G: "6", hp2M: "1024"},
			nodeDirty: true,
		},
		{
			name:      "mixed",
			node:      map[string]string{hp2M: "512"},
			gw:        map[string]string{hp1G: "2"},
			want:      map[string]string{hp1G: "2", hp2M: "512"},
			nodeDirty: true,
			gwDirty:   true,
		},
		{
			name: "set",
			node: map[string]string{hp1G: "8"},
			gw:   map[string]string{hp1G: "8"},
			want: map[string]string{hp1G: "8"},
		},
		{
			name:      "no-gateway",
			noGW:      true,
			want:      map[string]string{hp1G: "4"},
			nodeDirty: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node := &fabapi.FabNode{Annotations: tt.node}
			var gw *gwapi.Gateway
			if !tt.noGW {
				gw = &gwapi.Gateway{Annotations: tt.gw}
			}

			nodeDirty, gwDirty := SetGatewayHugePages(node, gw)
			require.Equal(t, tt.nodeDirty, nodeDirty)
			require.Equal(t, tt.gwDirty, gwDirty)
			require.Equal(t, tt.want, node.Annotations)
			if gw != nil {
				require.Equal(t, tt.want, gw.Annotations)
			}
		})
	}
}

func TestGatewayNICs(t *testing.T) {
	require.Equal(t, "0000:02:00.0", GatewayPCI(0))
	require.Equal(t, "enp2s0", GatewayKernel(0))
	require.Equal(t, "0000:05:00.0", GatewayPCI(1))
	require.Equal(t, "enp5s0", GatewayKernel(1))
	require.Equal(t, "0000:06:00.0", GatewayPCI(2))
	require.Equal(t, "enp6s0", GatewayKernel(2))
}
