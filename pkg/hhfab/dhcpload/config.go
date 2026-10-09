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

	// ModeRelay emulates leaf DHCP relays: packets carry giaddr and option 82 and are sent to dhcpd directly
	ModeRelay = "relay"
	// ModeAccess sends broadcast DHCP from VLAN interfaces of a server wired to a real leaf, which relays them
	ModeAccess = "access"

	RetriesPXE = "pxe" // 4, 8, 16, 32s
	RetriesRFC = "rfc" // 4, 8, 16, 32, 64s +-1s
)

type Config struct {
	// Mode is ModeRelay (default) or ModeAccess
	Mode string `json:"mode"`
	// Iface is the parent interface of the VLAN interfaces in ModeAccess
	Iface string `json:"iface"`
	// Server is the fabric-dhcpd address packets are sent to
	Server netip.AddrPort `json:"server"`
	// RelayBase is the first emulated leaf relay IP (giaddr), leaves use consecutive IPs, all must be local addresses
	RelayBase netip.Addr `json:"relayBase"`
	// RelayPort is the UDP port relays listen on, dhcpd always replies to giaddr:67
	RelayPort int `json:"relayPort"`
	Leaves    int `json:"leaves"`
	Servers   int `json:"servers"`
	// NICs is the number of DHCP clients per server
	NICs   int    `json:"nics"`
	Layout string `json:"layout"`
	// VPC is sent as VSS sub-option 151 VrfV<VPC>
	VPC string `json:"vpc"`
	// VLANBase is the VLAN of subnet 0, sent as circuit-id Vlan<n>
	VLANBase int `json:"vlanBase"`
	// CIDRBase and LeaseTime are used to build the VPC: subnet i gets the /20 at CIDRBase + i*4096
	CIDRBase  netip.Addr `json:"cidrBase"`
	LeaseTime int        `json:"leaseTime"`
	// Ramp spreads client start times uniformly over this window
	Ramp    time.Duration `json:"ramp"`
	Retries string        `json:"retries"`
	// MaxAttempts is the max transmissions per DISCOVER/REQUEST exchange, 0 = length of the retry schedule
	MaxAttempts int `json:"maxAttempts"`
	// Duration is the total run time incl. renewals, 0 = stop once every client is bound or gave up
	Duration time.Duration `json:"duration"`
	// RenewEvery overrides the renewal interval (default T1 from the ACK)
	RenewEvery time.Duration `json:"renewEvery"`
	// Release sends RELEASE for every bound client on exit
	Release     bool   `json:"release"`
	VendorClass string `json:"vendorClass"`
	// PXE requests TFTP server name and bootfile (options 66/67)
	PXE bool `json:"pxe"`
	// RemoteID is the option 82 remote-id sub-option, empty = omitted
	RemoteID string `json:"remoteID"`
	// CSVPath is where per-client results are written, empty = not written
	CSVPath  string        `json:"csvPath"`
	Progress time.Duration `json:"progress"`
	Seed     uint64        `json:"seed"`
	// Summary receives the final report, defaults to stdout
	Summary io.Writer `json:"-"`
}

func DefaultConfig() *Config {
	return &Config{
		RelayBase: netip.MustParseAddr("172.30.3.1"),
		RelayPort: 67,
		Leaves:    32,
		Servers:   500,
		NICs:      8,
		Mode:      ModeRelay,
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
	if !slices.Contains([]string{ModeRelay, ModeAccess}, c.Mode) {
		return fmt.Errorf("invalid mode %q", c.Mode) //nolint:err113
	}
	if c.Mode == ModeAccess && (c.Layout == LayoutLeaf || c.Iface == "") {
		return errors.New("access mode needs an interface and the rail or flat layout") //nolint:err113
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
