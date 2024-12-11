// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package f8r

import (
	"context"
	_ "embed"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	CtrlRef      = "fabricator/fabricator"
	CtrlChartRef = "fabricator/charts/fabricator"
	APIChartRef  = "fabricator/charts/fabricator-api"
	CtlRef       = "fabricator/hhfabctl"

	BinDir         = "/opt/bin"
	CtlBinName     = "hhfabctl"
	CtlDestBinName = "kubectl-hhfab"
)

//go:embed values.tmpl.yaml
var valuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	repo, err := comp.ImageURL(cfg, CtrlRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", CtrlRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo": repo,
		"Tag":  string(cfg.Status.Versions.Fabricator.Controller),
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	ctrlChartVersion := string(cfg.Status.Versions.Fabricator.Controller)
	ctrlChart, err := comp.NewHelmChart(cfg, "fabricator", CtrlChartRef, ctrlChartVersion, "", false, values)
	if err != nil {
		return nil, fmt.Errorf("ctrl chart: %w", err)
	}

	apiChartVersion := string(cfg.Status.Versions.Fabricator.API)
	apiChart, err := comp.NewHelmChart(cfg, "fabricator-api", APIChartRef, apiChartVersion, "", false, "")
	if err != nil {
		return nil, fmt.Errorf("api chart: %w", err)
	}

	return []client.Object{
		apiChart,
		ctrlChart,
	}, nil
}

var _ comp.KubeInstall = Install

func InstallFabAndControl(control fabapi.ControlNode) comp.KubeInstall {
	return func(cfg fabapi.Fabricator) ([]client.Object, error) {
		return []client.Object{
			&cfg,
			&control,
		}, nil
	}
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		APIChartRef:  cfg.Status.Versions.Fabricator.API,
		CtrlRef:      cfg.Status.Versions.Fabricator.Controller,
		CtrlChartRef: cfg.Status.Versions.Fabricator.Controller,
		CtlRef:       cfg.Status.Versions.Fabricator.Ctl,
	}, nil
}

var (
	_ comp.KubeStatus = StatusAPI
	_ comp.KubeStatus = StatusCtrl
)

func StatusAPI(ctx context.Context, kube client.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	return comp.MergeKubeStatuses(ctx, kube, cfg, //nolint:wrapcheck
		comp.GetCRDStatus("fabricators.fabricator.githedgehog.com", "v1beta1"),
		comp.GetCRDStatus("controlnodes.fabricator.githedgehog.com", "v1beta1"),
	)
}

func StatusCtrl(ctx context.Context, kube client.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, CtrlRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", CtrlRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Fabricator.Controller)

	return comp.GetDeploymentStatus("fabricator-ctrl", "manager", image)(ctx, kube, cfg)
}
