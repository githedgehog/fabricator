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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/process"
	"github.com/vishvananda/netlink"
)

func SetupOOBMgmtNet(_ context.Context, count int, includeIfaces []string) error {
	if count > 0 {
		slog.Debug("Preparing OOB Mgmt Net", "bridge", VLABBridge, "taps", count, "include", includeIfaces)
	} else {
		slog.Debug("Removing OOB Mgmt Net", "bridge", VLABBridge)
	}

	br, err := netlink.LinkByName(VLABBridge)
	if err != nil && !errors.As(err, &netlink.LinkNotFoundError{}) {
		return fmt.Errorf("getting bridge %q: %w", VLABBridge, err)
	}

	if errors.As(err, &netlink.LinkNotFoundError{}) && count > 0 {
		slog.Debug("Creating bridge", "name", VLABBridge)

		la := netlink.NewLinkAttrs()
		la.Name = VLABBridge
		br = &netlink.Bridge{LinkAttrs: la}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("adding bridge %q: %w", VLABBridge, err)
		}
	} else if !errors.As(err, &netlink.LinkNotFoundError{}) && count == 0 {
		slog.Debug("Deleting bridge", "name", VLABBridge)

		if err := netlink.LinkDel(br); err != nil {
			return fmt.Errorf("deleting bridge %q: %w", VLABBridge, err)
		}
	}

	if count > 0 {
		if err := netlink.LinkSetUp(br); err != nil {
			return fmt.Errorf("setting up bridge %q: %w", VLABBridge, err)
		}
	}

	links, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("listing links: %w", err)
	}

	existing := map[string]netlink.Link{}
	for _, link := range links {
		if link.Type() != "tuntap" {
			continue
		}
		name := link.Attrs().Name
		if !strings.HasPrefix(name, VLABTapPrefix) {
			continue
		}

		tapID, err := strconv.Atoi(name[len(VLABTapPrefix):])
		if err != nil {
			return fmt.Errorf("parsing tap ID: %w", err)
		}

		if tapID >= count {
			slog.Debug("Deleting no more needed tap", "name", name)

			if err := netlink.LinkDel(link); err != nil {
				return fmt.Errorf("deleting tap %q: %w", name, err)
			}
		}

		existing[name] = link
	}

	for idx := range count {
		name := fmt.Sprintf("%s%d", VLABTapPrefix, idx)
		tap, exist := existing[name]
		if !exist {
			slog.Debug("Creating tap", "name", name)

			la := netlink.NewLinkAttrs()
			la.Name = name
			tap = &netlink.Tuntap{
				LinkAttrs: la,
				Mode:      0x2, // netlink.TUNTAP_MODE_TAP
			}
			if err := netlink.LinkAdd(tap); err != nil {
				return fmt.Errorf("adding tap %q: %w", name, err)
			}
		}

		if err := netlink.LinkSetDown(tap); err != nil {
			return fmt.Errorf("setting tap down %q: %w", name, err)
		}

		if err := netlink.LinkSetMaster(tap, br); err != nil {
			return fmt.Errorf("adding tap %q to %q: %w", name, VLABBridge, err)
		}

		if err := netlink.LinkSetUp(tap); err != nil {
			return fmt.Errorf("setting tap up %q: %w", name, err)
		}
	}

	included := map[string]bool{}
	for _, link := range links {
		name := link.Attrs().Name
		if !slices.Contains(includeIfaces, name) {
			continue
		}

		slog.Debug("Attaching to bridge", "iface", name)

		if err := netlink.LinkSetDown(link); err != nil {
			return fmt.Errorf("setting link down %q: %w", name, err)
		}

		if err := netlink.LinkSetMaster(link, br); err != nil {
			return fmt.Errorf("adding link %q to %q: %w", name, VLABBridge, err)
		}

		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("setting link up %q: %w", name, err)
		}

		included[name] = true
	}

	for _, iface := range includeIfaces {
		if !included[iface] {
			return fmt.Errorf("interface %q not found", iface) //nolint:err113
		}
	}

	if count > 0 {
		slog.Info("OOB Mgmt Net is ready", "bridge", VLABBridge, "taps", count, "included", includeIfaces)
	} else {
		slog.Info("OOB Mgmt Net is removed", "bridge", VLABBridge)
	}

	return nil
}

func PreparePassthrough(_ context.Context, devs []string) error {
	if len(devs) == 0 {
		return nil
	}

	slog.Debug("Preparing devices for passthrough", "devices", devs)
	cmd := exec.Command("modprobe", "vfio-pci")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot insert vfio-pci: %w", err)
	}

	for _, dev := range devs {
		var err error
		for attempt := 0; attempt < 6; attempt++ {
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
		return fmt.Errorf("%s is still not bound to vfio-pci", dev) //nolint:goerr113
	}

	return nil
}

func isDeviceBoundToVFIO(dev string) bool {
	vfioDevicePath := filepath.Join("/sys/bus/pci/drivers/vfio-pci", dev)

	_, err := os.Stat(vfioDevicePath)

	return err == nil
}

const (
	HugePages1GDir     = "/sys/kernel/mm/hugepages/hugepages-1048576kB"
	HugePagesNrFile    = "nr_hugepages"
	HugePagesFreeFile  = "free_hugepages"
	CompactMemoryFile  = "/proc/sys/vm/compact_memory"
	HugePagesAttempts  = 3
	HugePagesRetryWait = 2 * time.Second
)

// PrepareHugePages makes sure the 1G huge pages pool has at least count pages in total. If it's smaller, it compacts
// memory, requests the missing pages and checks the resulting pool size, retrying a few times as the kernel can
// satisfy such a request only partially. It never shrinks the pool.
func PrepareHugePages(ctx context.Context, count uint) error {
	if count == 0 {
		return nil
	}

	if _, err := os.Stat(HugePages1GDir); err != nil {
		return fmt.Errorf("1G huge pages aren't supported (checking %q): %w", HugePages1GDir, err)
	}

	nr, err := readHugePages(HugePagesNrFile)
	if err != nil {
		return fmt.Errorf("getting huge pages pool size: %w", err)
	}

	if nr >= count {
		slog.Debug("Enough 1G huge pages in the pool", "pool", nr, "needed", count)

		return nil
	}

	for attempt := range HugePagesAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("waiting to retry huge pages allocation: %w", ctx.Err())
			case <-time.After(HugePagesRetryWait):
			}
		}

		slog.Info("Not enough 1G huge pages, compacting memory and growing the pool", "pool", nr, "needed", count)

		if err := compactMemory(); err != nil {
			slog.Warn("Can't compact memory, still trying to allocate huge pages", "err", err)
		}

		if err := writeHugePages(HugePagesNrFile, count); err != nil {
			return fmt.Errorf("growing huge pages pool to %d: %w", count, err)
		}

		// kernel could have allocated only a part of what we asked for, so the actual pool size is what matters
		if nr, err = readHugePages(HugePagesNrFile); err != nil {
			return fmt.Errorf("getting huge pages pool size: %w", err)
		}

		if nr >= count {
			free, err := readHugePages(HugePagesFreeFile)
			if err != nil {
				return fmt.Errorf("getting free huge pages: %w", err)
			}

			slog.Info("1G huge pages are ready", "pool", nr, "free", free, "needed", count)

			return nil
		}
	}

	return fmt.Errorf("only %d of %d 1G huge pages allocated, free up memory or preallocate them at boot", nr, count) //nolint:err113
}

func readHugePages(name string) (uint, error) {
	path := filepath.Join(HugePages1GDir, name)

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("reading %q: %w", path, err)
	}

	val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing %q: %w", path, err)
	}

	return uint(val), nil
}

func writeHugePages(name string, val uint) error {
	path := filepath.Join(HugePages1GDir, name)

	if err := os.WriteFile(path, []byte(strconv.FormatUint(uint64(val), 10)), 0o644); err != nil {
		return fmt.Errorf("writing %q: %w", path, err)
	}

	return nil
}

func compactMemory() error {
	if err := os.WriteFile(CompactMemoryFile, []byte("1"), 0o200); err != nil {
		return fmt.Errorf("writing %q: %w", CompactMemoryFile, err)
	}

	return nil
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

		for attempt := 0; attempt < maxAttempts; attempt++ {
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
