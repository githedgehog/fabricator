// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package bench

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Replies the kernel drops with the socket's buffer full are counted: the next
// packet read carries the socket's running total.
func TestSocketDropsCounted(t *testing.T) {
	t.Parallel()

	recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer recv.Close()

	require.NoError(t, recv.SetReadBuffer(4096))
	require.NoError(t, enableDropCount(recv))

	send, err := net.DialUDP("udp4", nil, recv.LocalAddr().(*net.UDPAddr)) //nolint:forcetypeassert // a UDP listener's address
	require.NoError(t, err)
	defer send.Close()

	// Far more than the buffer holds, so most are dropped.
	payload := make([]byte, 512)
	for range 200 {
		_, _ = send.Write(payload)
	}

	buf, oob := make([]byte, 2048), make([]byte, 128)

	// What was queued before the drops says nothing of them.
	for {
		require.NoError(t, recv.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
		if _, _, _, _, err := recv.ReadMsgUDPAddrPort(buf, oob); err != nil {
			break
		}
	}

	_, err = send.Write(payload)
	require.NoError(t, err)

	require.NoError(t, recv.SetReadDeadline(time.Now().Add(time.Second)))
	_, oobn, _, _, err := recv.ReadMsgUDPAddrPort(buf, oob) //nolint:dogsled // only the control message length is needed
	require.NoError(t, err)

	drops, ok := socketDrops(oob[:oobn])
	require.True(t, ok, "the kernel attaches the count once something was dropped")
	require.Positive(t, drops)
}
