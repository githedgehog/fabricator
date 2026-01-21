// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
)

type PrecacheOpts struct {
	All  bool
	VLAB bool
}

func Precache(ctx context.Context, workDir, cacheDir string, opts PrecacheOpts) error {
	c, err := load(ctx, workDir, cacheDir, false, HydrateModeNever, "")
	if err != nil {
		return err
	}

	return c.precache(ctx, opts)
}

func (c *Config) precache(ctx context.Context, opts PrecacheOpts) error {
	d, err := artificer.NewDownloaderWithDockerCreds(c.CacheDir, c.Repo, c.Prefix)
	if err != nil {
		return fmt.Errorf("creating downloader: %w", err)
	}

	slog.Info("Precaching airgap artifacts for installers")
	{
		artLists := slices.Clone(recipe.AirgapArtifactsBase)
		if c.Fab.Spec.Config.Gateway.Enable {
			artLists = append(artLists, recipe.AirgapArtifactsGateway...)
		}
		arts, err := comp.CollectArtifacts(c.Fab, artLists...)
		if err != nil {
			return fmt.Errorf("collecting airgap OCI artifacts: %w", err)
		}

		for ref, version := range arts {
			if err := d.WithOCI(ctx, ref, version, artificer.Noop); err != nil {
				return fmt.Errorf("precaching airgap OCI artifact %s:%s: %w", ref, version, err)
			}
		}
	}

	slog.Info("Precaching artifacts to be embedded into installers")
	{
		arts, err := comp.CollectArtifacts(c.Fab, recipe.PrecacheNodeBuildOCI)
		if err != nil {
			return fmt.Errorf("collecting OCI artifacts for embedding into installers: %w", err)
		}

		for ref, version := range arts {
			if err := d.WithOCI(ctx, ref, version, artificer.Noop); err != nil {
				return fmt.Errorf("precaching OCI artifact %s:%s for embedding into installers: %w", ref, version, err)
			}
		}
	}
	{
		arts, err := comp.CollectArtifacts(c.Fab, recipe.PrecacheNodeBuildORAS)
		if err != nil {
			return fmt.Errorf("collecting ORAS artifacts for embedding into installers: %w", err)
		}

		for ref, version := range arts {
			if err := d.WithORAS(ctx, ref, version, artificer.Noop); err != nil {
				return fmt.Errorf("precaching ORAS artifact %s:%s for embedding into installers: %w", ref, version, err)
			}
		}
	}

	if opts.All || opts.VLAB {
		slog.Info("Precaching artifacts for VLAB")
		{
			arts, err := comp.CollectArtifacts(c.Fab, PrecacheVLABOCI)
			if err != nil {
				return fmt.Errorf("collecting OCI artifacts for VLAB: %w", err)
			}

			for ref, version := range arts {
				if err := d.WithOCI(ctx, ref, version, artificer.Noop); err != nil {
					return fmt.Errorf("precaching OCI artifact %s:%s for VLAB: %w", ref, version, err)
				}
			}
		}
		{
			arts, err := comp.CollectArtifacts(c.Fab, PrecacheVLABORAS)
			if err != nil {
				return fmt.Errorf("collecting ORAS artifacts for VLAB: %w", err)
			}

			for ref, version := range arts {
				if err := d.WithORAS(ctx, ref, version, artificer.Noop); err != nil {
					return fmt.Errorf("precaching ORAS artifact %s:%s for VLAB: %w", ref, version, err)
				}
			}
		}
	}

	return nil
}
