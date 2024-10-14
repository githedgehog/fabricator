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

	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

const (
	DefaultVLABGeneratedFile = "vlab.generated.yaml"
)

func VLABGenerate(ctx context.Context, workDir, cacheDir string, builder VLABBuilder, target string) error {
	cfg, err := load(ctx, workDir, cacheDir, false, HydrateModeNever)
	if err != nil {
		return err
	}

	wL := apiutil.NewWiringLoader()
	if err := builder.Build(ctx, wL, cfg.Fab.Spec.Config.Fabric.Mode); err != nil {
		return err
	}

	includeDir := filepath.Join(workDir, IncludeDir)
	wiringFile, err := os.OpenFile(filepath.Join(includeDir, target), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating wiring file: %w", err)
	}
	defer wiringFile.Close()

	if err := apiutil.PrintWiring(ctx, wL.GetClient(), wiringFile); err != nil {
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
	HydrateMode HydrateMode
	NoCreate    bool
	ReCreate    bool
	USBImage    bool
	VLABRunOpts
}

func VLABUp(ctx context.Context, workDir, cacheDir string, opts VLABUpOpts) error {
	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode)
	if err != nil {
		return err
	}

	vlab, err := c.PrepareVLAB(ctx, opts)
	if err != nil {
		return fmt.Errorf("preparing VLAB: %w", err)
	}

	if err := c.build(ctx, BuildOpts{
		HydrateMode: opts.HydrateMode,
		USBImage:    opts.USBImage,
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

	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode)
	if err != nil {
		return nil, nil, err
	}

	vlab, err := c.PrepareVLAB(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing VLAB: %w", err)
	}

	return c, vlab, nil
}

func DoVLABSSH(ctx context.Context, workDir, cacheDir, name string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSSH, name)
}

func DoVLABSerial(ctx context.Context, workDir, cacheDir, name string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSerial, name)
}

func DoVLABSerialLog(ctx context.Context, workDir, cacheDir, name string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSerialLog, name)
}

func DoVLABSetupVPCs(ctx context.Context, workDir, cacheDir string, opts SetupVPCsOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.SetupVPCs(ctx, vlab, opts)
}
