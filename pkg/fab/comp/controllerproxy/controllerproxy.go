// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package controllerproxy

import (
	"context"
	_ "embed"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartRef = "fabricator/charts/controller-proxy"
	ImageRef = "fabricator/controller-proxy"
	NodePort = 31028
)

//go:embed values.tmpl.yaml
var valuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	repo, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}

	//TODO pull correct urls from the alloy configuration
	urls := []string{"*.grafana.net"}
	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo":         repo,
		"Tag":          string(cfg.Status.Versions.Platform.ControllerProxy),
		"NodePort":     NodePort,
		"TinproxyURLs": urls,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	chartVersion := string(cfg.Status.Versions.Platform.ControllerProxyChart)
	chart, err := comp.NewHelmChart(cfg, "controller-proxy", ChartRef, chartVersion, "", false, values)
	if err != nil {
		return nil, fmt.Errorf("chart: %w", err)
	}

	return []kclient.Object{chart}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ChartRef: cfg.Status.Versions.Platform.ControllerProxyChart,
		ImageRef: cfg.Status.Versions.Platform.ControllerProxy,
	}, nil
}

var _ comp.KubeStatus = Status

func Status(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Platform.ControllerProxy)

	return comp.GetDeploymentStatus("controller-proxy", "controller-proxy", image)(ctx, kube, cfg)
}
