// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/process"
	"github.com/vishvananda/netlink"
)

// tuntap values netlink only defines on Linux
const (
	tuntapModeTap    = netlink.TuntapMode(0x2)    // IFF_TAP
	tuntapMultiQueue = netlink.TuntapFlag(0x100)  // IFF_MULTI_QUEUE
	tuntapNoPI       = netlink.TuntapFlag(0x1000) // IFF_NO_PI
)

// disableIPv6 keeps the host off the VMs' links, an IPv6 link-local address has it sending ND and MLD into them and
// accepting router advertisements from the switches
func disableIPv6(iface string) error {
	path := filepath.Join("/proc/sys/net/ipv6/conf", iface, "disable_ipv6")
	if err := os.WriteFile(path, []byte("1"), 0o644); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // procfs
		return fmt.Errorf("disabling IPv6 on %q: %w", iface, err)
	}

	return nil
}

func isVLABNetLink(name string) bool {
	return strings.HasPrefix(name, VLABMgmt) || strings.HasPrefix(name, VLABLink) ||
		name == VLABLegacyMgmt || strings.HasPrefix(name, VLABLegacyMgmtTap)
}

// SetupVLABNet makes the host match the nets given: creates the missing bridges and taps, recreates the ones set up
// differently and deletes all the others VLAB owns, the management network's legacy names included
func SetupVLABNet(_ context.Context, nets []VLABNet) error {
	slog.Debug("Setting up VLAB networks", "nets", len(nets))

	bridges := map[string]VLABNet{}
	taps := map[string]bool{} // whether each tap is multi-queue
	for _, n := range nets {
		bridges[n.Name] = n
		for idx := range n.Taps {
			taps[n.TapName(idx)] = n.Queues[idx] > 1
		}
	}

	links, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("listing links: %w", err)
	}

	existing := map[string]netlink.Link{}
	for _, link := range links {
		name := link.Attrs().Name
		if !isVLABNetLink(name) {
			continue
		}

		keep := false
		switch link := link.(type) {
		case *netlink.Bridge:
			_, ok := bridges[name]
			keep = ok && (link.MulticastSnooping == nil || !*link.MulticastSnooping)
		case *netlink.Tuntap:
			multiQueue, ok := taps[name]
			keep = ok && (link.Flags&tuntapMultiQueue != 0) == multiQueue
		}
		if keep {
			existing[name] = link

			continue
		}

		slog.Debug("Deleting no more needed or differently set up link", "name", name, "type", link.Type())
		if err := netlink.LinkDel(link); err != nil {
			return fmt.Errorf("deleting %q: %w", name, err)
		}
	}

	for _, n := range nets {
		br, exist := existing[n.Name]
		if !exist {
			slog.Debug("Creating bridge", "name", n.Name)

			la := netlink.NewLinkAttrs()
			la.Name = n.Name
			// snooping drops the multicast it hasn't seen joined, VMs are to get it all as if wired directly
			bridge := &netlink.Bridge{LinkAttrs: la, MulticastSnooping: new(false)}
			if err := netlink.LinkAdd(bridge); err != nil {
				return fmt.Errorf("adding bridge %q: %w", n.Name, err)
			}
			br = bridge
		}

		if err := disableIPv6(n.Name); err != nil {
			return err
		}

		if err := netlink.LinkSetUp(br); err != nil {
			return fmt.Errorf("setting up bridge %q: %w", n.Name, err)
		}

		for idx := range n.Taps {
			name := n.TapName(idx)
			tap, exist := existing[name]
			if !exist {
				slog.Debug("Creating tap", "name", name, "queues", n.Queues[idx])

				la := netlink.NewLinkAttrs()
				la.Name = name
				tuntap := &netlink.Tuntap{LinkAttrs: la, Mode: tuntapModeTap}
				if n.Queues[idx] > 1 {
					tuntap.Flags = tuntapMultiQueue | tuntapNoPI
				}
				if err := netlink.LinkAdd(tuntap); err != nil {
					return fmt.Errorf("adding tap %q: %w", name, err)
				}
				tap = tuntap
			}

			if err := netlink.LinkSetDown(tap); err != nil {
				return fmt.Errorf("setting tap down %q: %w", name, err)
			}

			if err := disableIPv6(name); err != nil {
				return err
			}

			// the management bridge keeps the default, it carries host traffic and a bridge takes its ports' lowest MTU
			if n.Kind == VLABNetKindLink {
				if err := netlink.LinkSetMTU(tap, VLABLinkMTU); err != nil {
					return fmt.Errorf("setting tap MTU %q: %w", name, err)
				}
			}

			if err := netlink.LinkSetMaster(tap, br); err != nil {
				return fmt.Errorf("adding tap %q to %q: %w", name, n.Name, err)
			}

			if err := setGroupFwdMask(tap); err != nil {
				return err
			}

			if err := netlink.LinkSetUp(tap); err != nil {
				return fmt.Errorf("setting tap up %q: %w", name, err)
			}
		}

		for _, iface := range n.Ifaces {
			slog.Debug("Attaching to bridge", "iface", iface, "bridge", n.Name)

			link, err := netlink.LinkByName(iface)
			if err != nil {
				return fmt.Errorf("getting interface %q: %w", iface, err)
			}

			if err := netlink.LinkSetDown(link); err != nil {
				return fmt.Errorf("setting link down %q: %w", iface, err)
			}

			if err := disableIPv6(iface); err != nil {
				return err
			}

			if err := netlink.LinkSetMaster(link, br); err != nil {
				return fmt.Errorf("adding link %q to %q: %w", iface, n.Name, err)
			}

			if err := setGroupFwdMask(link); err != nil {
				return err
			}

			if err := netlink.LinkSetUp(link); err != nil {
				return fmt.Errorf("setting link up %q: %w", iface, err)
			}
		}
	}

	slog.Info("VLAB networks are ready", "nets", len(nets))

	return nil
}

func PreparePassthrough(ctx context.Context, devs []string) error {
	if len(devs) == 0 {
		return nil
	}

	slog.Debug("Preparing devices for passthrough", "devices", devs)
	cmd := exec.CommandContext(ctx, "modprobe", "vfio-pci")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot insert vfio-pci: %w", err)
	}

	for _, dev := range devs {
		var err error
		for range 6 {
			err = bindDeviceToVFIO(dev)
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("binding device %s to vfio-pci: %w", dev, err)
		}

		slog.Debug("Device is ready (bound to vfio-pci)", "device", dev)
	}

	slog.Info("Devices are ready for passthrough (bound to vfio-pci)", "count", len(devs))

	return nil
}

func bindDeviceToVFIO(dev string) error {
	vfioDevicePath := filepath.Join("/sys/bus/pci/drivers/vfio-pci", dev)
	devicePath := filepath.Join("/sys/bus/pci/devices", dev)

	vendorID, err := os.ReadFile(filepath.Join(devicePath, "vendor"))
	if err != nil {
		return fmt.Errorf("reading vendor id for %s: %w", dev, err)
	}
	deviceID, err := os.ReadFile(filepath.Join(devicePath, "device"))
	if err != nil {
		return fmt.Errorf("reading device id for %s: %w", dev, err)
	}

	if _, err := os.Stat(vfioDevicePath); err == nil {
		return nil
	}

	// unbind from current driver
	if _, err := os.Stat(filepath.Join(devicePath, "driver")); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("checking for driver for %s: %w", dev, err)
		}
	} else {
		file, err := os.OpenFile(filepath.Join(devicePath, "driver", "unbind"), os.O_WRONLY, 0o200)
		if err != nil {
			return fmt.Errorf("opening file to unbind driver for %s: %w", dev, err)
		}
		defer file.Close()

		if _, err := file.WriteString(dev); err != nil {
			return fmt.Errorf("writing to file to unbind driver for %s: %w", dev, err)
		}
	}

	file, err := os.OpenFile("/sys/bus/pci/drivers/vfio-pci/new_id", os.O_WRONLY, 0o200)
	if err != nil {
		return fmt.Errorf("opening new_id file to bind to vfio-pci for %s: %w", dev, err)
	}
	defer file.Close()

	if _, err := file.WriteString(string(vendorID) + " " + string(deviceID)); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("writing to new_id file to bind to vfio-pci for %s: %w", dev, err)
		}
	}

	file, err = os.OpenFile("/sys/bus/pci/drivers/vfio-pci/bind", os.O_WRONLY, 0o200)
	if err != nil {
		return fmt.Errorf("opening bind file to bind to vfio-pci for %s: %w", dev, err)
	}
	defer file.Close()

	if _, err := file.WriteString(dev); err != nil {
		return fmt.Errorf("writing to bind file to bind to vfio-pci for %s: %w", dev, err)
	}

	if _, err := os.Stat(vfioDevicePath); err != nil {
		return fmt.Errorf("%s is still not bound to vfio-pci", dev) //nolint:err113
	}

	return nil
}

func isDeviceBoundToVFIO(dev string) bool {
	vfioDevicePath := filepath.Join("/sys/bus/pci/drivers/vfio-pci", dev)

	_, err := os.Stat(vfioDevicePath)

	return err == nil
}

func CheckStaleVMs(ctx context.Context, kill bool) ([]int32, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting processes: %w", err)
	}

	stale := []int32{}
	for _, pr := range processes {
		cmd, err := pr.CmdlineSliceWithContext(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "no such file or directory") {
				continue
			}

			return nil, fmt.Errorf("getting process cmdline: %w", err)
		}

		// only one instance of VLAB supported at the same time
		if len(cmd) < 6 || cmd[0] != "qemu-system-x86_64" || cmd[1] != "-name" || cmd[3] != "-uuid" {
			continue
		}

		if !strings.HasPrefix(cmd[4], VLABUUIDPrefix) {
			continue
		}

		if kill {
			slog.Warn("Found stale VM process, killing it", "pid", pr.Pid, "vm", cmd[2], "uuid", cmd[4])
			err = pr.KillWithContext(ctx)
			if err != nil {
				return nil, fmt.Errorf("killing stale VM process %d: %w", pr.Pid, err)
			}
			stale = append(stale, pr.Pid)
		} else {
			stale = append(stale, pr.Pid)
		}
	}

	// Wait for killed processes to fully terminate and release locks
	if kill && len(stale) > 0 {
		slog.Info("Waiting for killed VMs to fully terminate and release locks", "count", len(stale), "pids", stale)
		maxWaitTime := 5 * time.Second
		checkInterval := 100 * time.Millisecond
		maxAttempts := int(maxWaitTime / checkInterval)
		start := time.Now()

		for attempt := range maxAttempts {
			allGone := true
			remaining := []int32{}
			for _, pid := range stale {
				exists, err := process.PidExistsWithContext(ctx, pid)
				if err != nil {
					slog.Warn("Error checking if VM process exists, assuming it still exists", "pid", pid, "error", err)
					allGone = false
					remaining = append(remaining, pid)

					continue
				}
				if exists {
					allGone = false
					remaining = append(remaining, pid)
				}
			}
			if allGone {
				slog.Debug("All killed VM processes terminated", "elapsed", time.Since(start))

				break
			}
			if attempt == maxAttempts-1 {
				slog.Warn("Some VM processes did not terminate within timeout", "remaining_pids", remaining, "timeout", maxWaitTime)
			}
			time.Sleep(checkInterval)
		}

		// Extra time for QEMU to release file locks after process termination
		extraWait := 1 * time.Second
		slog.Debug("Waiting extra time for file lock release", "duration", extraWait)
		time.Sleep(extraWait)
	}

	return stale, nil
}
