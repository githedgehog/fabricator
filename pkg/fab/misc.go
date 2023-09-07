package fab

import (
	_ "embed"

	helm "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed misc_cert_manager.tmpl.yaml
var certManagerValuesTemplate string

//go:embed misc_reloader.tmpl.yaml
var reloaderValuesTemplate string

type Misc struct {
	K9sRef                   cnc.Ref `json:"k9sRef,omitempty"`
	RBACProxyImageRef        cnc.Ref `json:"rbacProxyRef,omitempty"`
	CertManagerRef           cnc.Ref `json:"certManagerRef,omitempty"`
	CertManagerCAInjectorRef cnc.Ref `json:"certManagerCAInjectorRef,omitempty"`
	CertManagerControllerRef cnc.Ref `json:"certManagerControllerRef,omitempty"`
	CertManagerAcmeSolverRef cnc.Ref `json:"certManagerAcmeSolverRef,omitempty"`
	CertManagerWebhookRef    cnc.Ref `json:"certManagerWebhookRef,omitempty"`
	CertManagerCtlRef        cnc.Ref `json:"certManagerCtlRef,omitempty"`
	CertManagerChartRef      cnc.Ref `json:"certManagerChartRef,omitempty"`
	ReloaderImageRef         cnc.Ref `json:"reloaderImageRef,omitempty"`
	ReloaderChartRef         cnc.Ref `json:"reloaderChartRef,omitempty"`
}

var _ cnc.Component = (*Misc)(nil)

func (cfg *Misc) Name() string {
	return "misc"
}

func (cfg *Misc) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *Misc) Flags() []cli.Flag {
	return nil
}

func (cfg *Misc) Hydrate(preset cnc.Preset) error {
	cfg.K9sRef = cfg.K9sRef.Fallback(REF_K9S)
	cfg.RBACProxyImageRef = cfg.RBACProxyImageRef.Fallback(REF_RBAC_PROXY)

	cfg.CertManagerRef = cfg.CertManagerRef.Fallback(REF_CERT_MANAGER_VERSION)
	cfg.CertManagerCAInjectorRef = cfg.CertManagerCAInjectorRef.Fallback(REF_CERT_MANAGER_CAINJECTOR)
	cfg.CertManagerControllerRef = cfg.CertManagerControllerRef.Fallback(REF_CERT_MANAGER_CONTROLLER)
	cfg.CertManagerAcmeSolverRef = cfg.CertManagerAcmeSolverRef.Fallback(REF_CERT_MANAGER_ACMESOLVER)
	cfg.CertManagerWebhookRef = cfg.CertManagerWebhookRef.Fallback(REF_CERT_MANAGER_WEBHOOK)
	cfg.CertManagerCtlRef = cfg.CertManagerCtlRef.Fallback(REF_CERT_MANAGER_CTL)
	cfg.CertManagerChartRef = cfg.CertManagerChartRef.Fallback(REF_CERT_MANAGER_CHART)

	cfg.ReloaderImageRef = cfg.ReloaderImageRef.Fallback(REF_MISC_RELOADER)
	cfg.ReloaderChartRef = cfg.ReloaderChartRef.Fallback(REF_MISC_RELOADER_CHART)

	return nil
}

func (cfg *Misc) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.K9sRef = cfg.K9sRef.Fallback(BaseConfig(get).Source)
	cfg.RBACProxyImageRef = cfg.RBACProxyImageRef.Fallback(BaseConfig(get).Source)

	cfg.CertManagerRef = cfg.CertManagerRef.Fallback(BaseConfig(get).Source)
	cfg.CertManagerCAInjectorRef = cfg.CertManagerCAInjectorRef.Fallback(cfg.CertManagerRef)
	cfg.CertManagerControllerRef = cfg.CertManagerControllerRef.Fallback(cfg.CertManagerRef)
	cfg.CertManagerAcmeSolverRef = cfg.CertManagerAcmeSolverRef.Fallback(cfg.CertManagerRef)
	cfg.CertManagerWebhookRef = cfg.CertManagerWebhookRef.Fallback(cfg.CertManagerRef)
	cfg.CertManagerCtlRef = cfg.CertManagerCtlRef.Fallback(cfg.CertManagerRef)
	cfg.CertManagerChartRef = cfg.CertManagerChartRef.Fallback(cfg.CertManagerRef)

	cfg.ReloaderChartRef = cfg.ReloaderChartRef.Fallback(BaseConfig(get).Source)
	cfg.ReloaderImageRef = cfg.ReloaderImageRef.Fallback(BaseConfig(get).Source)

	target := BaseConfig(get).Target

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "k9s-hhfctl",
		&cnc.FilesORAS{
			Ref: cfg.K9sRef,
			Files: []cnc.File{
				{
					Name:          "k9s",
					InstallTarget: "/opt/bin",
					InstallMode:   0o755,
				},
			},
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "kube-rbac-proxy-image",
		&cnc.SyncOCI{
			Ref:    cfg.RBACProxyImageRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "cert-manager-cainjector-image",
		&cnc.SyncOCI{
			Ref:    cfg.CertManagerCAInjectorRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "cert-manager-controller-image",
		&cnc.SyncOCI{
			Ref:    cfg.CertManagerControllerRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "cert-manager-webhook-image",
		&cnc.SyncOCI{
			Ref:    cfg.CertManagerWebhookRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "cert-manager-ctl-image",
		&cnc.SyncOCI{
			Ref:    cfg.CertManagerCtlRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "cert-manager-chart",
		&cnc.SyncOCI{
			Ref:    cfg.CertManagerChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "cert-manager-install",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "cert-manager-install.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-cert-manager-install.yaml",
			},
			Content: cnc.FromKubeObjects(
				cnc.KubeHelmChart("cert-manager", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + target.Fallback(cfg.CertManagerChartRef).RepoName(),
					Version:         cfg.CertManagerChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
					FailurePolicy:   "abort", // very important not to re-install crd charts
				}, cnc.FromTemplate(certManagerValuesTemplate,
					"cainjectorRef", target.Fallback(cfg.CertManagerCAInjectorRef),
					"controllerRef", target.Fallback(cfg.CertManagerControllerRef),
					"acmesolverRef", target.Fallback(cfg.CertManagerAcmeSolverRef),
					"webhookRef", target.Fallback(cfg.CertManagerWebhookRef),
					"ctlRef", target.Fallback(cfg.CertManagerCtlRef),
				),
				)),
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "reloader-image",
		&cnc.SyncOCI{
			Ref:    cfg.ReloaderImageRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "reloader-chart",
		&cnc.SyncOCI{
			Ref:    cfg.ReloaderChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_2_MISC, "reloader-install",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "reloader-install.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-reloader-install.yaml",
			},
			Content: cnc.FromKubeObjects(
				cnc.KubeHelmChart("reloader", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + target.Fallback(cfg.ReloaderChartRef).RepoName(),
					Version:         cfg.ReloaderChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(reloaderValuesTemplate, "ref", target.Fallback(cfg.ReloaderImageRef)),
				)),
		})

	install(BundleControlInstall, STAGE_INSTALL_2_MISC, "cert-manager-wait",
		&cnc.WaitKube{
			Name: "deployment/cert-manager",
		})

	install(BundleControlInstall, STAGE_INSTALL_2_MISC, "reloader-wait",
		&cnc.WaitKube{
			Name: "deployment/reloader-reloader",
		})

	return nil
}

func MiscConfig(get cnc.GetComponent) *Misc {
	return get((&Misc{}).Name()).(*Misc)
}
