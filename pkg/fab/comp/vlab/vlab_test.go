// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package vlab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootPortNICs(t *testing.T) {
	require.Equal(t, "0000:05:00.0", RootPortPCI(0))
	require.Equal(t, "enp5s0", RootPortKernel(0))
	// PCI bus is hex while the kernel name has it in decimal
	require.Equal(t, "0000:13:00.0", RootPortPCI(MaxRootPorts-1))
	require.Equal(t, "enp19s0", RootPortKernel(MaxRootPorts-1))
}

func TestGatewayNICs(t *testing.T) {
	require.Equal(t, "0000:02:00.0", GatewayPCI(0))
	require.Equal(t, "enp2s0", GatewayKernel(0))
	require.Equal(t, "0000:05:00.0", GatewayPCI(1))
	require.Equal(t, "enp5s0", GatewayKernel(1))
	require.Equal(t, "0000:06:00.0", GatewayPCI(2))
	require.Equal(t, "enp6s0", GatewayKernel(2))
}
