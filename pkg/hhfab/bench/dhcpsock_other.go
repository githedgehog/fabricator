// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package bench

import (
	"fmt"
	"net"
)

// setDHCPBuffers asks for larger buffers the portable way, which the system
// caps; only Linux, where VLAB runs, can go past that.
func setDHCPBuffers(conn *net.UDPConn, size int) error {
	if err := conn.SetReadBuffer(size); err != nil {
		return fmt.Errorf("setting read buffer: %w", err)
	}
	if err := conn.SetWriteBuffer(size); err != nil {
		return fmt.Errorf("setting write buffer: %w", err)
	}

	return nil
}
