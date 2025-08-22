// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package controlproxy

import (
	"context"
	_ "embed"
	"fmt"
	"net/url"

	"github.com/samber/lo"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartRef = "fabricator/charts/control-proxy"
	ImageRef = "fabricator/control-proxy"
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

	urls := []string{}
	for _, val := range cfg.Spec.Config.Observability.Targets.Prometheus {
		u, err := url.Parse(val.URL)
		if err != nil {
			return nil, fmt.Errorf("url parsing prometheus target failed: %w", err)
		}
		hostname := u.Hostname()
		if hostname != "" {
			urls = append(urls, u.Hostname())
		}
	}
	for _, val := range cfg.Spec.Config.Observability.Targets.Loki {
		u, err := url.Parse(val.URL)
		if err != nil {
			return nil, fmt.Errorf("url parsing loki target failed: %w", err)
		}
		hostname := u.Hostname()
		if hostname != "" {
			urls = append(urls, u.Hostname())
		}
	}
	urls = lo.Uniq(urls)
	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo":          repo,
		"Tag":           string(cfg.Status.Versions.Platform.ControlProxy),
		"NodePort":      NodePort,
		"TinyproxyURLs": urls,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	chartVersion := string(cfg.Status.Versions.Platform.ControlProxyChart)
	chart, err := comp.NewHelmChart(cfg, "control-proxy", ChartRef, chartVersion, "", false, values)
	if err != nil {
		return nil, fmt.Errorf("chart: %w", err)
	}

	return []kclient.Object{chart}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ChartRef: cfg.Status.Versions.Platform.ControlProxyChart,
		ImageRef: cfg.Status.Versions.Platform.ControlProxy,
	}, nil
}

var _ comp.KubeStatus = Status

func Status(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Platform.ControlProxy)

	return comp.GetDeploymentStatus("control-proxy", "control-proxy", image)(ctx, kube, cfg)
}
