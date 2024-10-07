// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package reloader

import (
	_ "embed"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	version := string(Version(cfg))

	repo, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo": repo,
		"Tag":  version,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	helmChart, err := comp.NewHelmChart(cfg, "reloader", ChartRef, version, "", false, values)
	if err != nil {
		return nil, fmt.Errorf("helm chart: %w", err)
	}

	return []client.Object{helmChart}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	version := Version(cfg)

	return comp.OCIArtifacts{
		ChartRef: version,
		ImageRef: version,
	}, nil
}
