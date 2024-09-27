package hhfab

import (
	"context"
	"fmt"
	"path/filepath"

	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
)

type BuildOpts struct {
	HydrateMode HydrateMode
	Mode        BuildMode
	// JoinToken   string // TODO to use specific k3s join token
}

type BuildMode string

const (
	BuildModeManual BuildMode = "manual"
	BuildModeISO    BuildMode = "iso"
)

var BuildModes = []BuildMode{
	BuildModeManual,
	BuildModeISO,
}

func Build(ctx context.Context, workDir, cacheDir string, hMode HydrateMode, opts BuildOpts) error {
	c, err := load(ctx, workDir, cacheDir, true, hMode)
	if err != nil {
		return err
	}

	return c.build(ctx, opts)
}

func (c *Config) build(ctx context.Context, opts BuildOpts) error {
	if opts.Mode != BuildModeManual {
		return fmt.Errorf("unsupported build mode %q", opts.Mode) //nolint:goerr113
	}

	// TODO
	// Manual: Build installer, pack it, build ignition
	// ISO: Build installer, pack it into ISO

	d, err := artificer.NewDownloaderWithDockerCreds(c.CacheDir, c.Repo, c.Prefix)
	if err != nil {
		return fmt.Errorf("creating downloader: %w", err)
	}

	resultDir := filepath.Join(c.WorkDir, ResultDir)

	for _, control := range c.Controls {
		if err := (&recipe.ControlInstallBuilder{
			WorkDir:    resultDir,
			Fab:        c.Fab,
			Control:    control,
			Wiring:     c.Wiring,
			Downloader: d,
		}).Build(ctx); err != nil {
			return fmt.Errorf("building control node %s installer: %w", control.Name, err)
		}
	}

	return nil
}
