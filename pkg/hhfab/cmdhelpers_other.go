// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package hhfab

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

// setGroupFwdMask is Linux only, as is VLAB
func setGroupFwdMask(port netlink.Link) error {
	return fmt.Errorf("setting group forward mask of %q is only supported on Linux", port.Attrs().Name) //nolint:err113
}
