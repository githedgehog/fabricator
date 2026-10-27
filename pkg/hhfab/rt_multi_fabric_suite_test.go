// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
)

func rtExternal(name, fabric, peer string) vpcapi.External {
	ext := vpcapi.External{
		Name: name, Annotations: map[string]string{},
		Spec: vpcapi.ExternalSpec{Topology: vpcapi.ExternalTopology{Fabric: fabric}},
	}
	if peer != "" {
		ext.Annotations[VLABInterconnectAnnotation] = peer
	}

	return ext
}

func TestFindRTInterconnectPairs(t *testing.T) {
	externals := []vpcapi.External{
		rtExternal("to-def-2", "fabric-b", "to-fb-2"),
		rtExternal("to-fb-2", "default", "to-def-2"),
		rtExternal("to-fabric-b", "default", "to-default"),
		rtExternal("to-default", "fabric-b", "to-fabric-b"),
		rtExternal("internet", "default", ""),
		rtExternal("lonely", "default", "missing"),
	}

	pairs := findRTInterconnectPairs(externals)
	require.Len(t, pairs, 2)
	require.Equal(t, 0, pairs[0].ordinal)
	require.Equal(t, "to-fabric-b", pairs[0].sides[0].Name)
	require.Equal(t, "to-default", pairs[0].sides[1].Name)
	require.Equal(t, 1, pairs[1].ordinal)
	require.Equal(t, "to-fb-2", pairs[1].sides[0].Name)
	require.Equal(t, "to-def-2", pairs[1].sides[1].Name)
}
