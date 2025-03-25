// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package k3s

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	Ref                = "fabricator/k3s-airgap"
	BinName            = "k3s"
	BinDir             = "/opt/bin"
	InstallName        = "k3s-install.sh"
	AirgapName         = "k3s-airgap-images-amd64.tar.gz"
	AgentDir           = "/var/lib/rancher/k3s/agent"
	ImagesDir          = "/var/lib/rancher/k3s/agent/images"
	ServerDir          = "/var/lib/rancher/k3s/server"
	ChartsDir          = "/var/lib/rancher/k3s/server/static/" + comp.BootstrapChartsPrefix
	ServerServiceName  = "k3s.service"
	AgentServiceName   = "k3s-agent.service"
	APIPort            = 6443
	ConfigDir          = "/etc/rancher/k3s"
	ConfigPath         = "/etc/rancher/k3s/config.yaml"
	KubeConfigPath     = "/etc/rancher/k3s/k3s.yaml"
	KubeRegistriesPath = "/etc/rancher/k3s/registries.yaml"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.K3s
}

func KubeVersion(f fabapi.Fabricator) string {
	return strings.ReplaceAll(string(f.Status.Versions.Platform.K3s), "-", "+")
}

//go:embed server_config.tmpl.yaml
var k3sServerConfigTmpl string

func ServerConfig(f fabapi.Fabricator, control fabapi.ControlNode) (string, error) {
	controlVIP, err := f.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return "", fmt.Errorf("parsing control VIP: %w", err)
	}

	tlsSAN := f.Spec.Config.Control.TLSSAN
	if !slices.Contains(tlsSAN, controlVIP.Addr().String()) {
		tlsSAN = append(tlsSAN, controlVIP.Addr().String())
	}

	nodeIP, err := control.Spec.Management.IP.Parse()
	if err != nil {
		return "", fmt.Errorf("parsing control node IP: %w", err)
	}

	cfg, err := tmplutil.FromTemplate("k3s-server-config", k3sServerConfigTmpl, map[string]any{
		"Name":          control.Name,
		"NodeIP":        nodeIP.Addr(),
		"FlannelIface":  control.Spec.Management.Interface,
		"ClusterSubnet": f.Spec.Config.Control.KubeClusterSubnet,
		"ServiceSubnet": f.Spec.Config.Control.KubeServiceSubnet,
		"ClusterDNS":    f.Spec.Config.Control.KubeClusterDNS,
		"TLSSAN":        tlsSAN,
	})
	if err != nil {
		return "", fmt.Errorf("k3s config: %w", err)
	}

	return cfg, nil
}

//go:embed agent_config.tmpl.yaml
var k3sAgentConfigTmpl string

func AgentConfig(_ fabapi.Fabricator, node fabapi.FabNode) (string, error) {
	nodeIP, err := node.Spec.Management.IP.Parse()
	if err != nil {
		return "", fmt.Errorf("parsing control node IP: %w", err)
	}

	cfg, err := tmplutil.FromTemplate("k3s-agent-config", k3sAgentConfigTmpl, map[string]any{
		"Name":         node.Name,
		"NodeIP":       nodeIP.Addr(),
		"FlannelIface": node.Spec.Management.Interface,
	})
	if err != nil {
		return "", fmt.Errorf("k3s config: %w", err)
	}

	return cfg, nil
}

//go:embed registries.tmpl.yaml
var registriesTmpl string

func Registries(f fabapi.Fabricator, username, password string) (string, error) {
	reg, err := comp.RegistryURL(f)
	if err != nil {
		return "", fmt.Errorf("getting registry URL: %w", err)
	}

	return RegistriesFor(reg, username, password)
}

func RegistriesFor(regURL string, username, password string) (string, error) {
	cfg, err := tmplutil.FromTemplate("registries", registriesTmpl, map[string]any{
		"Registry": regURL,
		"Username": username,
		"Password": password,
	})
	if err != nil {
		return "", fmt.Errorf("registries: %w", err)
	}

	return cfg, nil
}

func InstallNodeRegistries(username, password string) comp.KubeInstall {
	return func(f fabapi.Fabricator) ([]client.Object, error) {
		regs, err := Registries(f, username, password)
		if err != nil {
			return nil, fmt.Errorf("getting registries: %w", err)
		}

		return []client.Object{
			comp.NewSecret(comp.FabNodeRegistriesSecret, comp.SecretTypeOpaque, map[string]string{
				comp.FabNodeRegistriesSecretKey: regs,
			}),
		}, nil
	}
}
