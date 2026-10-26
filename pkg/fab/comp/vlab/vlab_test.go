// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package vlab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayNICs(t *testing.T) {
	require.Equal(t, "0000:02:00.0", GatewayPCI(0))
	require.Equal(t, "enp2s0", GatewayKernel(0))
	require.Equal(t, "0000:00:10.0", GatewayPCI(1))
	require.Equal(t, "enp0s16", GatewayKernel(1))
	require.Equal(t, "0000:00:1e.0", GatewayPCI(GatewayMaxNICs))
	require.Equal(t, "enp0s30", GatewayKernel(GatewayMaxNICs))
}
