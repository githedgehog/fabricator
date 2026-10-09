// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// Package dhcpload emulates leaf DHCP relays (giaddr + option 82 circuit-id/VSS) in front of many DHCP clients to
// load test fabric-dhcpd.
package dhcpload

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const (
	LayoutRail = "rail" // one subnet per NIC index
	LayoutFlat = "flat" // one subnet for all clients
	LayoutLeaf = "leaf" // one subnet per leaf x NIC

	RetriesPXE = "pxe" // 4, 8, 16, 32s
	RetriesRFC = "rfc" // 4, 8, 16, 32, 64s +-1s
)

type Config struct {
	// Server is the fabric-dhcpd address packets are sent to
	Server netip.AddrPort
	// RelayBase is the first emulated leaf relay IP (giaddr), leaves use consecutive IPs, all must be local addresses
	RelayBase netip.Addr
	// RelayPort is the UDP port relays listen on, dhcpd always replies to giaddr:67
	RelayPort int
	Leaves    int
	Servers   int
	// NICs is the number of DHCP clients per server
	NICs   int
	Layout string
	// VPC is sent as VSS sub-option 151 VrfV<VPC>
	VPC string
	// VLANBase is the VLAN of subnet 0, sent as circuit-id Vlan<n>
	VLANBase int
	// CIDRBase and LeaseTime are used to build the VPC: subnet i gets the /20 at CIDRBase + i*4096
	CIDRBase  netip.Addr
	LeaseTime int
	// Ramp spreads client start times uniformly over this window
	Ramp    time.Duration
	Retries string
	// MaxAttempts is the max transmissions per DISCOVER/REQUEST exchange, 0 = length of the retry schedule
	MaxAttempts int
	// Duration is the total run time incl. renewals, 0 = stop once every client is bound or gave up
	Duration time.Duration
	// RenewEvery overrides the renewal interval (default T1 from the ACK)
	RenewEvery time.Duration
	// Release sends RELEASE for every bound client on exit
	Release     bool
	VendorClass string
	// PXE requests TFTP server name and bootfile (options 66/67)
	PXE bool
	// RemoteID is the option 82 remote-id sub-option, empty = omitted
	RemoteID string
	// CSVPath is where per-client results are written, empty = not written
	CSVPath  string
	Progress time.Duration
	Seed     uint64
	// Summary receives the final report, defaults to stdout
	Summary io.Writer
}

func DefaultConfig() *Config {
	return &Config{
		RelayBase: netip.MustParseAddr("172.30.3.1"),
		RelayPort: 67,
		Leaves:    32,
		Servers:   500,
		NICs:      8,
		Layout:    LayoutRail,
		VPC:       "loadtest",
		VLANBase:  1000,
		CIDRBase:  netip.MustParseAddr("10.0.128.0"),
		LeaseTime: 3600,
		Ramp:      60 * time.Second,
		Retries:   RetriesPXE,
		Progress:  5 * time.Second,
		Seed:      1,
	}
}

func (c *Config) Validate() error {
	if !c.RelayBase.Is4() || !c.CIDRBase.Is4() {
		return errors.New("relay base and CIDR base must be IPv4") //nolint:err113
	}
	if c.Leaves < 1 || c.Servers < 1 || c.NICs < 1 {
		return errors.New("leaves, servers and NICs must be >= 1") //nolint:err113
	}
	if !slices.Contains([]string{LayoutRail, LayoutFlat, LayoutLeaf}, c.Layout) {
		return fmt.Errorf("invalid layout %q", c.Layout) //nolint:err113
	}
	if !slices.Contains([]string{RetriesPXE, RetriesRFC}, c.Retries) {
		return fmt.Errorf("invalid retries %q", c.Retries) //nolint:err113
	}
	if c.Progress <= 0 {
		c.Progress = 5 * time.Second
	}

	return nil
}

// VPCName is the name of the VPC object, which is also the VRF name dhcpd matches subnets by (lowercased)
func (c *Config) VPCName() string {
	return strings.ToLower(c.VPC)
}

// RelayIPs returns the giaddr of every emulated leaf, they all have to be configured locally before Run
func (c *Config) RelayIPs() []netip.Addr {
	res := make([]netip.Addr, c.Leaves)
	for i := range c.Leaves {
		res[i] = c.relayIP(i)
	}

	return res
}

// Result summarizes a run, OK means every client bound, no renewal failed and no IP was handed out twice
type Result struct {
	Clients      int
	Bound        int
	Failed       int
	RenewsFailed int
	DuplicateIPs int
	OK           bool
}
