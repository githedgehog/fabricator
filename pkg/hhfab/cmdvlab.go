// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

const (
	DefaultVLABGeneratedFile = "vlab.generated.yaml"
)

func VLABGenerate(ctx context.Context, workDir, cacheDir string, builder VLABBuilder, target string) error {
	cfg, err := load(ctx, workDir, cacheDir, false, HydrateModeNever, "")
	if err != nil {
		return err
	}

	includeL := apiutil.NewLoader()
	if err := builder.Build(ctx, includeL, cfg.Fab.Spec.Config.Fabric.Mode, cfg.Nodes); err != nil {
		return err
	}

	includeDir := filepath.Join(workDir, IncludeDir)
	wiringFile, err := os.OpenFile(filepath.Join(includeDir, target), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating wiring file: %w", err)
	}
	defer wiringFile.Close()

	if err := apiutil.PrintInclude(ctx, includeL.GetClient(), wiringFile); err != nil {
		return fmt.Errorf("writing wiring file: %w", err)
	}

	slog.Info("Generated wiring file", "name", target)

	files, err := os.ReadDir(includeDir)
	if err != nil {
		return fmt.Errorf("reading include dir %q: %w", includeDir, err)
	}
	for _, file := range files {
		if file.IsDir() || file.Name() == target || !strings.HasSuffix(file.Name(), YAMLExt) {
			continue
		}

		slog.Warn("Include dir contains file(s) other than the generated wiring file", "name", file.Name())
	}

	return nil
}

type VLABUpOpts struct {
	HydrateMode          HydrateMode
	NoCreate             bool
	ReCreate             bool
	BuildMode            recipe.BuildMode
	SetJoinToken         string
	ObservabilityTargets string
	VLABRunOpts
}

func VLABUp(ctx context.Context, workDir, cacheDir string, opts VLABUpOpts) error {
	if opts.AutoUpgrade {
		opts.BuildMode = recipe.BuildModeManual
		opts.VLABRunOpts.BuildMode = recipe.BuildModeManual
	}

	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode, opts.SetJoinToken)
	if err != nil {
		return err
	}

	vlab, err := c.PrepareVLAB(ctx, opts)
	if err != nil {
		return fmt.Errorf("preparing VLAB: %w", err)
	}

	buildGateways := false
	for _, vm := range vlab.VMs {
		if vm.Type == VMTypeGateway {
			buildGateways = true

			break
		}
	}

	if opts.ControlsRestricted && c.Fab.Spec.Config.Registry.Mode == fabapi.RegistryModeUpstream {
		slog.Warn("Use --controls-restricted=false to allow external access for control nodes")

		return fmt.Errorf("controls restricted mode not supported for upstream registry") //nolint:err113
	}

	if err := c.build(ctx, BuildOpts{
		HydrateMode:          opts.HydrateMode,
		BuildMode:            opts.BuildMode,
		BuildControls:        true,
		BuildGateways:        buildGateways,
		ObservabilityTargets: opts.ObservabilityTargets,
	}); err != nil {
		return fmt.Errorf("building: %w", err)
	}

	return c.VLABRun(ctx, vlab, opts.VLABRunOpts)
}

func loadVLABForHelpers(ctx context.Context, workDir, cacheDir string) (*Config, *VLAB, error) {
	opts := VLABUpOpts{
		HydrateMode: HydrateModeIfNotPresent,
		NoCreate:    true,
	}

	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode, opts.SetJoinToken)
	if err != nil {
		return nil, nil, err
	}

	vlab, err := c.PrepareVLAB(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing VLAB: %w", err)
	}

	return c, vlab, nil
}

func DoVLABSSH(ctx context.Context, workDir, cacheDir, name string, args []string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSSH, name, args)
}

func DoVLABSerial(ctx context.Context, workDir, cacheDir, name string, args []string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSerial, name, args)
}

func DoVLABSerialLog(ctx context.Context, workDir, cacheDir, name string, args []string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSerialLog, name, args)
}

func DoShowTech(ctx context.Context, workDir, cacheDir string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABShowTech(ctx, vlab)
}

func DoVLABSetupVPCs(ctx context.Context, workDir, cacheDir string, opts SetupVPCsOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.SetupVPCs(ctx, vlab, opts)
}

func DoVLABSetupPeerings(ctx context.Context, workDir, cacheDir string, opts SetupPeeringsOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.SetupPeerings(ctx, vlab, opts)
}

func DoVLABTestConnectivity(ctx context.Context, workDir, cacheDir string, opts TestConnectivityOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.TestConnectivity(ctx, vlab, opts)
}

func DoVLABWait(ctx context.Context, workDir, cacheDir string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.Wait(ctx, vlab)
}

func DoVLABInspect(ctx context.Context, workDir, cacheDir string, opts InspectOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.Inspect(ctx, vlab, opts)
}

func DoVLABReleaseTest(ctx context.Context, workDir, cacheDir string, opts ReleaseTestOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.ReleaseTest(ctx, vlab, opts)
}

type SwitchPowerOpts struct {
	Switches    []string   // All switches if empty
	Action      pdu.Action // Power action (e.g., on, off, cycle)
	PDUUsername string
	PDUPassword string
}

func DoSwitchPower(ctx context.Context, workDir, cacheDir string, opts SwitchPowerOpts) error {
	c, _, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABSwitchPower(ctx, opts)
}

type SwitchReinstallOpts struct {
	Switches       []string            // All switches if empty
	Mode           SwitchReinstallMode // "reboot" or "hard-reset"
	SwitchUsername string              // Username for switch access (reboot mode only )
	SwitchPassword string              // Password for switch access (reboot mode only)
	PDUUsername    string              // (hard-reset mode only)
	PDUPassword    string              // (hard-reset mode only)
	WaitReady      bool                // Wait for the switch to be ready
}

type SwitchReinstallMode string

const (
	ReinstallModeReboot    SwitchReinstallMode = "reboot"
	ReinstallModeHardReset SwitchReinstallMode = "hard-reset"
)

var ReinstallModes = []SwitchReinstallMode{
	ReinstallModeReboot,
	ReinstallModeHardReset,
}

func DoSwitchReinstall(ctx context.Context, workDir, cacheDir string, opts SwitchReinstallOpts) error {
	c, _, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABSwitchReinstall(ctx, opts)
}
