// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
)

type BuildOpts struct {
	HydrateMode HydrateMode
	BuildMode   recipe.BuildMode
	// JoinToken   string // TODO to use specific k3s join token
}

func Build(ctx context.Context, workDir, cacheDir string, opts BuildOpts) error {
	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode)
	if err != nil {
		return err
	}

	return c.build(ctx, opts)
}

func (c *Config) build(ctx context.Context, opts BuildOpts) error {
	if !slices.Contains(recipe.BuildModes, opts.BuildMode) {
		return fmt.Errorf("invalid build mode %q", opts.BuildMode) //nolint:goerr113
	}

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
			Mode:       opts.BuildMode,
			Downloader: d,
		}).Build(ctx); err != nil {
			return fmt.Errorf("building control node %s installer: %w", control.Name, err)
		}
	}

	return nil
}
