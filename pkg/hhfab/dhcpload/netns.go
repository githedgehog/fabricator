// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

const (
	DefaultNetNS = "dhcplg"
	hostIf       = "lg-br"
	nsIf         = "lg-ns"
)

// NetNS is a network namespace holding the relay IPs, attached to a host bridge through a veth pair
type NetNS struct {
	Name string
}

// SetupNetNS creates the namespace (removing a stale one of the same name), connects it to the bridge and assigns
// the relay IPs with the prefix length of the management network.
func SetupNetNS(name, bridge string, relays []netip.Addr, bits int) (*NetNS, error) {
	br, err := netlink.LinkByName(bridge)
	if err != nil {
		return nil, fmt.Errorf("getting bridge %s (is VLAB running?): %w", bridge, err)
	}

	n := &NetNS{Name: name}
	if err := n.Cleanup(); err != nil {
		return nil, fmt.Errorf("removing stale netns: %w", err)
	}

	handle, err := newNamed(name)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: hostIf}, PeerName: nsIf}
	if err := netlink.LinkAdd(veth); err != nil {
		_ = n.Cleanup()

		return nil, fmt.Errorf("creating veth: %w", err)
	}
	if err := setupLinks(handle, br, relays, bits); err != nil {
		_ = n.Cleanup()

		return nil, err
	}

	return n, nil
}

func setupLinks(handle netns.NsHandle, br netlink.Link, relays []netip.Addr, bits int) error {
	host, err := netlink.LinkByName(hostIf)
	if err != nil {
		return fmt.Errorf("getting %s: %w", hostIf, err)
	}
	peer, err := netlink.LinkByName(nsIf)
	if err != nil {
		return fmt.Errorf("getting %s: %w", nsIf, err)
	}
	if err := netlink.LinkSetMaster(host, br); err != nil {
		return fmt.Errorf("attaching %s to bridge: %w", hostIf, err)
	}
	if err := netlink.LinkSetUp(host); err != nil {
		return fmt.Errorf("setting %s up: %w", hostIf, err)
	}
	if err := netlink.LinkSetNsFd(peer, int(handle)); err != nil {
		return fmt.Errorf("moving %s into netns: %w", nsIf, err)
	}

	nh, err := netlink.NewHandleAt(handle)
	if err != nil {
		return fmt.Errorf("opening netns handle: %w", err)
	}
	defer nh.Close()

	if lo, err := nh.LinkByName("lo"); err == nil {
		_ = nh.LinkSetUp(lo)
	}
	peer, err = nh.LinkByName(nsIf)
	if err != nil {
		return fmt.Errorf("getting %s in netns: %w", nsIf, err)
	}
	if err := nh.LinkSetUp(peer); err != nil {
		return fmt.Errorf("setting %s up: %w", nsIf, err)
	}
	for _, ip := range relays {
		addr := &netlink.Addr{IPNet: &net.IPNet{IP: ip.AsSlice(), Mask: net.CIDRMask(bits, 32)}}
		if err := nh.AddrAdd(peer, addr); err != nil {
			return fmt.Errorf("adding %s: %w", ip, err)
		}
	}

	return nil
}

// newNamed creates the namespace without leaving the calling thread in it, NewNamed switches it
func newNamed(name string) (netns.NsHandle, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		return 0, fmt.Errorf("getting current netns: %w", err)
	}
	defer orig.Close()

	handle, err := netns.NewNamed(name)
	if err != nil {
		return 0, fmt.Errorf("creating netns %s: %w", name, err)
	}
	if err := netns.Set(orig); err != nil {
		handle.Close()

		return 0, fmt.Errorf("leaving netns %s: %w", name, err)
	}

	return handle, nil
}

// Do runs fn on a thread inside the namespace
func (n *NetNS) Do(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		return fmt.Errorf("getting current netns: %w", err)
	}
	defer orig.Close()

	target, err := netns.GetFromName(n.Name)
	if err != nil {
		return fmt.Errorf("getting netns %s: %w", n.Name, err)
	}
	defer target.Close()

	if err := netns.Set(target); err != nil {
		return fmt.Errorf("entering netns %s: %w", n.Name, err)
	}
	defer func() { _ = netns.Set(orig) }()

	return fn()
}

// CheckReachable makes sure a TCP connection from the namespace to the control node works
func (n *NetNS) CheckReachable(ctx context.Context, addr netip.AddrPort, timeout time.Duration) error {
	return n.Do(func() error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr.String())
		if err != nil {
			return fmt.Errorf("reaching %s from netns %s: %w", addr, n.Name, err)
		}

		return conn.Close()
	})
}

// Cleanup removes the namespace and its veth pair, it's fine if they don't exist
func (n *NetNS) Cleanup() error {
	var errs []error
	if link, err := netlink.LinkByName(hostIf); err == nil {
		if err := netlink.LinkDel(link); err != nil {
			errs = append(errs, fmt.Errorf("deleting %s: %w", hostIf, err))
		}
	}
	if h, err := netns.GetFromName(n.Name); err == nil {
		h.Close()
		if err := netns.DeleteNamed(n.Name); err != nil {
			errs = append(errs, fmt.Errorf("deleting netns %s: %w", n.Name, err))
		}
	}

	return errors.Join(errs...)
}
