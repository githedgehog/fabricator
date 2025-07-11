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
	"go.githedgehog.com/fabricator/pkg/fab/comp/alloy"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"go.githedgehog.com/gateway/api/meta"
	corev1 "k8s.io/api/core/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

const (
	CtrlRef              = "gateway/gateway"
	CtrlChartRef         = "gateway/charts/gateway"
	APIChartRef          = "gateway/charts/gateway-api"
	AgentRef             = "gateway/gateway-agent"
	DataplaneRef         = "dataplane"
	FRRRef               = "dpdk-sys/frr"
	DataplaneMetricsPort = 9442
	FRRMetricsPort       = 9342
)

//go:embed values.tmpl.yaml
var valuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	if !cfg.Spec.Config.Gateway.Enable {
		return nil, nil
	}

	ctrlRepo, err := comp.ImageURL(cfg, CtrlRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", CtrlRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo": ctrlRepo,
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

	agentRepo, err := comp.ImageURL(cfg, AgentRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", AgentRef, err)
	}

	dataplaneRepo, err := comp.ImageURL(cfg, DataplaneRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", DataplaneRef, err)
	}

	frrRepo, err := comp.ImageURL(cfg, FRRRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", FRRRef, err)
	}

	registryURL, err := comp.RegistryURL(cfg)
	if err != nil {
		return nil, fmt.Errorf("getting registry URL: %w", err)
	}

	controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing control VIP: %w", err)
	}

	ctrlCfgData, err := kyaml.Marshal(&meta.GatewayCtrlConfig{
		Namespace: comp.FabNamespace,
		Tolerations: []corev1.Toleration{
			{
				Key:      fabapi.RoleTaintKey(fabapi.NodeRoleGateway),
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoExecute,
			},
		},
		AgentRef:             agentRepo + ":" + string(cfg.Status.Versions.Gateway.Agent),
		DataplaneRef:         dataplaneRepo + ":" + string(cfg.Status.Versions.Gateway.Dataplane),
		FRRRef:               frrRepo + ":" + string(cfg.Status.Versions.Gateway.FRR),
		DataplaneMetricsPort: DataplaneMetricsPort,
		FRRMetricsPort:       FRRMetricsPort,
		RegistryURL:          registryURL,
		RegistryCASecret:     comp.FabCAConfigMap,
		RegistryAuthSecret:   comp.RegistryUserReaderSecret + comp.RegistryUserSecretDockerSuffix,
		AlloyChartName:       comp.JoinURLParts(comp.RegPrefix, alloy.ChartRef),
		AlloyChartVersion:    string(alloy.Version(cfg)),
		AlloyImageName:       comp.JoinURLParts(comp.RegPrefix, alloy.ImageRef),
		AlloyImageVersion:    string(alloy.Version(cfg)),
		ControlProxyURL:      fmt.Sprintf("http://%s:%d", controlVIP.Addr().String(), fabric.ProxyNodePort),
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling ctrl config: %w", err)
	}

	return []kclient.Object{
		apiChart,
		ctrlChart,
		comp.NewConfigMap("gateway-ctrl-config", map[string]string{
			"config.yaml": string(ctrlCfgData),
		}),
	}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		APIChartRef:  cfg.Status.Versions.Gateway.API,
		CtrlRef:      cfg.Status.Versions.Gateway.Controller,
		CtrlChartRef: cfg.Status.Versions.Gateway.Controller,
		AgentRef:     cfg.Status.Versions.Gateway.Agent,
		DataplaneRef: cfg.Status.Versions.Gateway.Dataplane,
		FRRRef:       cfg.Status.Versions.Gateway.FRR,
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
		comp.GetCRDStatus("vpcinfos.gateway.githedgehog.com", "v1alpha1"),
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
