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
	"fmt"
	"strings"

	helm "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
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
	cnc.NoValidationComponent

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
	SONiCCampusRef  cnc.Ref    `json:"sonicCampusRef,omitempty"`
	SONiCVSRef      cnc.Ref    `json:"sonicVSRef,omitempty"`
	TLS             DasBootTLS `json:"tls,omitempty"`
	ClusterIP       string     `json:"clusterIP,omitempty"`
	NTPServers      string     `json:"ntpServers,omitempty"`
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

func (cfg *DasBoot) IsEnabled(_ cnc.Preset) bool {
	return true
}

func (cfg *DasBoot) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "ntp-servers",
			Usage:       "Upstream NTP servers (comma-separated list)",
			Destination: &cfg.NTPServers,
			Value:       "time.cloudflare.com,time1.google.com,time2.google.com,time3.google.com,time4.google.com",
		},
	}
}

func (cfg *DasBoot) Hydrate(_ cnc.Preset, _ meta.FabricMode) error {
	cfg.Ref = cfg.Ref.Fallback(RefDasBootVersion)
	cfg.RsyslogChartRef = cfg.RsyslogChartRef.Fallback(RefDasBootRsyslogChart)
	cfg.RsyslogImageRef = cfg.RsyslogImageRef.Fallback(RefDasBootRsyslogImage)
	cfg.NTPChartRef = cfg.NTPChartRef.Fallback(RefDasBootNTPChart)
	cfg.NTPImageRef = cfg.NTPImageRef.Fallback(RefDasBootNTPImage)
	cfg.CRDsChartRef = cfg.CRDsChartRef.Fallback(RefDasBootCRDsChart)
	cfg.SeederChartRef = cfg.SeederChartRef.Fallback(RefDasBootSeederChart)
	cfg.SeederImageRef = cfg.SeederImageRef.Fallback(RefDasBootSeederImage)
	cfg.RegCtrlChartRef = cfg.RegCtrlChartRef.Fallback(RefDasBootRegCtrlChart)
	cfg.RegCtrlImageRef = cfg.RegCtrlImageRef.Fallback(RefDasBootRegCtrlImage)
	cfg.SONiCBaseRef = cfg.SONiCBaseRef.Fallback(RefSonicBCMBase)
	cfg.SONiCCampusRef = cfg.SONiCCampusRef.Fallback(RefSonicBCMCampus)
	cfg.SONiCVSRef = cfg.SONiCVSRef.Fallback(RefSonicBCMVS)

	err := cfg.TLS.ServerCA.Ensure("DAS BOOT Server CA", nil, KeyUsageCA, nil, nil, nil) // TODO key usage
	if err != nil {
		return errors.Wrapf(err, "error ensuring OCI Repo CA") // TODO
	}

	err = cfg.TLS.Server.Ensure("localhost", &cfg.TLS.ServerCA, KeyUsageServer, nil,
		[]string{ControlVIP},
		[]string{"das-boot-seeder.default.svc.cluster.local"}, // TODO
	) // TODO config and key usage
	if err != nil {
		return errors.Wrap(err, "error ensuring OCI Repo Certs") // TODO
	}

	err = cfg.TLS.ClientCA.Ensure("DAS BOOT Client CA", nil, KeyUsageCA, nil, nil, nil) // TODO key usage
	if err != nil {
		return errors.Wrapf(err, "error ensuring OCI Repo CA") // TODO
	}

	err = cfg.TLS.ConfigCA.Ensure("DAS BOOT Config Signatures CA", nil, KeyUsageCA, nil, nil, nil) // TODO key usage
	if err != nil {
		return errors.Wrapf(err, "error ensuring OCI Repo CA") // TODO
	}

	err = cfg.TLS.Config.Ensure("localhost", &cfg.TLS.ConfigCA, KeyUsageServer, nil, nil, nil) // TODO config and key usage
	if err != nil {
		return errors.Wrap(err, "error ensuring OCI Repo Certs") // TODO
	}

	if cfg.ClusterIP == "" {
		cfg.ClusterIP = DasBootSeederClusterIP
	}

	return nil
}

func (cfg *DasBoot) Build(_ string, preset cnc.Preset, _ meta.FabricMode, get cnc.GetComponent, _ *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
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
	cfg.SONiCCampusRef = cfg.SONiCCampusRef.Fallback(BaseConfig(get).Source)
	cfg.SONiCVSRef = cfg.SONiCVSRef.Fallback(BaseConfig(get).Source)

	source := BaseConfig(get).Source
	target := BaseConfig(get).Target
	targetInCluster := BaseConfig(get).TargetInCluster

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-rsyslog-image",
		&cnc.SyncOCI{
			Ref:    cfg.RsyslogImageRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-rsyslog-chart",
		&cnc.SyncOCI{
			Ref:    cfg.RsyslogChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-ntp-image",
		&cnc.SyncOCI{
			Ref:    cfg.NTPImageRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-ntp-chart",
		&cnc.SyncOCI{
			Ref:    cfg.NTPChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-crds-chart",
		&cnc.SyncOCI{
			Ref:    cfg.CRDsChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-seeder-image",
		&cnc.SyncOCI{
			Ref:    cfg.SeederImageRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-seeder-chart",
		&cnc.SyncOCI{
			Ref:    cfg.SeederChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-reg-ctrl-image",
		&cnc.SyncOCI{
			Ref:    cfg.RegCtrlImageRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-reg-ctrl-chart",
		&cnc.SyncOCI{
			Ref:    cfg.RegCtrlChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall4DasBoot, "das-boot-install",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "dasboot-install.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-dasboot-install.yaml",
			},
			Content: cnc.FromKubeObjects(
				cnc.KubeHelmChart("das-boot-rsyslog", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.RsyslogChartRef).RepoName(),
					Version:         cfg.RsyslogChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(dasBootRsyslogValuesTemplate,
					"ref", target.Fallback(cfg.RsyslogImageRef),
					"nodePort", DasBootSyslogNodePort,
				)),
				cnc.KubeHelmChart("das-boot-ntp", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.NTPChartRef).RepoName(),
					Version:         cfg.NTPChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(dasBootNtpValuesTemplate,
					"ref", target.Fallback(cfg.NTPImageRef),
					"nodePort", DasBootNTPNodePort,
					"hostNetwork", "true",
					"ntpServers", cfg.NTPServers,
				)),
				cnc.KubeHelmChart("das-boot-crds", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.CRDsChartRef).RepoName(),
					Version:         cfg.CRDsChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
					FailurePolicy:   "abort", // very important not to re-install crd charts
				}, cnc.FromValue("")),
				cnc.KubeHelmChart("das-boot-seeder", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.SeederChartRef).RepoName(),
					Version:         cfg.SeederChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(dasBootSeederValuesTemplate,
					"ref", target.Fallback(cfg.SeederImageRef),
					"controlVIP", ControlVIP,
					"ntpNodePort", DasBootNTPNodePort,
					"syslogNodePort", DasBootSyslogNodePort,
				)),
				cnc.KubeHelmChart("das-boot-registration-controller", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.RegCtrlChartRef).RepoName(),
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

	for _, srcTargetsPair := range RefONIESrcTargetsPairs {
		for _, srcTargetsPairTarget := range srcTargetsPair.targets {
			run(BundleControlInstall, StageInstall4DasBoot, fmt.Sprintf("honie-%s", strings.ReplaceAll(srcTargetsPairTarget.Name, "/", "-")),
				&cnc.SyncOCI{
					Ref:    srcTargetsPair.src.Fallback(source, RefHONIEVersion),
					Target: srcTargetsPairTarget.Fallback(target, RefONIETargetVersion),
				})
		}
	}

	for _, sonicTarget := range RefSonicTargetsBase {
		run(BundleControlInstall, StageInstall4DasBoot, fmt.Sprintf("das-boot-bin-%s", strings.ReplaceAll(sonicTarget.Name, "/", "-")),
			&cnc.SyncOCI{
				Ref:    cfg.SONiCBaseRef,
				Target: target.Fallback(RefSonicTargetVersion, sonicTarget),
			})
	}
	for _, sonicTarget := range RefSonicTargetsCampus {
		run(BundleControlInstall, StageInstall4DasBoot, fmt.Sprintf("das-boot-bin-%s", strings.ReplaceAll(sonicTarget.Name, "/", "-")),
			&cnc.SyncOCI{
				Ref:    cfg.SONiCCampusRef,
				Target: target.Fallback(RefSonicTargetVersion, sonicTarget),
			})
	}

	if preset == PresetVLAB {
		for _, sonicTarget := range RefSonicTargetsVS {
			run(BundleControlInstall, StageInstall4DasBoot, fmt.Sprintf("das-boot-bin-%s", strings.ReplaceAll(sonicTarget.Name, "/", "-")),
				&cnc.SyncOCI{
					Ref:    cfg.SONiCVSRef,
					Target: target.Fallback(RefSonicTargetVersion, sonicTarget),
				})
		}
	}

	install(BundleControlInstall, StageInstall4DasBoot, "das-boot-seeder-wait",
		&cnc.WaitKube{
			Name: "daemonset/das-boot-seeder",
		})

	install(BundleControlInstall, StageInstall4DasBoot, "das-boot-reg-ctrl-wait",
		&cnc.WaitKube{
			Name: "deployment/das-boot-registration-controller",
		})

	return nil
}
