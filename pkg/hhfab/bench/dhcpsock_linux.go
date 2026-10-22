// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package bench

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// setDHCPBuffers sizes the relay socket's buffers past the sysctl limits,
// which root may do with the FORCE variants. The bench always runs as root
// here, since it binds port 67; should FORCE fail anyway, the sizes the
// sysctls allow are asked for instead, and the error still says so.
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
		return errors.Join(fmt.Errorf("setting socket buffers past the sysctl limits: %w", sockErr),
			conn.SetReadBuffer(size), conn.SetWriteBuffer(size))
	}

	return nil
}

// enableDropCount has the kernel attach the socket's count of packets it
// dropped for want of buffer space to what it receives (SO_RXQ_OVFL), so a
// reply lost on the bench host isn't taken for dhcpd's silence.
func enableDropCount(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("getting raw socket: %w", err)
	}

	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1)
	}); err != nil {
		return fmt.Errorf("controlling socket: %w", err)
	}

	if sockErr != nil {
		return fmt.Errorf("enabling the drop count: %w", sockErr)
	}

	return nil
}

// socketDrops is the drop count enableDropCount asked for, from a received
// packet's control messages: the socket's total so far. The kernel only adds
// it once something was dropped.
func socketDrops(oob []byte) (uint32, bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return 0, false
	}

	for _, msg := range msgs {
		if msg.Header.Level == unix.SOL_SOCKET && msg.Header.Type == unix.SO_RXQ_OVFL && len(msg.Data) >= 4 {
			return binary.NativeEndian.Uint32(msg.Data), true
		}
	}

	return 0, false
}
