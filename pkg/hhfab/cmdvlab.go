package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/davecgh/go-spew/spew"
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
	wiringFile, err := os.Create(filepath.Join(includeDir, target))
	if err != nil {
		return fmt.Errorf("creating wiring file: %w", err)
	}
	defer wiringFile.Close()

	if err := printWiring(ctx, wL.GetClient(), wiringFile); err != nil {
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

func VLABUp(ctx context.Context, workDir, cacheDir string, hMode HydrateMode, killStale bool) error {
	cfg, err := load(ctx, workDir, cacheDir, true, hMode)
	if err != nil {
		return err
	}

	vlab, err := cfg.PrepareVLAB(ctx, VLABConfigOpts{
		ControlsRestricted: true,
		ServersRestricted:  true,
	})
	if err != nil {
		return fmt.Errorf("creating VLAB: %w", err)
	}

	// TODO remove
	spew.Dump(vlab)

	return cfg.VLABRun(ctx, vlab, killStale)
}
