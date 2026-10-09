// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	ethHeaderLen = 14
	udpHeaderLen = 8
	ipHeaderLen  = 20
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
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_IP)

	ip := frame[ethHeaderLen : ethHeaderLen+ipHeaderLen]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(ipHeaderLen+udpHeaderLen+len(payload))) //nolint:gosec
	ip[8] = 64
	ip[9] = unix.IPPROTO_UDP
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
	if len(frame) < ethHeaderLen+ipHeaderLen+udpHeaderLen || binary.BigEndian.Uint16(frame[12:14]) != unix.ETH_P_IP {
		return nil, errNotDHCPReply
	}
	ip := frame[ethHeaderLen:]
	hdrLen := int(ip[0]&0x0f) * 4
	if ip[0]>>4 != 4 || hdrLen < ipHeaderLen || ip[9] != unix.IPPROTO_UDP || len(ip) < hdrLen+udpHeaderLen {
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

func vlanIfName(vlan int) string {
	return fmt.Sprintf("lg%d", vlan)
}

// vlanEndpoint is a VLAN interface of the server with a packet socket on it
type vlanEndpoint struct {
	vlan int
	link netlink.Link
	file *os.File
	rc   syscall.RawConn
	xids *sync.Map
}

func (v *vlanEndpoint) name() string       { return vlanIfName(v.vlan) }
func (v *vlanEndpoint) pending() *sync.Map { return v.xids }

func (v *vlanEndpoint) close() {
	if v.file != nil {
		_ = v.file.Close()
	}
	if v.link != nil {
		_ = netlink.LinkDel(v.link)
	}
}

func (v *vlanEndpoint) send(cl *client, msg *dhcpv4.DHCPv4) error {
	if _, err := v.file.Write(buildFrame(cl.mac, msg.ClientIPAddr, msg.ToBytes())); err != nil {
		return fmt.Errorf("writing to %s: %w", v.name(), err)
	}

	return nil
}

func (v *vlanEndpoint) serve(ctx context.Context, stats *stats) {
	buf := make([]byte, 65536)
	for ctx.Err() == nil {
		var n int
		var from unix.Sockaddr
		var recvErr error
		if err := v.rc.Read(func(fd uintptr) bool {
			n, from, recvErr = unix.Recvfrom(int(fd), buf, 0)

			return !errors.Is(recvErr, unix.EAGAIN)
		}); err != nil {
			return // closed
		}
		if recvErr != nil {
			slog.Warn("Packet read failed", "iface", v.name(), "err", recvErr)

			continue
		}
		if ll, ok := from.(*unix.SockaddrLinklayer); ok && ll.Pkttype == unix.PACKET_OUTGOING {
			continue
		}
		payload, err := parseFrame(buf[:n])
		if err != nil {
			continue
		}
		deliver(v.xids, payload, stats)
	}
}

func htons(v uint16) uint16 {
	return v<<8 | v>>8
}

func (v *vlanEndpoint) open(parent netlink.Link) error {
	name := vlanIfName(v.vlan)
	if stale, err := netlink.LinkByName(name); err == nil {
		if err := netlink.LinkDel(stale); err != nil {
			return fmt.Errorf("removing stale %s: %w", name, err)
		}
	}

	la := netlink.NewLinkAttrs()
	la.Name = name
	la.ParentIndex = parent.Attrs().Index
	link := &netlink.Vlan{LinkAttrs: la, VlanId: v.vlan}
	if err := netlink.LinkAdd(link); err != nil {
		return fmt.Errorf("adding %s: %w", name, err)
	}
	v.link = link
	if err := netlink.SetPromiscOn(link); err != nil {
		return fmt.Errorf("setting %s promiscuous: %w", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("setting %s up: %w", name, err)
	}
	idx := link.Attrs().Index
	if l, err := netlink.LinkByName(name); err == nil {
		idx = l.Attrs().Index
	}

	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, int(htons(unix.ETH_P_IP)))
	if err != nil {
		return fmt.Errorf("opening packet socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_IP), Ifindex: idx}); err != nil {
		_ = unix.Close(fd)

		return fmt.Errorf("binding packet socket to %s: %w", name, err)
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 8<<20); err != nil {
		slog.Warn("Setting socket read buffer", "err", err)
	}
	v.file = os.NewFile(uintptr(fd), name)
	if v.rc, err = v.file.SyscallConn(); err != nil {
		return fmt.Errorf("getting raw conn: %w", err)
	}

	return nil
}

// openAccessEndpoints creates a VLAN interface on c.Iface for every subnet, the leaf port the interface is wired to has
// to have the VPC subnets attached with their VLANs tagged
func (c *Config) openAccessEndpoints(ctx context.Context, st *stats) ([]endpoint, error) {
	if c.Iface == "" {
		return nil, errors.New("access mode needs an interface") //nolint:err113
	}
	parent, err := netlink.LinkByName(c.Iface)
	if err != nil {
		return nil, fmt.Errorf("getting interface %s: %w", c.Iface, err)
	}
	if err := netlink.LinkSetUp(parent); err != nil {
		return nil, fmt.Errorf("setting %s up: %w", c.Iface, err)
	}
	// replies to the client MACs are unicast unless the relay honors the broadcast flag
	if err := netlink.SetPromiscOn(parent); err != nil {
		return nil, fmt.Errorf("setting %s promiscuous: %w", c.Iface, err)
	}

	xids := &sync.Map{}
	eps := make([]endpoint, c.NumSubnets())
	for i := range eps {
		v := &vlanEndpoint{vlan: c.subnet(i).vlan, xids: xids}
		eps[i] = v
		if err := v.open(parent); err != nil {
			return eps, err
		}
		go v.serve(ctx, st)
	}

	return eps, nil
}
