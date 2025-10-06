// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package reloader

import (
	"context"
	_ "embed"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartRef = "fabricator/charts/reloader"
	ImageRef = "fabricator/reloader"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.Reloader
}

//go:embed values.tmpl.yaml
var valuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	imageVersion := string(cfg.Status.Versions.Platform.Reloader)
	chartVersion := string(cfg.Status.Versions.Platform.ReloaderChart)

	repo, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo": repo,
		"Tag":  imageVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	helmChart, err := comp.NewHelmChart(cfg, "reloader", ChartRef, chartVersion, "", false, values)
	if err != nil {
		return nil, fmt.Errorf("helm chart: %w", err)
	}

	return []kclient.Object{helmChart}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ChartRef: cfg.Status.Versions.Platform.ReloaderChart,
		ImageRef: cfg.Status.Versions.Platform.Reloader,
	}, nil
}

var _ comp.KubeStatus = Status

func Status(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Platform.Reloader)

	return comp.GetDeploymentStatus("reloader-reloader", "reloader-reloader", image)(ctx, kube, cfg)
}
