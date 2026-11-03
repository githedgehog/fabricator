// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/libmeta/pkg/alloy"
	kyaml "sigs.k8s.io/yaml"
)

type BuildOpts struct {
	HydrateMode          HydrateMode
	BuildMode            recipe.BuildMode
	ModeOverrides        map[string]recipe.BuildMode
	BuildControls        bool
	BuildGateways        bool
	SetJoinToken         string
	ObservabilityTargets string
}

func Build(ctx context.Context, workDir, cacheDir string, extraCacheDirs []string, opts BuildOpts) error {
	c, err := load(ctx, workDir, cacheDir, extraCacheDirs, true, opts.HydrateMode, opts.SetJoinToken)
	if err != nil {
		return err
	}

	// hardware nodes are installed from the ISO unless explicitly overridden
	overrides := c.hardwareModeOverrides(recipe.BuildModeISO)
	maps.Copy(overrides, opts.ModeOverrides)
	opts.ModeOverrides = overrides

	return c.build(ctx, opts)
}

// ParseModeOverrides parses <name>=<mode> overrides, build validates them
func ParseModeOverrides(raw []string) (map[string]recipe.BuildMode, error) {
	overrides := map[string]recipe.BuildMode{}
	for _, override := range raw {
		name, mode, ok := strings.Cut(override, "=")
		if !ok || name == "" || mode == "" {
			return nil, fmt.Errorf("invalid build mode override %q, expected <name>=<mode>", override) //nolint:err113
		}
		if _, exist := overrides[name]; exist {
			return nil, fmt.Errorf("duplicate build mode override for %q", name) //nolint:err113
		}

		overrides[name] = recipe.BuildMode(mode)
	}

	return overrides, nil
}

func (c *Config) hardwareModeOverrides(mode recipe.BuildMode) map[string]recipe.BuildMode {
	overrides := map[string]recipe.BuildMode{}
	for _, control := range c.Controls {
		if isHardware(&control) {
			overrides[control.Name] = mode
		}
	}
	for _, node := range c.hardwareNodes() {
		overrides[node.Name] = mode
	}

	return overrides
}

func buildMode(name string, opts BuildOpts) recipe.BuildMode {
	if mode, ok := opts.ModeOverrides[name]; ok {
		return mode
	}

	return opts.BuildMode
}

func (c *Config) build(ctx context.Context, opts BuildOpts) error {
	if !slices.Contains(recipe.BuildModes, opts.BuildMode) {
		return fmt.Errorf("invalid build mode %q", opts.BuildMode) //nolint:err113
	}

	for name, mode := range opts.ModeOverrides {
		if !slices.Contains(recipe.BuildModes, mode) {
			return fmt.Errorf("invalid build mode %q for %q", mode, name) //nolint:err113
		}

		known := slices.ContainsFunc(c.Controls, func(control fabapi.ControlNode) bool { return control.Name == name }) ||
			slices.ContainsFunc(c.Nodes, func(node fabapi.FabNode) bool { return node.Name == name })
		if !known {
			return fmt.Errorf("build mode override for unknown control or node %q", name) //nolint:err113
		}
	}

	for _, node := range c.Nodes {
		if !slices.Equal(node.Spec.Roles, []fabapi.FabNodeRole{fabapi.NodeRoleGateway}) {
			return fmt.Errorf("unsupported node roles %q (only gateway role is currently supported)", node.Spec.Roles) //nolint:err113
		}
	}

	// installers built manually in a work dir with VLAB should include the VLAB SSH key same as vlab up does
	vlabKey, err := getVLABSSHKey(c.WorkDir)
	if err != nil {
		return err
	}
	if vlabKey != "" && c.addAuthorizedKey(vlabKey) {
		slog.Info("Including VLAB SSH key into installers", "file", filepath.Join(VLABDir, VLABSSHKeyFile))
	}

	targets := alloy.Targets{}
	if err := kyaml.Unmarshal([]byte(opts.ObservabilityTargets), &targets); err != nil {
		return fmt.Errorf("unmarshaling extra observability targets: %w", err)
	}

	if c.Fab.Spec.Config.Observability.Targets.Prometheus == nil {
		c.Fab.Spec.Config.Observability.Targets.Prometheus = map[string]alloy.PrometheusTarget{}
	}
	for name, target := range targets.Prometheus {
		if _, ok := c.Fab.Spec.Config.Observability.Targets.Prometheus[name]; ok {
			slog.Warn("Skipping extra Prometheus target that is already defined in Fabricator", "name", name)

			continue
		}
		slog.Debug("Adding extra Prometheus target", "name", name)
		c.Fab.Spec.Config.Observability.Targets.Prometheus[name] = target
	}
	if c.Fab.Spec.Config.Observability.Targets.Loki == nil {
		c.Fab.Spec.Config.Observability.Targets.Loki = map[string]alloy.LokiTarget{}
	}
	for name, target := range targets.Loki {
		if _, ok := c.Fab.Spec.Config.Observability.Targets.Loki[name]; ok {
			slog.Warn("Skipping extra Loki target that is already defined in Fabricator", "name", name)

			continue
		}
		slog.Debug("Adding extra Loki target", "name", name)
		c.Fab.Spec.Config.Observability.Targets.Loki[name] = target
	}
	if c.Fab.Spec.Config.Observability.Targets.Pyroscope == nil {
		c.Fab.Spec.Config.Observability.Targets.Pyroscope = map[string]alloy.PyroscopeTarget{}
	}
	for name, target := range targets.Pyroscope {
		if _, ok := c.Fab.Spec.Config.Observability.Targets.Pyroscope[name]; ok {
			slog.Warn("Skipping extra Pyroscope target that is already defined in Fabricator", "name", name)

			continue
		}
		slog.Debug("Adding extra Pyroscope target", "name", name)
		c.Fab.Spec.Config.Observability.Targets.Pyroscope[name] = target
	}

	d, err := artificer.NewDownloaderWithDockerCreds(c.CacheDir, c.ExtraCacheDirs, c.Repo, c.Prefix)
	if err != nil {
		return fmt.Errorf("creating downloader: %w", err)
	}

	resultDir := filepath.Join(c.WorkDir, ResultDir)

	if opts.BuildControls {
		slog.Info("Building control node installers")

		for _, control := range c.Controls {
			mode := buildMode(control.Name, opts)
			if mode != opts.BuildMode {
				slog.Info("Overriding build mode", "control", control.Name, "mode", mode)
			}

			if err := (&recipe.ControlInstallBuilder{
				WorkDir:    resultDir,
				Fab:        c.Fab,
				Control:    control,
				Nodes:      c.Nodes,
				Client:     c.Client,
				Mode:       mode,
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
				return fmt.Errorf("unsupported node roles %q (only one role is currently supported)", node.Spec.Roles) //nolint:err113
			}

			mode := buildMode(node.Name, opts)
			if mode != opts.BuildMode {
				slog.Info("Overriding build mode", "node", node.Name, "mode", mode)
			}

			if err := (&recipe.NodeInstallBuilder{
				WorkDir:    resultDir,
				Fab:        c.Fab,
				Node:       node,
				Client:     c.Client,
				Mode:       mode,
				Downloader: d,
			}).Build(ctx); err != nil {
				return fmt.Errorf("building node %s installer: %w", node.Name, err)
			}
		}
	}

	return nil
}
