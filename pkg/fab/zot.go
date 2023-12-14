package fab

import (
	_ "embed"
	"time"

	helm "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/manager/config"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

//go:embed zot_values.tmpl.yaml
var zotValuesTemplate string

type Zot struct {
	Ref cnc.Ref `json:"ref,omitempty"`
	TLS ZotTLS  `json:"tls,omitempty"`
}

type ZotTLS struct {
	CA     cnc.KeyPair `json:"ca,omitempty"`
	Server cnc.KeyPair `json:"server,omitempty"`
}

var _ cnc.Component = (*Zot)(nil)

func (cfg *Zot) Name() string {
	return "zot"
}

func (cfg *Zot) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *Zot) Flags() []cli.Flag {
	return nil
}

func (cfg *Zot) Hydrate(preset cnc.Preset, fabricMode config.FabricMode) error {
	cfg.Ref = cfg.Ref.Fallback(REF_ZOT)

	err := cfg.TLS.CA.Ensure(OCI_REPO_CA_CN, nil, KEY_USAGE_CA, nil, nil, nil)
	if err != nil {
		return errors.Wrapf(err, "error ensuring OCI Repo CA")
	}

	err = cfg.TLS.Server.Ensure(OCI_REPO_SERVER_CN, &cfg.TLS.CA, KEY_USAGE_SERVER, nil,
		[]string{CONTROL_VIP}, []string{"registry.local", "registry.default", "registry.default.svc.cluster.local"})
	if err != nil {
		return errors.Wrap(err, "error ensuring OCI Repo Certs")
	}

	return nil
}

func (cfg *Zot) Build(basedir string, preset cnc.Preset, fabricMode config.FabricMode, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.Ref = cfg.Ref.Fallback(BaseConfig(get).Source)

	run(BundleControlInstall, STAGE_INSTALL_0_PREP, "zot-airgap-files",
		&cnc.FilesORAS{
			Ref: cfg.Ref.Fallback(BaseConfig(get).Source),
			Files: []cnc.File{
				{
					Name:          "zot-airgap-images-amd64.tar.gz", // TODO try to switch to full image, maybe have UI
					InstallTarget: "/var/lib/rancher/k3s/agent/images",
				},
				{
					Name:          "zot.tgz", // TODO rename to zot-chart.tgz
					InstallTarget: "/var/lib/rancher/k3s/server/static/charts",
					InstallName:   "hh-zot-chart.tgz",
				},
			},
		})

	run(BundleControlInstall, STAGE_INSTALL_0_PREP, "zot-install",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "zot-install.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-zot-install.yaml",
			},
			Content: cnc.FromKubeObjects(
				cnc.KubeHelmChart("zot", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "https://%{KUBERNETES_API}%/static/charts/hh-zot-chart.tgz",
				}, cnc.FromTemplate(zotValuesTemplate, "ref", REF_ZOT_TARGET_IMAGE.Fallback(cfg.Ref))),
				cnc.KubeService("registry", "default", core.ServiceSpec{
					Type: core.ServiceTypeNodePort,
					Ports: []core.ServicePort{
						{
							Name:       "zot",
							Port:       5000,
							NodePort:   int32(ZOT_NODE_PORT),
							TargetPort: intstr.FromString("zot"),
							Protocol:   core.ProtocolTCP,
						},
					},
					Selector: map[string]string{
						"app.kubernetes.io/instance": "zot",
						"app.kubernetes.io/name":     "zot",
					},
				}),
				cnc.KubeSecret("zot-secret", "default", map[string]string{
					"cert.pem": cfg.TLS.Server.Cert,
					"key.pem":  cfg.TLS.Server.Key,
				}),
			),
		})

	run(BundleControlInstall, STAGE_INSTALL_0_PREP, "zot-ca-file",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "zot-ca.crt",
				InstallTarget: "/etc/ssl/certs",
				InstallName:   "hh-registry-ca.pem",
			},
			Content: cnc.FromValue(cfg.TLS.CA.Cert),
		})

	install(BundleControlInstall, STAGE_INSTALL_0_PREP, "zot-ca-install",
		&cnc.ExecCommand{
			Name: "update-ca-certificates",
			Args: []string{"|", "grep", "-v", "=\\>"}, // don't print all cert names
		})

	install(BundleControlInstall, STAGE_INSTALL_1_K3SZOT, "zot-wait",
		&cnc.WaitURL{
			URL: ZOT_CHECK_URL,
			Wait: cnc.WaitParams{
				Delay:    10 * time.Second,
				Interval: 5 * time.Second,
				Attempts: 120, // ~10min
			},
		})

	return nil
}

func ZotConfig(get cnc.GetComponent) *Zot {
	return get((&Zot{}).Name()).(*Zot)
}
