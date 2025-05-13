// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
)

type BuildOpts struct {
	HydrateMode   HydrateMode
	BuildMode     recipe.BuildMode
	BuildControls bool
	BuildGateways bool
	SetJoinToken  string
}

func Build(ctx context.Context, workDir, cacheDir string, opts BuildOpts) error {
	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode, opts.SetJoinToken)
	if err != nil {
		return err
	}

	return c.build(ctx, opts)
}

func (c *Config) build(ctx context.Context, opts BuildOpts) error {
	if !slices.Contains(recipe.BuildModes, opts.BuildMode) {
		return fmt.Errorf("invalid build mode %q", opts.BuildMode) //nolint:goerr113
	}

	for _, node := range c.Nodes {
		if !slices.Equal(node.Spec.Roles, []fabapi.FabNodeRole{fabapi.NodeRoleGateway}) {
			return fmt.Errorf("unsupported node roles %q (only gateway role is currently supported)", node.Spec.Roles) //nolint:goerr113
		}
	}

	d, err := artificer.NewDownloaderWithDockerCreds(c.CacheDir, c.Repo, c.Prefix)
	if err != nil {
		return fmt.Errorf("creating downloader: %w", err)
	}

	resultDir := filepath.Join(c.WorkDir, ResultDir)

	if opts.BuildControls {
		slog.Info("Building control node installers")

		for _, control := range c.Controls {
			if err := (&recipe.ControlInstallBuilder{
				WorkDir:    resultDir,
				Fab:        c.Fab,
				Control:    control,
				Nodes:      c.Nodes,
				Client:     c.Client,
				Mode:       opts.BuildMode,
				Downloader: d,
			}).Build(ctx); err != nil {
				return fmt.Errorf("building control node %s installer: %w", control.Name, err)
			}
		}
	}

	if opts.BuildGateways {
		slog.Info("Building node installers")

		for _, node := range c.Nodes {
			if len(node.Spec.Roles) != 1 {
				return fmt.Errorf("unsupported node roles %q (only one role is currently supported)", node.Spec.Roles) //nolint:goerr113
			}

			if err := (&recipe.NodeInstallBuilder{
				WorkDir:    resultDir,
				Fab:        c.Fab,
				Node:       node,
				Mode:       opts.BuildMode,
				Downloader: d,
			}).Build(ctx); err != nil {
				return fmt.Errorf("building node %s installer: %w", node.Name, err)
			}
		}
	}

	return nil
}
