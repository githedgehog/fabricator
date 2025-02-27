// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package k3s

import (
	_ "embed"
	"fmt"
	"slices"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
)

const (
	Ref                = "fabricator/k3s-airgap"
	BinName            = "k3s"
	BinDir             = "/opt/bin"
	InstallName        = "k3s-install.sh"
	AirgapName         = "k3s-airgap-images-amd64.tar.gz"
	ImagesDir          = "/var/lib/rancher/k3s/agent/images"
	ChartsDir          = "/var/lib/rancher/k3s/server/static/" + comp.BootstrapChartsPrefix
	APIPort            = 6443
	ConfigDir          = "/etc/rancher/k3s"
	ConfigPath         = "/etc/rancher/k3s/config.yaml"
	KubeConfigPath     = "/etc/rancher/k3s/k3s.yaml"
	KubeRegistriesPath = "/etc/rancher/k3s/registries.yaml"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.K3s
}

//go:embed config.tmpl.yaml
var k3sConfigTmpl string

//go:embed registries.tmpl.yaml
var registriesTmpl string

func Config(f fabapi.Fabricator, control fabapi.ControlNode) (string, error) {
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

	cfg, err := tmplutil.FromTemplate("k3s-config", k3sConfigTmpl, map[string]any{
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

func Registries(f fabapi.Fabricator, username, password string) (string, error) {
	reg, err := comp.RegistryURL(f)
	if err != nil {
		return "", fmt.Errorf("getting registry URL: %w", err)
	}

	cfg, err := tmplutil.FromTemplate("registries", registriesTmpl, map[string]any{
		"Registry": reg,
		"Username": username,
		"Password": password,
	})
	if err != nil {
		return "", fmt.Errorf("registries: %w", err)
	}

	return cfg, nil
}
