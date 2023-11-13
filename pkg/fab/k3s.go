package fab

import (
	_ "embed"
	"slices"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed k3s_config.tmpl.yaml
var k3sConfigTemplate string

type K3s struct {
	Ref         cnc.Ref  `json:"ref,omitempty"`
	ClusterCIDR string   `json:"clusterCIDR,omitempty"`
	ServiceCIDR string   `json:"serviceCIDR,omitempty"`
	ClusterDNS  string   `json:"clusterDNS,omitempty"`
	TLSSAN      []string `json:"tlsSAN,omitempty"`
}

var _ cnc.Component = (*K3s)(nil)

func (cfg *K3s) Name() string {
	return "k3s"
}

func (cfg *K3s) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *K3s) Flags() []cli.Flag {
	return nil
}

func (cfg *K3s) Hydrate(preset cnc.Preset) error {
	cfg.Ref = cfg.Ref.Fallback(REF_K3S)

	if cfg.ClusterCIDR == "" {
		cfg.ClusterCIDR = CONTROL_KUBE_CLUSTER_CIDR
	}

	if cfg.ServiceCIDR == "" {
		cfg.ServiceCIDR = CONTROL_KUBE_SERVICE_CIDR
	}

	if cfg.ClusterDNS == "" {
		cfg.ClusterDNS = CONTROL_KUBE_CLUSTER_DNS
	}

	for _, tlsSAN := range []string{
		"127.0.0.1",
		"kube-fabric.local",
		CONTROL_VIP,
		// TODO add configurable SANs
	} {
		if !slices.Contains(cfg.TLSSAN, tlsSAN) {
			cfg.TLSSAN = append(cfg.TLSSAN, tlsSAN)
		}
	}

	return nil
}

func (cfg *K3s) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.Ref = cfg.Ref.Fallback(BaseConfig(get).Source)

	run(BundleControlInstall, STAGE_INSTALL_0_PREP, "k3s-airgap-files",
		&cnc.FilesORAS{
			Ref: cfg.Ref,
			Files: []cnc.File{
				{
					Name:          "k3s-install", // TODO rename to k3s-install.sh
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

	run(BundleControlInstall, STAGE_INSTALL_0_PREP, "k3s-config",
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

	install(BundleControlInstall, STAGE_INSTALL_1_K3SZOT, "k3s-airgap-install",
		&cnc.ExecCommand{
			Name: "k3s-install",
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
			} else {
				name = server.Name
			}
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
