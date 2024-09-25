package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
)

func PrepareTaps(_ context.Context, count int) error {
	slog.Info("Preparing taps", "count", count)

	br, err := netlink.LinkByName(VLABBridge)
	if err != nil && !errors.As(err, &netlink.LinkNotFoundError{}) {
		return fmt.Errorf("getting bridge %q: %w", VLABBridge, err)
	}

	if errors.As(err, &netlink.LinkNotFoundError{}) && count > 0 {
		slog.Info("Creating bridge", "name", VLABBridge)

		la := netlink.NewLinkAttrs()
		la.Name = VLABBridge
		br = &netlink.Bridge{LinkAttrs: la}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("adding bridge %q: %w", VLABBridge, err)
		}
	} else if !errors.As(err, &netlink.LinkNotFoundError{}) && count == 0 {
		slog.Info("Deleting bridge", "name", VLABBridge)

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
			slog.Info("Deleting no more needed tap", "name", name)

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
			slog.Info("Creating tap", "name", name)

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

	return nil
}
