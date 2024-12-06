// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package ntp

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartRef = "fabricator/charts/ntp"
	ImageRef = "fabricator/ntp"
	NodePort = 30123
)

//go:embed values.tmpl.yaml
var valuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	repo, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo":       repo,
		"Tag":        string(cfg.Status.Versions.Platform.NTP),
		"NodePort":   NodePort,
		"NTPServers": strings.Join(cfg.Spec.Config.Control.NTPServers, ","),
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	chartVersion := string(cfg.Status.Versions.Platform.NTPChart)
	chart, err := comp.NewHelmChart(cfg, "ntp", ChartRef, chartVersion, "", false, values)
	if err != nil {
		return nil, fmt.Errorf("chart: %w", err)
	}

	return []client.Object{chart}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ChartRef: cfg.Status.Versions.Platform.NTPChart,
		ImageRef: cfg.Status.Versions.Platform.NTP,
	}, nil
}

var _ comp.KubeStatus = Status

func Status(ctx context.Context, kube client.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Platform.NTP)

	return comp.GetDeploymentStatus("ntp", "ntp", image)(ctx, kube, cfg)
}
