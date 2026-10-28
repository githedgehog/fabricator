// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package hhfab

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

// groupFwdMask makes a bridge port pass the link-local frames (STP, LACP, LLDP and the rest) a bridge keeps to
// itself by default, so VMs get them as if wired directly, all but the pause frames the kernel never forwards
const groupFwdMask = 0xfffd

// setGroupFwdMask sets the mask over netlink rather than sysfs, which containers (e.g. CI runners) may mount read-only
func setGroupFwdMask(port netlink.Link) error {
	if err := netlink.LinkSetBRSlaveGroupFwdMask(port, groupFwdMask); err != nil {
		return fmt.Errorf("setting group forward mask of %q: %w", port.Attrs().Name, err)
	}

	return nil
}
