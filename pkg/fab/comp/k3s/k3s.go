package k3s

import (
	_ "embed"
	"fmt"
	"slices"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
)

const (
	Ref                   = "fabricator/k3s-airgap"
	BinName               = "k3s"
	BinDir                = "/opt/bin"
	InstallName           = "k3s-install.sh"
	AirgapName            = "k3s-airgap-images-amd64.tar.gz"
	ImagesDir             = "/var/lib/rancher/k3s/agent/images"
	BootstrapChartsPrefix = "charts"
	ChartsDir             = "/var/lib/rancher/k3s/server/static/" + BootstrapChartsPrefix
	APIPort               = 6443
	ConfigDir             = "/etc/rancher/k3s"
	ConfigPath            = "/etc/rancher/k3s/config.yaml"
	KubeConfigPath        = "/etc/rancher/k3s/k3s.yaml"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.K3s
}

//go:embed config.tmpl.yaml
var k3sConfigTmpl string

func Config(f fabapi.Fabricator, control fabapi.ControlNode) (string, error) {
	tlsSAN := f.Spec.Config.Control.TLSSAN
	if !slices.Contains(tlsSAN, string(f.Spec.Config.Control.VIP)) {
		tlsSAN = append(tlsSAN, string(f.Spec.Config.Control.VIP))
	}

	cfg, err := tmplutil.FromTemplate("k3s-config", k3sConfigTmpl, map[string]any{
		"Name":          control.Name,
		"NodeIP":        control.Spec.Management.IP,
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
