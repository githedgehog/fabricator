// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package alloy

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"strconv"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/controlproxy"
	"go.githedgehog.com/fabricator/pkg/fab/comp/gateway"
	"go.githedgehog.com/libmeta/pkg/alloy"
	"go.githedgehog.com/libmeta/pkg/tmpl"
	corev1 "k8s.io/api/core/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

const (
	BinRef   = "fabricator/alloy-bin" // used for fabric switches
	ImageRef = "fabricator/alloy"
	ChartRef = "fabricator/charts/alloy"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.Alloy
}

//go:embed gw_values.tmpl.yaml
var gwValuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing control VIP: %w", err)
	}
	proxyURL := fmt.Sprintf("http://%s:%d", controlVIP.Addr().String(), controlproxy.NodePort)

	registryURL, err := comp.RegistryURL(cfg)
	if err != nil {
		return nil, fmt.Errorf("getting registry URL: %w", err)
	}

	tolerations, err := kyaml.Marshal([]corev1.Toleration{
		{
			Key:      fabapi.RoleTaintKey(fabapi.NodeRoleGateway),
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gateway tolerations: %w", err)
	}

	gwAlloyCfg := alloy.Config{
		ProxyURL:     proxyURL,
		AutoHostname: true,
		Targets:      cfg.Spec.Config.Observability.Targets,

		Scrapes:  map[string]alloy.Scrape{},
		LogFiles: map[string]alloy.LogFile{},
	}
	if cfg.Spec.Config.Gateway.Observability != nil {
		obs := cfg.Spec.Config.Gateway.Observability
		if obs.Dataplane.Metrics {
			gwAlloyCfg.Scrapes["dataplane"] = alloy.Scrape{
				Address:         net.JoinHostPort("127.0.0.1", strconv.Itoa(gateway.DataplaneMetricsPort)),
				IntervalSeconds: obs.Dataplane.MetricsInterval,
				Relabel:         obs.Dataplane.MetricsRelabel,
			}
		}
		if obs.FRR.Metrics {
			gwAlloyCfg.Scrapes["frr"] = alloy.Scrape{
				Address:         net.JoinHostPort("127.0.0.1", strconv.Itoa(gateway.FRRMetricsPort)),
				IntervalSeconds: obs.FRR.MetricsInterval,
				Relabel:         obs.FRR.MetricsRelabel,
			}
		}
		if obs.Unix.Metrics {
			gwAlloyCfg.Scrapes["unix"] = alloy.Scrape{
				Unix: alloy.ScrapeUnix{
					Enable:     true,
					Collectors: obs.Unix.MetricsCollectors,
				},
				IntervalSeconds: obs.Unix.MetricsInterval,
				Relabel:         obs.Unix.MetricsRelabel,
			}
		}
	}

	gwAlloyConfigData, err := gwAlloyCfg.Render()
	if err != nil {
		return nil, fmt.Errorf("gateway alloy config: %w", err)
	}

	gwAlloyValues, err := tmpl.Render("values", gwValuesTmpl, map[string]any{
		"Registry":    registryURL,
		"Image":       comp.JoinURLParts(comp.RegPrefix, ImageRef),
		"Version":     string(Version(cfg)),
		"Config":      string(gwAlloyConfigData),
		"Tolerations": string(tolerations),
	})
	if err != nil {
		return nil, fmt.Errorf("gateway alloy values: %w", err)
	}

	chartVersion := string(Version(cfg))
	gwAlloyChart, err := comp.NewHelmChart(cfg, "alloy-gw", ChartRef, chartVersion, "", false, string(gwAlloyValues))
	if err != nil {
		return nil, fmt.Errorf("gateway alloy chart: %w", err)
	}

	return []kclient.Object{
		gwAlloyChart,
	}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ImageRef: Version(cfg),
		ChartRef: Version(cfg),
	}, nil
}

var _ comp.KubeStatus = StatusGateway

func StatusGateway(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	if !cfg.Spec.Config.Gateway.Enable {
		return fabapi.CompStatusSkipped, nil
	}

	ref, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}
	image := ref + ":" + string(Version(cfg))

	return comp.GetDaemonSetStatus("alloy-gw", "alloy", image)(ctx, kube, cfg)
}
