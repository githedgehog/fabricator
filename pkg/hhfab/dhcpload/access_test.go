// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/stretchr/testify/require"
)

func TestBuildFrame(t *testing.T) {
	mac := net.HardwareAddr{0x02, 0x4c, 0x54, 0, 0, 1}
	msg, err := dhcpv4.NewDiscovery(mac)
	require.NoError(t, err)
	payload := msg.ToBytes()

	frame := buildFrame(mac, net.IPv4zero, payload)
	require.Equal(t, ethHeaderLen+ipHeaderLen+udpHeaderLen+len(payload), len(frame))
	require.Equal(t, broadcastMAC, net.HardwareAddr(frame[0:6]))
	require.Equal(t, mac, net.HardwareAddr(frame[6:12]))
	require.Equal(t, uint16(0x0800), binary.BigEndian.Uint16(frame[12:14]))
	require.Equal(t, uint16(0), ipChecksum(frame[ethHeaderLen:ethHeaderLen+ipHeaderLen]), "valid IP header checksum")
	require.Equal(t, "255.255.255.255", net.IP(frame[30:34]).String())
	require.Equal(t, uint16(68), binary.BigEndian.Uint16(frame[34:36]))
	require.Equal(t, uint16(67), binary.BigEndian.Uint16(frame[36:38]))

	// renewal keeps the client IP as the source
	frame = buildFrame(mac, net.IPv4(10, 0, 128, 5), payload)
	require.Equal(t, "10.0.128.5", net.IP(frame[26:30]).String())
}

func TestParseFrame(t *testing.T) {
	mac := net.HardwareAddr{0x02, 0x4c, 0x54, 0, 0, 1}
	msg, err := dhcpv4.NewDiscovery(mac)
	require.NoError(t, err)
	payload := msg.ToBytes()

	// a reply goes from port 67 to port 68
	frame := buildFrame(mac, net.IPv4zero, payload)
	_, err = parseFrame(frame)
	require.ErrorIs(t, err, errNotDHCPReply, "requests are not replies")

	binary.BigEndian.PutUint16(frame[34:36], 67)
	binary.BigEndian.PutUint16(frame[36:38], 68)
	got, err := parseFrame(frame)
	require.NoError(t, err)
	require.Equal(t, payload, got)

	// trailing Ethernet padding is not part of the payload
	got, err = parseFrame(append(frame, 0, 0, 0, 0))
	require.NoError(t, err)
	require.Equal(t, payload, got)

	_, err = parseFrame(frame[:20])
	require.ErrorIs(t, err, errNotDHCPReply)
	frame[23] = 6 // TCP
	_, err = parseFrame(frame)
	require.ErrorIs(t, err, errNotDHCPReply)
}

func TestValidateAccess(t *testing.T) {
	c := testConfig()
	c.Mode = ModeAccess
	require.Error(t, c.Validate(), "no interface")
	c.Iface = "enp2s1"
	require.NoError(t, c.Validate())
	c.Layout = LayoutLeaf
	require.Error(t, c.Validate())
}

func TestConfigJSON(t *testing.T) {
	c := testConfig()
	c.Mode = ModeAccess
	c.Iface = "enp2s1"
	c.Ramp = 90 * time.Second
	c.CSVPath = "/tmp/out.csv"

	data, err := json.Marshal(c)
	require.NoError(t, err)

	got := DefaultConfig()
	require.NoError(t, json.Unmarshal(data, got))
	require.Equal(t, c, got)
}
