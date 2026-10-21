// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package bench

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// setDHCPBuffers sizes the relay socket's buffers past the sysctl limits,
// which root may do with the FORCE variants. The bench always runs as root
// here, since it binds port 67.
func setDHCPBuffers(conn *net.UDPConn, size int) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("getting raw socket: %w", err)
	}

	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		if sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, size); sockErr != nil {
			return
		}
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUFFORCE, size)
	}); err != nil {
		return fmt.Errorf("controlling socket: %w", err)
	}

	if sockErr != nil {
		return fmt.Errorf("setting socket buffers: %w", sockErr)
	}

	return nil
}
