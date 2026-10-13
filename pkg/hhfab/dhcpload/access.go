// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"encoding/binary"
	"errors"
	"net"
)

const (
	ethHeaderLen  = 14
	udpHeaderLen  = 8
	ipHeaderLen   = 20
	etherTypeIPv4 = 0x0800
	ipProtoUDP    = 17
)

var (
	errNotDHCPReply = errors.New("not a DHCP reply")
	broadcastMAC    = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
)

// buildFrame wraps a DHCP payload into an Ethernet broadcast frame sent from the client's MAC, 0.0.0.0:68 (or the
// client IP when renewing) to 255.255.255.255:67
func buildFrame(mac net.HardwareAddr, srcIP net.IP, payload []byte) []byte {
	frame := make([]byte, ethHeaderLen+ipHeaderLen+udpHeaderLen+len(payload))
	copy(frame[0:6], broadcastMAC)
	copy(frame[6:12], mac)
	binary.BigEndian.PutUint16(frame[12:14], etherTypeIPv4)

	ip := frame[ethHeaderLen : ethHeaderLen+ipHeaderLen]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(ipHeaderLen+udpHeaderLen+len(payload))) //nolint:gosec
	ip[8] = 64
	ip[9] = ipProtoUDP
	if v4 := srcIP.To4(); v4 != nil {
		copy(ip[12:16], v4)
	}
	copy(ip[16:20], net.IPv4bcast.To4())
	binary.BigEndian.PutUint16(ip[10:12], ipChecksum(ip))

	udp := frame[ethHeaderLen+ipHeaderLen:]
	binary.BigEndian.PutUint16(udp[0:2], 68)
	binary.BigEndian.PutUint16(udp[2:4], 67)
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpHeaderLen+len(payload))) //nolint:gosec
	copy(udp[udpHeaderLen:], payload)

	return frame
}

func ipChecksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(hdr); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(hdr[i : i+2]))
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}

	return ^uint16(sum) //nolint:gosec
}

// parseFrame returns the DHCP payload of an Ethernet frame carrying an IPv4 UDP packet to the DHCP client port
func parseFrame(frame []byte) ([]byte, error) {
	if len(frame) < ethHeaderLen+ipHeaderLen+udpHeaderLen || binary.BigEndian.Uint16(frame[12:14]) != etherTypeIPv4 {
		return nil, errNotDHCPReply
	}
	ip := frame[ethHeaderLen:]
	hdrLen := int(ip[0]&0x0f) * 4
	if ip[0]>>4 != 4 || hdrLen < ipHeaderLen || ip[9] != ipProtoUDP || len(ip) < hdrLen+udpHeaderLen {
		return nil, errNotDHCPReply
	}
	udp := ip[hdrLen:]
	if binary.BigEndian.Uint16(udp[2:4]) != 68 {
		return nil, errNotDHCPReply
	}
	n := int(binary.BigEndian.Uint16(udp[4:6]))
	if n < udpHeaderLen || n > len(udp) {
		return nil, errNotDHCPReply
	}

	return udp[udpHeaderLen:n], nil
}
