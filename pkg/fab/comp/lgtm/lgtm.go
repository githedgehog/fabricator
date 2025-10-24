// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package lgtm

import (
	_ "embed"
	"fmt"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	coreapi "k8s.io/api/core/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

const (
	Namespace = "lgtm"

	// Chart references
	GrafanaChartRef    = "lgtm/charts/grafana"
	LokiChartRef       = "lgtm/charts/loki"
	TempoChartRef      = "lgtm/charts/tempo"
	PrometheusChartRef = "lgtm/charts/prometheus"

	// Image references
	GrafanaImageRef    = "lgtm/images/grafana"
	LokiImageRef       = "lgtm/images/loki"
	TempoImageRef      = "lgtm/images/tempo"
	PrometheusImageRef = "lgtm/images/prometheus"
)

// Split image URL into registry and repository parts
func SplitImageURL(imageURL string) (string, string) {
	parts := strings.SplitN(imageURL, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	// Fallback in case of unexpected format
	return "", imageURL
}

// Indent every line by n spaces for YAML block injection
func IndentYAMLBlock(yaml string, spaces int) string {
	indent := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(yaml, "\n"), "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}

	return strings.Join(lines, "\n")
}

func Install(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	res := []kclient.Object{}

	// Create lgtm namespace
	res = append(res, comp.NewNamespace(Namespace))

	// Node selector to run only on observability nodes
	nodeSelector := map[string]string{
		fabapi.RoleLabelKey(fabapi.NodeRoleObservability): "true",
	}

	// Tolerations for observability node taints
	tolerations := []coreapi.Toleration{
		{
			Key:      fabapi.RoleTaintKey(fabapi.NodeRoleObservability),
			Operator: coreapi.TolerationOpExists,
			Effect:   coreapi.TaintEffectNoExecute,
		},
	}

	tolerationsYAML, err := kyaml.Marshal(tolerations)
	if err != nil {
		return nil, fmt.Errorf("marshaling tolerations: %w", err)
	}
	tolerationsBlock := IndentYAMLBlock(string(tolerationsYAML), 4)

	nodeSelectorYAML, err := kyaml.Marshal(nodeSelector)
	if err != nil {
		return nil, fmt.Errorf("marshaling node selector: %w", err)
	}
	nodeSelectorBlock := IndentYAMLBlock(string(nodeSelectorYAML), 4)

	nodeSelectorPromYAML, err := kyaml.Marshal(nodeSelector)
	if err != nil {
		return nil, fmt.Errorf("marshaling prometheus node selector: %w", err)
	}
	nodeSelectorPromBlock := IndentYAMLBlock(string(nodeSelectorPromYAML), 6)

	// If no subcomponents are explicitly enabled in config, default-enable all
	if !cfg.Spec.Config.LGTM.Grafana.Enabled && !cfg.Spec.Config.LGTM.Loki.Enabled && !cfg.Spec.Config.LGTM.Tempo.Enabled && !cfg.Spec.Config.LGTM.Prometheus.Enabled {
		cfg.Spec.Config.LGTM.Grafana.Enabled = true
		cfg.Spec.Config.LGTM.Loki.Enabled = true
		cfg.Spec.Config.LGTM.Tempo.Enabled = true
		cfg.Spec.Config.LGTM.Prometheus.Enabled = true
	}

	// Deploy Grafana
	if cfg.Spec.Config.LGTM.Grafana.Enabled {
		grafanaRepo, err := comp.ImageURL(cfg, GrafanaImageRef)
		if err != nil {
			return nil, fmt.Errorf("getting image URL for %q: %w", GrafanaImageRef, err)
		}
		grafanaRegistry, grafanaRepository := SplitImageURL(grafanaRepo)

		// Default admin credentials if not specified
		adminUser := "admin"
		adminPassword := "admin"
		if cfg.Spec.Config.LGTM.Grafana.AdminUser != "" {
			adminUser = cfg.Spec.Config.LGTM.Grafana.AdminUser
		}
		if cfg.Spec.Config.LGTM.Grafana.AdminPassword != "" {
			adminPassword = cfg.Spec.Config.LGTM.Grafana.AdminPassword
		}

		// Prepare dashboards map
		dashboards := map[string]string{
			"crm":           grafanaDashboardCRM,
			"fabric":        grafanaDashboardFabric,
			"interfaces":    grafanaDashboardInterfaces,
			"logs":          grafanaDashboardLogs,
			"node-exporter": grafanaDashboardNodeExporter,
			"platform":      grafanaDashboardPlatform,
		}

		grafanaValues, err := tmplutil.FromTemplate("grafana-values", grafanaValuesTmpl, map[string]any{
			"Registry":      grafanaRegistry,
			"Repository":    grafanaRepository,
			"Tag":           string(cfg.Status.Versions.LGTM.Grafana),
			"NodeSelector":  nodeSelectorBlock,
			"Tolerations":   tolerationsBlock,
			"Namespace":     Namespace,
			"AdminUser":     adminUser,
			"AdminPassword": adminPassword,
			"Dashboards":    dashboards,
		})
		if err != nil {
			return nil, fmt.Errorf("generating grafana values: %w", err)
		}

		grafanaChart, err := comp.NewHelmChartWithNamespace(
			cfg, "grafana", Namespace,
			GrafanaChartRef,
			string(cfg.Status.Versions.LGTM.Grafana),
			"", false, grafanaValues,
		)
		if err != nil {
			return nil, fmt.Errorf("creating Grafana chart: %w", err)
		}
		res = append(res, grafanaChart)
	}

	// Deploy Loki with gateway nginx.conf override
	if cfg.Spec.Config.LGTM.Loki.Enabled {
		lokiRepo, err := comp.ImageURL(cfg, LokiImageRef)
		if err != nil {
			return nil, fmt.Errorf("getting image URL for %q: %w", LokiImageRef, err)
		}
		lokiRegistry, lokiRepository := SplitImageURL(lokiRepo)

		lokiValues, err := tmplutil.FromTemplate("loki-values", lokiValuesTmpl, map[string]any{
			"Registry":     lokiRegistry,
			"Repository":   lokiRepository,
			"Tag":          string(cfg.Status.Versions.LGTM.Loki),
			"NodeSelector": nodeSelectorBlock,
			"Tolerations":  tolerationsBlock,
		})
		if err != nil {
			return nil, fmt.Errorf("generating loki values: %w", err)
		}

		lokiChart, err := comp.NewHelmChartWithNamespace(
			cfg, "loki", Namespace,
			LokiChartRef,
			string(cfg.Status.Versions.LGTM.Loki),
			"", false, lokiValues,
		)
		if err != nil {
			return nil, fmt.Errorf("creating Loki chart: %w", err)
		}
		res = append(res, lokiChart)
	}

	// Deploy Tempo
	if cfg.Spec.Config.LGTM.Tempo.Enabled {
		tempoRepo, err := comp.ImageURL(cfg, TempoImageRef)
		if err != nil {
			return nil, fmt.Errorf("getting image URL for %q: %w", TempoImageRef, err)
		}
		tempoRegistry, tempoRepository := SplitImageURL(tempoRepo)

		tempoValues, err := tmplutil.FromTemplate("tempo-values", tempoValuesTmpl, map[string]any{
			"Registry":     tempoRegistry,
			"Repository":   tempoRepository,
			"Tag":          string(cfg.Status.Versions.LGTM.Tempo),
			"NodeSelector": nodeSelectorBlock,
			"Tolerations":  tolerationsBlock,
		})
		if err != nil {
			return nil, fmt.Errorf("generating tempo values: %w", err)
		}

		tempoChart, err := comp.NewHelmChartWithNamespace(
			cfg, "tempo", Namespace,
			TempoChartRef,
			string(cfg.Status.Versions.LGTM.Tempo),
			"", false, tempoValues,
		)
		if err != nil {
			return nil, fmt.Errorf("creating Tempo chart: %w", err)
		}
		res = append(res, tempoChart)
	}

	// Deploy Prometheus
	if cfg.Spec.Config.LGTM.Prometheus.Enabled {
		promRepo, err := comp.ImageURL(cfg, PrometheusImageRef)
		if err != nil {
			return nil, fmt.Errorf("getting image URL for %q: %w", PrometheusImageRef, err)
		}
		promRegistry, promRepository := SplitImageURL(promRepo)

		promValues, err := tmplutil.FromTemplate("prometheus-values", prometheusValuesTmpl, map[string]any{
			"Registry":          promRegistry,
			"Repository":        promRepository,
			"Tag":               string(cfg.Status.Versions.LGTM.Prometheus),
			"NodeSelectorBlock": nodeSelectorPromBlock,
			"TolerationsBlock":  tolerationsBlock,
			"Namespace":         Namespace,
		})
		if err != nil {
			return nil, fmt.Errorf("generating prometheus values: %w", err)
		}

		promChart, err := comp.NewHelmChartWithNamespace(
			cfg, "prometheus", Namespace,
			PrometheusChartRef,
			string(cfg.Status.Versions.LGTM.Prometheus),
			"", false, promValues,
		)
		if err != nil {
			return nil, fmt.Errorf("creating Prometheus chart: %w", err)
		}
		res = append(res, promChart)
	}

	return res, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	// If no subcomponents are explicitly enabled in config, default-enable all
	if !cfg.Spec.Config.LGTM.Grafana.Enabled && !cfg.Spec.Config.LGTM.Loki.Enabled && !cfg.Spec.Config.LGTM.Tempo.Enabled && !cfg.Spec.Config.LGTM.Prometheus.Enabled {
		cfg.Spec.Config.LGTM.Grafana.Enabled = true
		cfg.Spec.Config.LGTM.Loki.Enabled = true
		cfg.Spec.Config.LGTM.Tempo.Enabled = true
		cfg.Spec.Config.LGTM.Prometheus.Enabled = true
	}

	arts := comp.OCIArtifacts{}

	if cfg.Spec.Config.LGTM.Grafana.Enabled {
		arts[GrafanaChartRef] = cfg.Status.Versions.LGTM.Grafana
		arts[GrafanaImageRef] = cfg.Status.Versions.LGTM.Grafana
	}
	if cfg.Spec.Config.LGTM.Loki.Enabled {
		arts[LokiChartRef] = cfg.Status.Versions.LGTM.Loki
		arts[LokiImageRef] = cfg.Status.Versions.LGTM.Loki
	}
	if cfg.Spec.Config.LGTM.Tempo.Enabled {
		arts[TempoChartRef] = cfg.Status.Versions.LGTM.Tempo
		arts[TempoImageRef] = cfg.Status.Versions.LGTM.Tempo
	}
	if cfg.Spec.Config.LGTM.Prometheus.Enabled {
		arts[PrometheusChartRef] = cfg.Status.Versions.LGTM.Prometheus
		arts[PrometheusImageRef] = cfg.Status.Versions.LGTM.Prometheus
	}

	return arts, nil
}

// TODO: Add status check functions similar to other components
// var _ comp.KubeStatus = StatusGrafana
// var _ comp.KubeStatus = StatusLoki
// etc.

//go:embed grafana.values.tmpl.yaml
var grafanaValuesTmpl string

//go:embed loki.values.tmpl.yaml
var lokiValuesTmpl string

//go:embed tempo.values.tmpl.yaml
var tempoValuesTmpl string

//go:embed prometheus.values.tmpl.yaml
var prometheusValuesTmpl string

//go:embed grafana_crm.json
var grafanaDashboardCRM string

//go:embed grafana_fabric.json
var grafanaDashboardFabric string

//go:embed grafana_interfaces.json
var grafanaDashboardInterfaces string

//go:embed grafana_logs.json
var grafanaDashboardLogs string

//go:embed grafana_node_exporter.json
var grafanaDashboardNodeExporter string

//go:embed grafana_platform.json
var grafanaDashboardPlatform string
