package fab

import (
	_ "embed"
	"fmt"
	"strings"

	helm "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed dasboot_rsyslog.tmpl.yaml
var dasBootRsyslogValuesTemplate string

//go:embed dasboot_ntp.tmpl.yaml
var dasBootNtpValuesTemplate string

//go:embed dasboot_seeder.tmpl.yaml
var dasBootSeederValuesTemplate string

//go:embed dasboot_reg_ctrl.tmpl.yaml
var dasBootRegCtrlValuesTemplate string

type DasBoot struct {
	Ref             cnc.Ref    `json:"ref,omitempty"`
	RsyslogChartRef cnc.Ref    `json:"rsyslogChartRef,omitempty"`
	RsyslogImageRef cnc.Ref    `json:"rsyslogImageRef,omitempty"`
	NTPChartRef     cnc.Ref    `json:"ntpChartRef,omitempty"`
	NTPImageRef     cnc.Ref    `json:"ntpImageRef,omitempty"`
	CRDsChartRef    cnc.Ref    `json:"crdsChartRef,omitempty"`
	SeederChartRef  cnc.Ref    `json:"seederChartRef,omitempty"`
	SeederImageRef  cnc.Ref    `json:"seederImageRef,omitempty"`
	RegCtrlChartRef cnc.Ref    `json:"regCtrlChartRef,omitempty"`
	RegCtrlImageRef cnc.Ref    `json:"regCtrlImageRef,omitempty"`
	SONiCBaseRef    cnc.Ref    `json:"sonicBaseRef,omitempty"`
	SONiCVSRef      cnc.Ref    `json:"sonicVSRef,omitempty"`
	TLS             DasBootTLS `json:"tls,omitempty"`
	ClusterIP       string     `json:"clusterIP,omitempty"`
}

type DasBootTLS struct {
	ServerCA cnc.KeyPair `json:"serverCA,omitempty"`
	Server   cnc.KeyPair `json:"server,omitempty"`
	ClientCA cnc.KeyPair `json:"clientCA,omitempty"`
	ConfigCA cnc.KeyPair `json:"configCA,omitempty"`
	Config   cnc.KeyPair `json:"config,omitempty"`
}

var _ cnc.Component = (*DasBoot)(nil)

func (cfg *DasBoot) Name() string {
	return "das-boot"
}

func (cfg *DasBoot) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *DasBoot) Flags() []cli.Flag {
	return nil
}

func (cfg *DasBoot) Hydrate(preset cnc.Preset) error {
	cfg.Ref = cfg.Ref.Fallback(REF_DASBOOT_VERSION)
	cfg.RsyslogChartRef = cfg.RsyslogChartRef.Fallback(REF_DASBOOT_RSYSLOG_CHART)
	cfg.RsyslogImageRef = cfg.RsyslogImageRef.Fallback(REF_DASBOOT_RSYSLOG_IMAGE)
	cfg.NTPChartRef = cfg.NTPChartRef.Fallback(REF_DASBOOT_NTP_CHART)
	cfg.NTPImageRef = cfg.NTPImageRef.Fallback(REF_DASBOOT_NTP_IMAGE)
	cfg.CRDsChartRef = cfg.CRDsChartRef.Fallback(REF_DASBOOT_CRDS_CHART)
	cfg.SeederChartRef = cfg.SeederChartRef.Fallback(REF_DASBOOT_SEEDER_CHART)
	cfg.SeederImageRef = cfg.SeederImageRef.Fallback(REF_DASBOOT_SEEDER_IMAGE)
	cfg.RegCtrlChartRef = cfg.RegCtrlChartRef.Fallback(REF_DASBOOT_REGCTRL_CHART)
	cfg.RegCtrlImageRef = cfg.RegCtrlImageRef.Fallback(REF_DASBOOT_REGCTRL_IMAGE)
	cfg.SONiCBaseRef = cfg.SONiCBaseRef.Fallback(REF_SONIC_BCOM_BASE)
	cfg.SONiCVSRef = cfg.SONiCVSRef.Fallback(REF_SONIC_BCOM_VS)

	err := cfg.TLS.ServerCA.Ensure("DAS BOOT Server CA", nil, KEY_USAGE_CA, nil, nil, nil) // TODO key usage
	if err != nil {
		return errors.Wrapf(err, "error ensuring OCI Repo CA") // TODO
	}

	err = cfg.TLS.Server.Ensure("localhost", &cfg.TLS.ServerCA, KEY_USAGE_SERVER, nil,
		[]string{CONTROL_VIP},
		[]string{"das-boot-seeder.default.svc.cluster.local"}, // TODO
	) // TODO config and key usage
	if err != nil {
		return errors.Wrap(err, "error ensuring OCI Repo Certs") // TODO
	}

	err = cfg.TLS.ClientCA.Ensure("DAS BOOT Client CA", nil, KEY_USAGE_CA, nil, nil, nil) // TODO key usage
	if err != nil {
		return errors.Wrapf(err, "error ensuring OCI Repo CA") // TODO
	}

	err = cfg.TLS.ConfigCA.Ensure("DAS BOOT Config Signatures CA", nil, KEY_USAGE_CA, nil, nil, nil) // TODO key usage
	if err != nil {
		return errors.Wrapf(err, "error ensuring OCI Repo CA") // TODO
	}

	err = cfg.TLS.Config.Ensure("localhost", &cfg.TLS.ConfigCA, KEY_USAGE_SERVER, nil, nil, nil) // TODO config and key usage
	if err != nil {
		return errors.Wrap(err, "error ensuring OCI Repo Certs") // TODO
	}

	if cfg.ClusterIP == "" {
		cfg.ClusterIP = DAS_BOOT_SEEDER_CLUSTER_IP
	}

	return nil
}

func (cfg *DasBoot) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.RsyslogImageRef = cfg.RsyslogImageRef.Fallback(BaseConfig(get).Source)
	cfg.RsyslogChartRef = cfg.RsyslogChartRef.Fallback(BaseConfig(get).Source)
	cfg.NTPImageRef = cfg.NTPImageRef.Fallback(BaseConfig(get).Source)
	cfg.NTPChartRef = cfg.NTPChartRef.Fallback(BaseConfig(get).Source)
	cfg.CRDsChartRef = cfg.CRDsChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.SeederImageRef = cfg.SeederImageRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.SeederChartRef = cfg.SeederChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.RegCtrlImageRef = cfg.RegCtrlImageRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.RegCtrlChartRef = cfg.RegCtrlChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.SONiCBaseRef = cfg.SONiCBaseRef.Fallback(BaseConfig(get).Source)
	cfg.SONiCVSRef = cfg.SONiCVSRef.Fallback(BaseConfig(get).Source)

	target := BaseConfig(get).Target
	targetInCluster := BaseConfig(get).TargetInCluster

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-rsyslog-image",
		&cnc.SyncOCI{
			Ref:    cfg.RsyslogImageRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-rsyslog-chart",
		&cnc.SyncOCI{
			Ref:    cfg.RsyslogChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-ntp-image",
		&cnc.SyncOCI{
			Ref:    cfg.NTPImageRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-ntp-chart",
		&cnc.SyncOCI{
			Ref:    cfg.NTPChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-crds-chart",
		&cnc.SyncOCI{
			Ref:    cfg.CRDsChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-seeder-image",
		&cnc.SyncOCI{
			Ref:    cfg.SeederImageRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-seeder-chart",
		&cnc.SyncOCI{
			Ref:    cfg.SeederChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-reg-ctrl-image",
		&cnc.SyncOCI{
			Ref:    cfg.RegCtrlImageRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-reg-ctrl-chart",
		&cnc.SyncOCI{
			Ref:    cfg.RegCtrlChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-install",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "dasboot-install.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-dasboot-install.yaml",
			},
			Content: cnc.FromKubeObjects(
				cnc.KubeHelmChart("das-boot-rsyslog", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + targetInCluster.Fallback(cfg.RsyslogChartRef).RepoName(),
					Version:         cfg.RsyslogChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(dasBootRsyslogValuesTemplate,
					"ref", target.Fallback(cfg.RsyslogImageRef),
					"nodePort", DAS_BOOT_SYSLOG_NODE_PORT,
				)),
				cnc.KubeHelmChart("das-boot-ntp", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + targetInCluster.Fallback(cfg.NTPChartRef).RepoName(),
					Version:         cfg.NTPChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(dasBootNtpValuesTemplate,
					"ref", target.Fallback(cfg.NTPImageRef),
					"nodePort", DAS_BOOT_NTP_NODE_PORT,
				)),
				cnc.KubeHelmChart("das-boot-crds", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + targetInCluster.Fallback(cfg.CRDsChartRef).RepoName(),
					Version:         cfg.CRDsChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
					FailurePolicy:   "abort", // very important not to re-install crd charts
				}, cnc.FromValue("")),
				cnc.KubeHelmChart("das-boot-seeder", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + targetInCluster.Fallback(cfg.SeederChartRef).RepoName(),
					Version:         cfg.SeederChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(dasBootSeederValuesTemplate,
					"ref", target.Fallback(cfg.SeederImageRef),
					"controlVIP", CONTROL_VIP+CONTROL_VIP_MASK,
					"ntpNodePort", DAS_BOOT_NTP_NODE_PORT,
					"syslogNodePort", DAS_BOOT_SYSLOG_NODE_PORT,
				)),
				cnc.KubeHelmChart("das-boot-registration-controller", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + targetInCluster.Fallback(cfg.RegCtrlChartRef).RepoName(),
					Version:         cfg.RegCtrlChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(dasBootRegCtrlValuesTemplate, "ref", target.Fallback(cfg.RegCtrlImageRef))),
				cnc.KubeSecret("das-boot-server-cert", "default", map[string]string{
					"cert.pem": cfg.TLS.Server.Cert,
					"key.pem":  cfg.TLS.Server.Key,
				}),
				cnc.KubeSecret("das-boot-config-cert", "default", map[string]string{
					"cert.pem": cfg.TLS.Config.Cert,
					"key.pem":  cfg.TLS.Config.Key,
				}),
				cnc.KubeSecret("das-boot-client-ca", "default", map[string]string{
					"cert.pem": cfg.TLS.ClientCA.Cert,
					"key.pem":  cfg.TLS.ClientCA.Key,
				}),
				cnc.KubeSecret("das-boot-server-ca", "default", map[string]string{
					"cert.pem": cfg.TLS.ServerCA.Cert,
				}),
				cnc.KubeSecret("das-boot-config-ca", "default", map[string]string{
					"cert.pem": cfg.TLS.ConfigCA.Cert,
				}),
				cnc.KubeSecret("oci-ca", "default", map[string]string{ // TODO rename
					"cert.pem": ZotConfig(get).TLS.CA.Cert,
				}),
			),
		})

	for _, sonicTarget := range REF_SONIC_TARGETS_BASE {
		run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, fmt.Sprintf("das-boot-bin-%s", strings.ReplaceAll(sonicTarget.Name, "/", "-")),
			&cnc.SyncOCI{
				Ref:    cfg.SONiCBaseRef,
				Target: target.Fallback(REF_SONIC_TARGET_VERSION, sonicTarget),
			})
	}

	if preset == PRESET_VLAB {
		for _, sonicTarget := range REF_SONIC_TARGETS_VS {
			run(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, fmt.Sprintf("das-boot-bin-%s", strings.ReplaceAll(sonicTarget.Name, "/", "-")),
				&cnc.SyncOCI{
					Ref:    cfg.SONiCVSRef,
					Target: target.Fallback(REF_SONIC_TARGET_VERSION, sonicTarget),
				})
		}
	}

	install(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-seeder-wait",
		&cnc.WaitKube{
			Name: "daemonset/das-boot-seeder",
		})

	install(BundleControlInstall, STAGE_INSTALL_4_DASBOOT, "das-boot-reg-ctrl-wait",
		&cnc.WaitKube{
			Name: "deployment/das-boot-registration-controller",
		})

	return nil
}
