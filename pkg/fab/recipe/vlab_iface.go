// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.githedgehog.com/fabric/pkg/util/logutil"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
)

const (
	// dmiChassisAssetTagPath is where the guest sees the SMBIOS chassis asset tag
	// VLAB marks its VMs with.
	dmiChassisAssetTagPath = "/sys/class/dmi/id/chassis_asset_tag"
	sysClassNet            = "/sys/class/net"
	virtioNetDriver        = "virtio_net"
)

// isVLAB reports whether this machine is a VLAB VM.
func isVLAB() bool {
	tag, err := os.ReadFile(dmiChassisAssetTagPath)

	return err == nil && strings.TrimSpace(string(tag)) == flatcar.VLABAssetTag
}

// ensureVLABIfaceNames gives a VLAB VM's virtio NICs the names the VLAB naming
// rule gives them at boot - their PCI path names, as e1000 NICs had - if they
// came up without it. That happens on the first boot of a VM that moved from
// e1000 to virtio-net before the rule was installed, which is how VLAB
// upgrades run: the VMs restart on the new hhfab first, then the upgrade that
// installs the rule runs. Everything from the address checks to k3s looks the
// management interface up by name, so the rule is installed for later boots and
// the interfaces are renamed in place now. Anything but a VLAB VM is untouched.
//
// TODO remove this hack, keeping only the naming rule itself, once upgrades are
// only supported from 26.05 onwards: every node then already has the rule from
// its install or a previous upgrade before its VM ever restarts on virtio-net.
func ensureVLABIfaceNames(ctx context.Context) error {
	if !isVLAB() {
		return nil
	}

	if err := installVLABVirtioNames(); err != nil {
		return err
	}

	ifaces, err := os.ReadDir(sysClassNet)
	if err != nil {
		return fmt.Errorf("listing interfaces: %w", err)
	}

	for _, iface := range ifaces {
		current := iface.Name()

		driver, err := os.Readlink(filepath.Join(sysClassNet, current, "device", "driver"))
		if err != nil || filepath.Base(driver) != virtioNetDriver {
			continue
		}

		props, err := exec.CommandContext(ctx, "udevadm", "info", "--query=property", filepath.Join(sysClassNet, current)).Output() //nolint:gosec // interface name from sysfs
		if err != nil {
			return fmt.Errorf("getting udev properties of %s: %w", current, err)
		}

		name := udevProperty(props, "ID_NET_NAME_PATH")
		if name == "" || name == current {
			continue
		}

		if err := renameIface(ctx, current, name); err != nil {
			return err
		}
	}

	return nil
}

// renameIface renames an interface in place, keeping its networkd config.
func renameIface(ctx context.Context, current, name string) error {
	// Only primary names count: the path name is usually an alternative name of
	// this very interface, which the first step below drops. Another interface
	// already called that would make the rename fail with this one already down.
	if _, err := net.InterfaceByName(name); err == nil {
		return fmt.Errorf("renaming %s to %s: another interface is already named %s", current, name, name) //nolint:err113
	}

	slog.Info("Renaming VLAB interface to its PCI path name", "from", current, "to", name)

	for _, args := range [][]string{
		// the kernel refuses a name that is already an alternative name, which
		// the path name usually is; failing to drop one that is not is fine
		{"-", "ip", "link", "property", "del", "dev", current, "altname", name},
		{"ip", "link", "set", "dev", current, "down"},
		{"ip", "link", "set", "dev", current, "name", name},
		{"ip", "link", "set", "dev", name, "up"},
		{"networkctl", "reconfigure", name},
	} {
		optional := args[0] == "-"
		if optional {
			args = args[1:]
		}

		cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // fixed commands, names from sysfs and udev
		cmd.Stdout = logutil.NewSink(ctx, slog.Debug, args[0]+": ")
		cmd.Stderr = logutil.NewSink(ctx, slog.Debug, args[0]+": ")

		if err := cmd.Run(); err != nil && !optional {
			return fmt.Errorf("renaming %s to %s: running %s: %w", current, name, strings.Join(args, " "), err)
		}
	}

	return nil
}

// udevProperty returns the value of key in `udevadm info --query=property`
// output, or an empty string if it is not there.
func udevProperty(props []byte, key string) string {
	scanner := bufio.NewScanner(bytes.NewReader(props))
	for scanner.Scan() {
		if k, v, ok := strings.Cut(scanner.Text(), "="); ok && k == key {
			return v
		}
	}

	return ""
}
