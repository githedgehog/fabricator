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
	require.Equal(t, "0000:00:06.0", GatewayPCI(1))
	require.Equal(t, "enp0s6", GatewayKernel(1))
	require.Equal(t, "0000:00:08.0", GatewayPCI(2))
	require.Equal(t, "enp0s8", GatewayKernel(2))
	require.Equal(t, "0000:00:0c.0", GatewayPCI(GatewayMaxNICs))
	require.Equal(t, "enp0s12", GatewayKernel(GatewayMaxNICs))
}

// igb VFs sit at PF devfn + 0x80 with stride 2 (8 VFs), so a PF in slot s uses slots s+16 and s+17 for its VFs.
// QEMU 10.x rejects a VF that overflows the bus (VF slot 31 is the q35 LPC device) or overlaps another PF's VFs.
func TestGatewayNICSlotsFitIGBVFs(t *testing.T) {
	used := map[int]uint8{}
	for nicID := uint8(1); nicID <= GatewayMaxNICs; nicID++ {
		pf := int(GatewayNICSlot(nicID))
		require.Greater(t, pf, 5, "slots 2-5 hold the fixed VM devices, NIC %d", nicID)
		for _, slot := range []int{pf, pf + 16, pf + 17} {
			require.Less(t, slot, 31, "NIC %d uses slot %d, 31 is the q35 LPC device", nicID, slot)
			other, taken := used[slot]
			require.False(t, taken, "NIC %d uses slot %d already used by NIC %d", nicID, slot, other)
			used[slot] = nicID
		}
	}
}
