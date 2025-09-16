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
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/process"
	"github.com/vishvananda/netlink"
)

func PrepareTaps(_ context.Context, count int) error {
	if count > 0 {
		slog.Debug("Preparing taps and bridge", "count", count)
	} else {
		slog.Debug("Deleting taps and bridge")
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

	for idx := 0; idx < count; idx++ {
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

	if count > 0 {
		slog.Info("Taps and bridge are ready", "count", count)
	} else {
		slog.Info("Taps and bridge are deleted")
	}

	return nil
}

func PreparePassthrough(_ context.Context, devs []string) error {
	if len(devs) == 0 {
		return nil
	}

	slog.Debug("Preparing devices for passthrough", "devices", devs)
	cmd := exec.Command("modprobe", "vfio_pci")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot insert vfio_pci: %w", err)
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
			slog.Warn("Found stale VM process, killing it", "pid", pr.Pid)
			err = pr.KillWithContext(ctx)
			if err != nil {
				return nil, fmt.Errorf("killing stale VM process %d: %w", pr.Pid, err)
			}
		} else {
			stale = append(stale, pr.Pid)
		}
	}

	return stale, nil
}
