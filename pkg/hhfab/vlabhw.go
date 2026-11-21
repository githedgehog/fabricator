// Copyright 2026 Hedgehog
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
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu"
	"golang.org/x/sync/errgroup"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// HWMode is what vlab up does with the hardware switches or nodes
type HWMode string

const (
	// HWModeAuto installs the ones not installed into this VLAB before
	HWModeAuto HWMode = "auto"
	// HWModeReinstall installs all of them
	HWModeReinstall HWMode = "reinstall"
	// HWModeDisabled leaves them as is
	HWModeDisabled HWMode = "disabled"
)

var HWModes = []HWMode{
	HWModeAuto,
	HWModeReinstall,
	HWModeDisabled,
}

// HWModeFlags are the hardware modes from the flags, empty if not set
type HWModeFlags struct {
	All      HWMode
	Switches HWMode
	Nodes    HWMode
}

var ErrPDUCredentials = errors.New("PDU username and password are required to manage hardware switches")

// resolveHWModes returns the switch and node modes
func resolveHWModes(flags HWModeFlags, upgrade bool) (HWMode, HWMode, error) {
	for _, mode := range []HWMode{flags.All, flags.Switches, flags.Nodes} {
		if mode != "" && !slices.Contains(HWModes, mode) {
			return "", "", fmt.Errorf("invalid hardware mode %q, expected one of %v", mode, HWModes) //nolint:err113
		}
	}

	if flags.All != "" && (flags.Switches != "" || flags.Nodes != "") {
		return "", "", fmt.Errorf("--hw can't be combined with --hw-switches or --hw-nodes") //nolint:err113
	}

	switches, nodes := flags.Switches, flags.Nodes
	if flags.All != "" {
		switches, nodes = flags.All, flags.All
	}

	if switches == "" {
		switches = HWModeAuto
	}
	if nodes == "" {
		nodes = HWModeAuto
	}

	if upgrade && (switches == HWModeReinstall || nodes == HWModeReinstall) {
		return "", "", fmt.Errorf("--upgrade can't reinstall hardware") //nolint:err113
	}

	return switches, nodes, nil
}

// UnusedHW is the hardware kept out of VLAB, only to power it off
type UnusedHW struct {
	Switches []wiringapi.Switch
	Nodes    []fabapi.FabNode
}

// dropUnusedHW removes the unused switches and nodes, so they never reach the cluster, installers or VLAB config;
// it's after the hydration, so it doesn't change the rest
func (c *Config) dropUnusedHW(ctx context.Context) error {
	conns := &wiringapi.ConnectionList{}
	if err := c.Client.List(ctx, conns); err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}
	connected := map[string]string{}
	for _, conn := range conns.Items {
		switches, _, _, _, err := conn.Spec.Endpoints()
		if err != nil {
			return fmt.Errorf("getting connection %s endpoints: %w", conn.Name, err)
		}
		for _, sw := range switches {
			connected[sw] = conn.Name
		}
	}

	unusedNames := []string{}

	switches := &wiringapi.SwitchList{}
	if err := c.Client.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}
	for _, sw := range switches.Items {
		unused, err := vlabcomp.IsUnused(sw.Annotations, sw.Spec.Description)
		if err != nil {
			return fmt.Errorf("switch %s: %w", sw.Name, err)
		}
		_, annotated := sw.Annotations[vlabcomp.UnusedAnnotation]
		if !unused {
			// the annotation is for hhfab only, keep it out of the cluster
			if annotated {
				delete(sw.Annotations, vlabcomp.UnusedAnnotation)
				if err := c.Client.Update(ctx, &sw); err != nil {
					return fmt.Errorf("updating switch %s: %w", sw.Name, err)
				}
			}

			continue
		}
		if !isHardware(&sw) {
			// the description fallback is only for the VLAB hardware
			if !annotated {
				continue
			}

			return fmt.Errorf("switch %s is unused, but only hardware can be", sw.Name) //nolint:err113
		}
		if conn, ok := connected[sw.Name]; ok {
			return fmt.Errorf("switch %s is unused, but connection %s uses it", sw.Name, conn) //nolint:err113
		}
		if !annotated {
			slog.Debug("Switch is unused by its description", "switch", sw.Name)
		}
		if len(hhfctl.GetPowerInfo(&sw)) == 0 {
			slog.Warn("Unused switch has no PDU info, it can't be powered off", "switch", sw.Name)
		}

		if err := c.Client.Delete(ctx, &sw); err != nil {
			return fmt.Errorf("removing unused switch %s: %w", sw.Name, err)
		}
		c.UnusedHW.Switches = append(c.UnusedHW.Switches, sw)
		unusedNames = append(unusedNames, sw.Name)
	}

	nodes := []fabapi.FabNode{}
	for _, node := range c.Nodes {
		unused, err := vlabcomp.IsUnused(node.Annotations, "")
		if err != nil {
			return fmt.Errorf("node %s: %w", node.Name, err)
		}
		if !unused {
			delete(node.Annotations, vlabcomp.UnusedAnnotation)
			nodes = append(nodes, node)

			continue
		}
		if !isHardware(&node) {
			return fmt.Errorf("node %s is unused, but only hardware can be", node.Name) //nolint:err113
		}
		if err := c.Client.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: node.Name}, &gwapi.Gateway{}); err == nil {
			return fmt.Errorf("node %s is unused, but gateway %s uses it", node.Name, node.Name) //nolint:err113
		} else if !kapierrors.IsNotFound(err) {
			return fmt.Errorf("getting gateway %s: %w", node.Name, err)
		}
		endpoint, err := getBMCEndpoint(&node)
		if err != nil {
			return err
		}
		if endpoint == "" {
			slog.Warn("Unused node has no BMC, it can't be powered off", "node", node.Name, "annotation", HHFabCfgBMC)
		}

		if err := c.Client.Delete(ctx, &node); kclient.IgnoreNotFound(err) != nil {
			return fmt.Errorf("removing unused node %s: %w", node.Name, err)
		}
		c.UnusedHW.Nodes = append(c.UnusedHW.Nodes, node)
		unusedNames = append(unusedNames, node.Name)
	}
	c.Nodes = nodes

	if len(unusedNames) > 0 {
		slog.Info("Unused hardware is kept out of VLAB", "names", unusedNames)
	}

	return nil
}

func (c *Config) hwMarker(name string) string {
	return filepath.Join(c.WorkDir, VLABDir, VLABHWDir, name+VLABHWInstalledSuffix)
}

func (c *Config) isHWInstalled(name string) (bool, error) {
	if _, err := os.Stat(c.hwMarker(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("checking %s install marker: %w", name, err)
	}

	return true, nil
}

func (c *Config) setHWInstalled(name string) error {
	marker := c.hwMarker(name)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return fmt.Errorf("creating install marker dir: %w", err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return fmt.Errorf("writing %s install marker: %w", name, err)
	}

	return nil
}

func (c *Config) resetHWInstalled(name string) error {
	if err := os.Remove(c.hwMarker(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s install marker: %w", name, err)
	}

	return nil
}

// hwPlan is what vlab up does with the used hardware, it's decided before the build to fail early; each switch and
// node is in one list only, so none is power cycled and powered on at the same time
type hwPlan struct {
	reinstallSwitches []string           // power cycled into the NOS install
	powerOnSwitches   []wiringapi.Switch // installed before, in case they're off
	reinstallNodes    map[string]string  // installed over BMC, by name to BMC endpoint
	upgradeNodes      []string           // upgraded same as VMs
	powerOnNodes      map[string]string  // installed before, in case they're off, by name to BMC endpoint
	manualNodes       map[string]bool    // installed by the user
}

// planHW has to run after the VLAB is prepared, as recreating it removes the install markers
func (c *Config) planHW(ctx context.Context, opts VLABRunOpts) (*hwPlan, error) {
	plan := &hwPlan{
		reinstallNodes: map[string]string{},
		powerOnNodes:   map[string]string{},
		manualNodes:    map[string]bool{},
	}

	switches := &wiringapi.SwitchList{}
	if err := c.Client.List(ctx, switches); err != nil {
		return nil, fmt.Errorf("listing switches: %w", err)
	}
	for _, sw := range switches.Items {
		if !isHardware(&sw) || opts.HWSwitches == HWModeDisabled {
			continue
		}

		// the reinstall needs both, the PDU to cycle it and the console to get it into the NOS install
		if len(hhfctl.GetPowerInfo(&sw)) == 0 || hhfctl.GetSerialInfo(&sw) == "" {
			if opts.HWSwitches == HWModeReinstall {
				return nil, fmt.Errorf("hardware switch %s has no PDU or serial console info to reinstall it", sw.Name) //nolint:err113
			}

			slog.Warn("Hardware switch without PDU or serial console info has to be installed manually", "switch", sw.Name)

			continue
		}

		installed, err := c.isHWInstalled(sw.Name)
		if err != nil {
			return nil, err
		}

		switch {
		case opts.HWSwitches == HWModeReinstall:
			plan.reinstallSwitches = append(plan.reinstallSwitches, sw.Name)
		case installed || opts.AutoUpgrade:
			plan.powerOnSwitches = append(plan.powerOnSwitches, sw)
		default:
			slog.Info("Hardware switch wasn't installed into this VLAB, reinstalling it", "switch", sw.Name)
			plan.reinstallSwitches = append(plan.reinstallSwitches, sw.Name)
		}
	}

	bmcNodes, err := c.bmcNodes()
	if err != nil {
		return nil, err
	}
	for _, node := range c.hardwareNodes() {
		endpoint := bmcNodes[node.Name]
		installed, err := c.isHWInstalled(node.Name)
		if err != nil {
			return nil, err
		}

		switch {
		case opts.HWNodes == HWModeDisabled:
			plan.manualNodes[node.Name] = true
		case opts.AutoUpgrade:
			plan.upgradeNodes = append(plan.upgradeNodes, node.Name)
			if endpoint != "" {
				plan.powerOnNodes[node.Name] = endpoint
			}
		case endpoint == "":
			if opts.HWNodes == HWModeReinstall {
				return nil, fmt.Errorf("hardware node %s has no %s annotation to reinstall it", node.Name, HHFabCfgBMC) //nolint:err113
			}
			plan.manualNodes[node.Name] = true
		case opts.HWNodes == HWModeReinstall || !installed:
			plan.reinstallNodes[node.Name] = endpoint
		default:
			plan.powerOnNodes[node.Name] = endpoint
		}
	}

	// powering on and off needs the credentials too
	needPDU := len(plan.reinstallSwitches) > 0 || len(plan.powerOnSwitches) > 0
	needBMC := len(plan.reinstallNodes) > 0 || len(plan.powerOnNodes) > 0
	if opts.HWPowerOffUnused {
		for _, sw := range c.UnusedHW.Switches {
			needPDU = needPDU || len(hhfctl.GetPowerInfo(&sw)) > 0
		}
		for _, node := range c.UnusedHW.Nodes {
			endpoint, _ := getBMCEndpoint(&node) // validated when dropped
			needBMC = needBMC || endpoint != ""
		}
	}
	if needPDU && (opts.PDUUsername == "" || opts.PDUPassword == "") {
		return nil, ErrPDUCredentials
	}
	if needBMC && (opts.BMC.Username == "" || opts.BMC.Password == "") {
		return nil, ErrBMCCredentials
	}

	if len(plan.reinstallSwitches) > 0 {
		if _, err := exec.LookPath(VLABCmdExpect); err != nil {
			return nil, fmt.Errorf("%q is required to reinstall hardware switches: %w", VLABCmdExpect, err)
		}
	}

	if err := checkBMCNodes(ctx, plan.reinstallNodes, opts.BMC); err != nil {
		return nil, err
	}

	return plan, nil
}

// powerOffUnusedHW runs on every vlab up, so the unused hardware is never left on
func (c *Config) powerOffUnusedHW(ctx context.Context, opts VLABRunOpts) error {
	if !opts.HWPowerOffUnused {
		return nil
	}

	group := &errgroup.Group{}
	for _, sw := range c.UnusedHW.Switches {
		group.Go(func() error {
			if err := c.resetHWInstalled(sw.Name); err != nil {
				return err
			}
			if len(hhfctl.GetPowerInfo(&sw)) == 0 {
				return nil
			}
			if err := powerSwitch(ctx, &sw, pdu.ActionOff, opts.PDUUsername, opts.PDUPassword); err != nil {
				return fmt.Errorf("powering off unused switch: %w", err)
			}

			return nil
		})
	}
	for _, node := range c.UnusedHW.Nodes {
		group.Go(func() error {
			if err := c.resetHWInstalled(node.Name); err != nil {
				return err
			}
			endpoint, _ := getBMCEndpoint(&node) // validated when dropped
			if endpoint == "" {
				return nil
			}
			if err := powerNode(ctx, endpoint, pdu.ActionOff, opts.BMC); err != nil {
				return fmt.Errorf("powering off unused node %s: %w", node.Name, err)
			}

			return nil
		})
	}

	return group.Wait() //nolint:wrapcheck
}

// powerOnUsedHW runs on every vlab up, so the used hardware that isn't reinstalled is on, as it may be off after a
// previous run. It's marked installed, as after an upgrade it's part of this VLAB without a marker.
func (c *Config) powerOnUsedHW(ctx context.Context, plan *hwPlan, opts VLABRunOpts) error {
	group := &errgroup.Group{}
	for _, sw := range plan.powerOnSwitches {
		group.Go(func() error {
			if err := powerSwitch(ctx, &sw, pdu.ActionOn, opts.PDUUsername, opts.PDUPassword); err != nil {
				return fmt.Errorf("powering on switch: %w", err)
			}

			return c.setHWInstalled(sw.Name)
		})
	}
	for name, endpoint := range plan.powerOnNodes {
		group.Go(func() error {
			if err := powerNode(ctx, endpoint, pdu.ActionOn, opts.BMC); err != nil {
				return fmt.Errorf("powering on node %s: %w", name, err)
			}

			return c.setHWInstalled(name)
		})
	}

	return group.Wait() //nolint:wrapcheck
}

// switchInstallTimeout is from the PDU cycle to all agents ready
const switchInstallTimeout = 30 * time.Minute

// hwSwitchesInstall puts the switches into the NOS install mode right away, they keep retrying the discovery until the
// control node serves them, and waits for their agents to be ready
func (c *Config) hwSwitchesInstall(ctx context.Context, names []string, opts VLABRunOpts) error {
	ctx, cancel := context.WithTimeout(ctx, switchInstallTimeout)
	defer cancel()

	// the script limits from the discovery to the NOS install would trip while the control node is still booting
	if err := c.VLABSwitchReinstall(ctx, SwitchReinstallOpts{
		Switches:    names,
		Mode:        ReinstallModeHardReset,
		PDUUsername: opts.PDUUsername,
		PDUPassword: opts.PDUPassword,
	}); err != nil {
		return err
	}

	// an existing cluster has the agent status from before the reinstall, the switches are in the NOS install by now
	start := time.Now()

	kube, _, err := c.clusterFab(ctx)
	if err != nil {
		return err
	}

	group := &errgroup.Group{}
	for _, name := range names {
		group.Go(func() error {
			if err := waitSwitchAgent(ctx, kube, name, start); err != nil {
				return err
			}
			if err := c.setHWInstalled(name); err != nil {
				return err
			}

			slog.Info("Hardware switch installed", "switch", name)

			return nil
		})
	}

	return group.Wait() //nolint:wrapcheck
}

// waitSwitchAgent waits for the agent at the version the cluster expects, with a heartbeat after the since time
func waitSwitchAgent(ctx context.Context, kube kclient.Reader, name string, since time.Time) error {
	for {
		f := &fabapi.Fabricator{}
		err := kube.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f)
		if err == nil {
			expected := string(f.Status.Versions.Fabric.Controller)
			ag := &agentapi.Agent{}
			err = kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: name}, ag)
			if err == nil && expected != "" && switchAgentApplied(ag, expected) && ag.Status.Version == expected &&
				ag.Status.LastHeartbeat.After(since) {
				return nil
			}
		}

		slog.Debug("Waiting for switch agent", "switch", name, "err", err)

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for switch %s agent: %w", name, ctx.Err())
		case <-time.After(15 * time.Second):
		}
	}
}
