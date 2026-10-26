// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
)

func TestMigrateVLABGatewayIfaces(t *testing.T) {
	gw := &gwapi.Gateway{Spec: gwapi.GatewaySpec{
		Driver: gwapi.GatewayDriverKernel,
		Interfaces: map[string]gwapi.GatewayInterface{
			"enp2s1": {IPs: []string{"172.30.128.1/31"}, MTU: 9000},
			"enp2s2": {IPs: []string{"172.30.128.3/31"}},
			"port3":  {PCI: "0000:00:12.0", Kernel: "enp0s18"},
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
		"port1": {PCI: "0000:00:10.0", Kernel: "enp0s16", IPs: []string{"172.30.128.1/31"}, MTU: 9000},
		"port2": {PCI: "0000:00:11.0", Kernel: "enp0s17", IPs: []string{"172.30.128.3/31"}},
		"port3": {PCI: "0000:00:12.0", Kernel: "enp0s18"},
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
