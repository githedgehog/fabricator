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
		return err
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
	Recreate    bool
	VLABRunOpts
}

func VLABUp(ctx context.Context, workDir, cacheDir string, opts VLABUpOpts) error {
	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode)
	if err != nil {
		return err
	}

	vlab, err := c.PrepareVLAB(ctx, opts)
	if err != nil {
		return fmt.Errorf("creating VLAB: %w", err)
	}

	// TODO do not rebuild if not needed
	slog.Info("Building installers")
	if err := c.build(ctx, BuildOpts{Mode: BuildModeManual}); err != nil {
		return fmt.Errorf("building: %w", err)
	}

	return c.VLABRun(ctx, vlab, opts.VLABRunOpts)
}
