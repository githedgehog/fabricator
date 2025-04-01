// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	_ "embed"
	"fmt"

	// just to keep the import
	_ "go.githedgehog.com/gateway/api/gateway/v1alpha1"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	CtrlRef      = "gateway/gateway"
	CtrlChartRef = "gateway/charts/gateway"
	APIChartRef  = "gateway/charts/gateway-api"
)

//go:embed values.tmpl.yaml
var valuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	if !cfg.Spec.Config.Gateway.Enable {
		return nil, nil
	}

	repo, err := comp.ImageURL(cfg, CtrlRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", CtrlRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo": repo,
		"Tag":  string(cfg.Status.Versions.Gateway.Controller),
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	ctrlChartVersion := string(cfg.Status.Versions.Gateway.Controller)
	ctrlChart, err := comp.NewHelmChart(cfg, "gateway", CtrlChartRef, ctrlChartVersion, "", false, values)
	if err != nil {
		return nil, fmt.Errorf("ctrl chart: %w", err)
	}

	apiChartVersion := string(cfg.Status.Versions.Gateway.API)
	apiChart, err := comp.NewHelmChart(cfg, "gateway-api", APIChartRef, apiChartVersion, "", false, "")
	if err != nil {
		return nil, fmt.Errorf("api chart: %w", err)
	}

	return []kclient.Object{
		apiChart,
		ctrlChart,
	}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		APIChartRef:  cfg.Status.Versions.Gateway.API,
		CtrlRef:      cfg.Status.Versions.Gateway.Controller,
		CtrlChartRef: cfg.Status.Versions.Gateway.Controller,
	}, nil
}

var (
	_ comp.KubeStatus = StatusAPI
	_ comp.KubeStatus = StatusCtrl
)

func StatusAPI(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	if !cfg.Spec.Config.Gateway.Enable {
		return fabapi.CompStatusSkipped, nil
	}

	return comp.MergeKubeStatuses(ctx, kube, cfg, //nolint:wrapcheck
		comp.GetCRDStatus("gateways.gateway.githedgehog.com", "v1alpha1"),
		comp.GetCRDStatus("peerings.gateway.githedgehog.com", "v1alpha1"),
		comp.GetCRDStatus("vpcinfoes.gateway.githedgehog.com", "v1alpha1"),
	)
}

func StatusCtrl(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	if !cfg.Spec.Config.Gateway.Enable {
		return fabapi.CompStatusSkipped, nil
	}

	ref, err := comp.ImageURL(cfg, CtrlRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", CtrlRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Gateway.Controller)

	return comp.GetDeploymentStatus("gateway-ctrl", "manager", image)(ctx, kube, cfg)
}
