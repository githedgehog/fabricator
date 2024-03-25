// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fab

import (
	_ "embed"
	"slices"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed k3s_config.tmpl.yaml
var k3sConfigTemplate string

type K3s struct {
	cnc.NoValidationComponent

	Ref         cnc.Ref  `json:"ref,omitempty"`
	ClusterCIDR string   `json:"clusterCIDR,omitempty"`
	ServiceCIDR string   `json:"serviceCIDR,omitempty"`
	ClusterDNS  string   `json:"clusterDNS,omitempty"`
	TLSSAN      []string `json:"tlsSAN,omitempty"`

	tlsSAN cli.StringSlice
}

var _ cnc.Component = (*K3s)(nil)

func (cfg *K3s) Name() string {
	return "k3s"
}

func (cfg *K3s) IsEnabled(_ cnc.Preset) bool {
	return true
}

func (cfg *K3s) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringSliceFlag{
			Name:        "tls-san",
			Usage:       "TLS SANs for K8s API / control plane",
			EnvVars:     []string{"HHFAB_TLS_SAN"},
			Destination: &cfg.tlsSAN,
		},
	}
}

func (cfg *K3s) Hydrate(_ cnc.Preset, _ meta.FabricMode) error {
	cfg.Ref = cfg.Ref.Fallback(RefK3s)

	if cfg.ClusterCIDR == "" {
		cfg.ClusterCIDR = ControlKubeClusterCIDR
	}

	if cfg.ServiceCIDR == "" {
		cfg.ServiceCIDR = ControlKubeServiceCIDR
	}

	if cfg.ClusterDNS == "" {
		cfg.ClusterDNS = ControlKubeClusterDNS
	}

	for _, tlsSAN := range append([]string{
		"127.0.0.1",
		"kube-fabric.local",
		ControlVIP,
	}, cfg.tlsSAN.Value()...) {
		if !slices.Contains(cfg.TLSSAN, tlsSAN) {
			cfg.TLSSAN = append(cfg.TLSSAN, tlsSAN)
		}
	}

	return nil
}

func (cfg *K3s) Build(_ string, _ cnc.Preset, _ meta.FabricMode, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.Ref = cfg.Ref.Fallback(BaseConfig(get).Source)

	run(BundleControlInstall, StageInstall0Prep, "k3s-airgap-files",
		&cnc.FilesORAS{
			Ref: cfg.Ref,
			Files: []cnc.File{
				{
					Name:          "k3s-install.sh",
					InstallTarget: "/opt/bin",
					InstallMode:   0o755,
				},
				{
					Name:          "k3s",
					InstallTarget: "/opt/bin",
					InstallMode:   0o755,
				},
				{
					Name:          "k3s-airgap-images-amd64.tar.gz",
					InstallTarget: "/var/lib/rancher/k3s/agent/images",
				},
			},
		})

	controlNodeName, err := cfg.ControlNodeName(wiring)
	if err != nil {
		return errors.Wrap(err, "error getting control node name")
	}

	run(BundleControlInstall, StageInstall0Prep, "k3s-config",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "k3s-config.yaml",
				InstallTarget: "/etc/rancher/k3s",
				InstallName:   "config.yaml",
			},
			Content: cnc.FromTemplate(k3sConfigTemplate,
				"cfg", cfg,
				"controlNodeName", controlNodeName,
			),
		})

	install(BundleControlInstall, StageInstall1K3sZot, "k3s-airgap-install",
		&cnc.ExecCommand{
			Name: "k3s-install.sh",
			Args: []string{"--disable=servicelb,traefik"},
			Env: []string{
				"INSTALL_K3S_SKIP_DOWNLOAD=true",
				"INSTALL_K3S_BIN_DIR=/opt/bin",
			},
		})

	return nil
}

func (cfg *K3s) ControlNodeName(data *wiring.Data) (string, error) {
	name := ""
	for _, server := range data.Server.All() {
		if server.Spec.Type == wiringapi.ServerTypeControl {
			if name != "" {
				return "", errors.New("multiple control nodes found")
			}

			name = server.Name
		}
	}

	if name == "" {
		return "", errors.New("no control node found")
	}

	return name, nil
}

func K3sConfig(get cnc.GetComponent) *K3s {
	return get((&K3s{}).Name()).(*K3s)
}
