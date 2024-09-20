package meta

import (
	"fmt"
	"net/netip"
)

var ErrIPv4Only = fmt.Errorf("must be an IPv4")

// +kubebuilder:validation:Type=string
type Addr string

func (val Addr) Parse() (netip.Addr, error) {
	ip, err := netip.ParseAddr(string(val))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parsing addr %q: %w", val, err)
	}
	if !ip.Is4() {
		return netip.Addr{}, fmt.Errorf("parsing addr %q: %w", val, ErrIPv4Only)
	}

	return ip, nil
}

// +kubebuilder:validation:Type=string
type Prefix string

func (val Prefix) Parse() (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(string(val))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parsing prefix %q: %w", val, err)
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("parsing prefix %q: %w", val, ErrIPv4Only)
	}

	return prefix, nil
}

const (
	AddrDHCP = AddrOrDHCP("dhcp")
)

type AddrOrDHCP string

func (val AddrOrDHCP) Parse() (bool, netip.Addr, error) {
	if val == AddrDHCP {
		return true, netip.Addr{}, nil
	}

	ip, err := netip.ParseAddr(string(val))
	if err != nil {
		return false, netip.Addr{}, fmt.Errorf("parsing addr %q: %w", val, err)
	}
	if !ip.Is4() {
		return false, netip.Addr{}, fmt.Errorf("parsing addr %q: %w", val, ErrIPv4Only)
	}

	return false, ip, nil
}
