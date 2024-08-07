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
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"slices"

	helm "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/agent/alloy"
	"go.githedgehog.com/fabric/pkg/agent/dozer/bcm"
	wiringlib "go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed fabric_values.tmpl.yaml
var fabricValuesTemplate string

//go:embed fabric_dhcp_server_values.tmpl.yaml
var fabricDHCPServerTemplate string

//go:embed fabric_dhcpd_values.tmpl.yaml
var fabricDHCPDTemplate string

//go:embed fabric_proxy_values.tmpl.yaml
var fabricProxyTemplate string

type Fabric struct {
	Ref                      cnc.Ref          `json:"ref,omitempty"`
	FabricAPIChartRef        cnc.Ref          `json:"fabricApiChartRef,omitempty"`
	FabricChartRef           cnc.Ref          `json:"fabricChartRef,omitempty"`
	FabricImageRef           cnc.Ref          `json:"fabricImageRef,omitempty"`
	AgentRef                 cnc.Ref          `json:"agentRef,omitempty"`
	ControlAgentRef          cnc.Ref          `json:"controlAgentRef,omitempty"`
	CtlRef                   cnc.Ref          `json:"ctlRef,omitempty"`
	FabricDHCPServerRef      cnc.Ref          `json:"dhcpServerRef,omitempty"`
	FabricDHCPServerChartRef cnc.Ref          `json:"dhcpServerChartRef,omitempty"`
	FabricDHCPDRef           cnc.Ref          `json:"dhcpdRef,omitempty"`
	FabricDHCPDChartRef      cnc.Ref          `json:"dhcpdChartRef,omitempty"`
	BaseVPCCommunity         string           `json:"baseVPCCommunity,omitempty"`
	ServerFacingMTUOffset    uint             `json:"serverFacingMTUOffset,omitempty"`
	DHCPServer               string           `json:"dhcpServer,omitempty"`
	AlloyRef                 cnc.Ref          `json:"alloyRef,omitempty"`
	Alloy                    meta.AlloyConfig `json:"alloy,omitempty"`
	ControlProxyRef          cnc.Ref          `json:"controlProxyRef,omitempty"`
	ControlProxyChartRef     cnc.Ref          `json:"controlProxyChartRef,omitempty"`
	ControlProxy             bool             `json:"controlProxy,omitempty"`
	SwitchUsers              []meta.UserCreds `json:"switchUsers,omitempty"`
}

var _ cnc.Component = (*Fabric)(nil)

func (cfg *Fabric) Name() string {
	return "fabric"
}

func (cfg *Fabric) IsEnabled(_ cnc.Preset) bool {
	return true
}

func (cfg *Fabric) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "base-vpc-community",
			Usage:       "base community to stamp on VPC routes",
			Destination: &cfg.BaseVPCCommunity,
			Value:       "50000:0",
		},
		&cli.UintFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "server-facing-mtu-offset",
			Usage:       "offset to apply to server-facing MTU",
			Destination: &cfg.ServerFacingMTUOffset,
			Value:       64,
		},
		&cli.StringFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "dhcpd",
			Usage:       "use 'hedgehog' DHCPD to enables multi ipv4 namespace DHCP with overlapping subnets (one of 'hedgehog', 'isc')",
			Destination: &cfg.DHCPServer,
			Value:       string(meta.DHCPModeHedgehog),
		},
		&cli.BoolFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "control-proxy",
			Usage:       "enable control proxy to allow services running on the switches (e.g. Grafana Alloy) to go to outside through control node",
			Destination: &cfg.ControlProxy,
			Value:       false,
		},
	}
}

func (cfg *Fabric) Hydrate(_ cnc.Preset, _ meta.FabricMode) error {
	cfg.Ref = cfg.Ref.Fallback(RefFabricVersion)
	cfg.FabricAPIChartRef = cfg.FabricAPIChartRef.Fallback(RefFabricAPIChart)
	cfg.FabricChartRef = cfg.FabricChartRef.Fallback(RefFabricChart)
	cfg.FabricImageRef = cfg.FabricImageRef.Fallback(RefFabricImage)
	cfg.AgentRef = cfg.AgentRef.Fallback(RefFabricAgent)
	cfg.ControlAgentRef = cfg.ControlAgentRef.Fallback(RefFabricControlAgent)
	cfg.CtlRef = cfg.CtlRef.Fallback(RefFabricCtl)
	cfg.FabricDHCPServerRef = cfg.FabricDHCPServerRef.Fallback(RefFabricDHCPServer)
	cfg.FabricDHCPServerChartRef = cfg.FabricDHCPServerChartRef.Fallback(RefFabricDHCPServerChart)
	cfg.FabricDHCPDRef = cfg.FabricDHCPDRef.Fallback(RefFabricDHCPD)
	cfg.FabricDHCPDChartRef = cfg.FabricDHCPDChartRef.Fallback(RefFabricDHCPDChart)
	cfg.AlloyRef = cfg.AlloyRef.Fallback(RefAlloy)
	cfg.ControlProxyRef = cfg.ControlProxyRef.Fallback(RefControlProxy)
	cfg.ControlProxyChartRef = cfg.ControlProxyChartRef.Fallback(RefControlProxyChart)

	if !slices.Contains(meta.DHCPModes, meta.DHCPMode(cfg.DHCPServer)) {
		return errors.Errorf("invalid dhcp server mode %q", cfg.DHCPServer)
	}

	cfg.Alloy.Default()

	return nil
}

func (cfg *Fabric) buildFabricConfig(fabricMode meta.FabricMode, get cnc.GetComponent, users []meta.UserCreds) *meta.FabricConfig {
	target := BaseConfig(get).Target

	cfg.Alloy.ControlProxyURL = fmt.Sprintf("http://%s:%d", ControlVIP, ControlProxyNodePort)

	return &meta.FabricConfig{
		ControlVIP:  ControlVIP + ControlVIPMask,
		APIServer:   fmt.Sprintf("%s:%d", ControlVIP, K3sAPIPort),
		AgentRepo:   target.Fallback(cfg.AgentRef).RepoName(),
		AgentRepoCA: ZotConfig(get).TLS.CA.Cert,
		VPCIRBVLANRanges: []meta.VLANRange{
			{From: 3000, To: 3999}, // TODO make configurable
		},
		VPCPeeringVLANRanges: []meta.VLANRange{
			{From: 100, To: 999}, // TODO only 500 needed? make configurable
		},
		VPCPeeringDisabled: false,
		ReservedSubnets: []string{ // TODO make configurable
			K3sConfig(get).ClusterCIDR,
			K3sConfig(get).ServiceCIDR,
			"172.30.0.0/16", // Fabric subnet // TODO make configurable
			"172.31.0.0/16", // VLAB subnet // TODO make configurable
		},
		Users:                 users,
		DHCPMode:              meta.DHCPMode(cfg.DHCPServer),
		DHCPDConfigMap:        "fabric-dhcp-server-config",
		DHCPDConfigKey:        "dhcpd.conf",
		FabricMode:            fabricMode,
		BaseVPCCommunity:      cfg.BaseVPCCommunity,
		VPCLoopbackSubnet:     "172.30.240.0/20", // TODO make configurable
		FabricMTU:             9100,              // TODO make configurable
		ServerFacingMTUOffset: uint16(cfg.ServerFacingMTUOffset),
		ESLAGMACBase:          "f2:00:00:00:00:00", // TODO make configurable
		ESLAGESIPrefix:        "00:f2:00:00:",      // TODO make configurable
		Alloy:                 cfg.Alloy,
		AlloyRepo:             target.Fallback(cfg.AlloyRef).RepoName(),
		AlloyVersion:          target.Fallback(cfg.AlloyRef).Tag,
		DefaultMaxPathsEBGP:   64,
	}
}

func (cfg *Fabric) Validate(_ string, _ cnc.Preset, fabricMode meta.FabricMode, get cnc.GetComponent, wiring *wiringlib.Data) error {
	fabricCfg := cfg.buildFabricConfig(fabricMode, get, []meta.UserCreds{})

	if err := wiringlib.ValidateFabric(context.TODO(), wiring.Native, fabricCfg); err != nil {
		return errors.Wrapf(err, "error validating wiring")
	}

	if err := cfg.Alloy.Validate(); err != nil {
		return errors.Wrap(err, "error validating alloy config")
	}

	for _, user := range cfg.SwitchUsers {
		if slices.Contains([]string{
			"root",
			"daemon",
			"bin",
			"sys",
			"adm",
			"tty",
			"disk",
			"lp",
			"mail",
			"news",
			"uucp",
			"man",
			"proxy",
			"kmem",
			"dialout",
			"fax",
			"voice",
			"cdrom",
			"floppy",
			"tape",
			"sudo",
			"audio",
			"dip",
			"www-data",
			"backup",
			"operator",
			"list",
			"irc",
			"src",
			"gnats",
			"shadow",
			"utmp",
			"video",
			"sasl",
			"plugdev",
			"staff",
			"games",
			"users",
			"nogroup",
			"systemd-journal",
			"systemd-timesync",
			"systemd-network",
			"systemd-resolve",
			"docker",
			"redis",
			"netadmin",
			"secadmin",
			"messagebus",
			"input",
			"kvm",
			"render",
			"crontab",
			"i2c",
			"ssh",
			"systemd-coredump",
			"ntp",
			"frr",
			bcm.AgentUser,
			alloy.UserName,
		}, user.Name) {
			return errors.Errorf("switch user can't be named %q", user.Name)
		}
	}

	return nil
}

func (cfg *Fabric) Build(_ string, _ cnc.Preset, fabricMode meta.FabricMode, get cnc.GetComponent, wiring *wiringlib.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.FabricAPIChartRef = cfg.FabricAPIChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricChartRef = cfg.FabricChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricImageRef = cfg.FabricImageRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.AgentRef = cfg.AgentRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.ControlAgentRef = cfg.ControlAgentRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.CtlRef = cfg.CtlRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPServerRef = cfg.FabricDHCPServerRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPServerChartRef = cfg.FabricDHCPServerChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPDRef = cfg.FabricDHCPDRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPDChartRef = cfg.FabricDHCPDChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.AlloyRef = cfg.AlloyRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.ControlProxyRef = cfg.ControlProxyRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.ControlProxyChartRef = cfg.ControlProxyChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)

	target := BaseConfig(get).Target
	targetInCluster := BaseConfig(get).TargetInCluster

	controlNodeName, err := getControlNodeName(wiring)
	if err != nil {
		return errors.Wrap(err, "error getting control node name")
	}

	wiringData := &bytes.Buffer{}
	err = wiring.Write(wiringData) // TODO extract to lib
	if err != nil {
		return errors.Wrap(err, "error writing wiring data")
	}

	run(BundleControlInstall, StageInstall3Fabric, "fabric-api-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricAPIChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-image",
		&cnc.SyncOCI{
			Ref:    cfg.FabricImageRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-agent",
		&cnc.SyncOCI{
			Ref:    cfg.AgentRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-dhcp-server-image",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPServerRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-dhcp-server-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPServerChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-dhcpd-image",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPDRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-dhcpd-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPDChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-alloy",
		&cnc.SyncOCI{
			Ref:    cfg.AlloyRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "control-proxy-image",
		&cnc.SyncOCI{
			Ref:    cfg.ControlProxyRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "control-proxy-chart",
		&cnc.SyncOCI{
			Ref:    cfg.ControlProxyChartRef,
			Target: target,
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-control-agent",
		&cnc.FilesORAS{
			Ref: cfg.ControlAgentRef,
			Files: []cnc.File{
				{
					Name:          "agent",
					InstallTarget: "/opt/hedgehog/bin",
					InstallMode:   0o755,
				},
			},
		})

	install(BundleControlInstall, StageInstall3Fabric, "fabric-control-agent-install",
		&cnc.ExecCommand{
			Name: "/opt/hedgehog/bin/agent",
			Args: []string{"install", "--control", "--agent-path", "/opt/hedgehog/bin/agent", "--agent-user", "root"},
		})

	var dhcp cnc.KubeObjectProvider
	if cfg.DHCPServer == "isc" {
		dhcp = cnc.KubeHelmChart("fabric-dhcp-server", "default", helm.HelmChartSpec{
			TargetNamespace: "default",
			Chart:           OCIScheme + targetInCluster.Fallback(cfg.FabricDHCPServerChartRef).RepoName(),
			Version:         cfg.FabricDHCPServerChartRef.Tag,
			RepoCA:          ZotConfig(get).TLS.CA.Cert,
		}, cnc.FromTemplate(fabricDHCPServerTemplate,
			"ref", target.Fallback(cfg.FabricDHCPServerRef),
		))
	} else if cfg.DHCPServer == "hedgehog" {
		dhcp = cnc.KubeHelmChart("fabric-dhcpd", "default", helm.HelmChartSpec{
			TargetNamespace: "default",
			Chart:           OCIScheme + targetInCluster.Fallback(cfg.FabricDHCPDChartRef).RepoName(),
			Version:         cfg.FabricDHCPDChartRef.Tag,
			RepoCA:          ZotConfig(get).TLS.CA.Cert,
		}, cnc.FromTemplate(fabricDHCPDTemplate,
			"ref", target.Fallback(cfg.FabricDHCPDRef),
		))
	}

	users := append([]meta.UserCreds{}, cfg.SwitchUsers...)
	slog.Info("Base config", "dev", BaseConfig(get).Dev)
	if BaseConfig(get).Dev {
		for _, devUser := range DevSonicUsers {
			ok := true
			for _, user := range users {
				if user.Name == devUser.Name {
					slog.Warn("Skipping dev user as it's already used", "user", user.Name)
					ok = false

					break
				}
			}

			if !ok {
				continue
			}

			slog.Debug("Adding dev user", "user", devUser.Name)
			users = append(users, devUser)
		}

		for idx := range users {
			users[idx].SSHKeys = append(users[idx].SSHKeys, BaseConfig(get).AuthorizedKeys...)
			slog.Debug("Adding dev ssh keys to user", "user", users[idx])
		}
	}

	admin := false
	for _, user := range users {
		if user.Name == "admin" {
			admin = true

			break
		}
	}
	if !admin {
		return errors.New("switch admin user is required")
	}

	fabricCfg := cfg.buildFabricConfig(fabricMode, get, users)

	run(BundleControlInstall, StageInstall3Fabric, "fabric-install",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "fabric-install.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-fabric-install.yaml",
			},
			Content: cnc.FromKubeObjects(
				cnc.KubeHelmChart("fabric-api", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.FabricAPIChartRef).RepoName(),
					Version:         cfg.FabricAPIChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
					FailurePolicy:   "abort", // very important not to re-install crd charts
				}, cnc.FromValue("")),
				cnc.KubeHelmChart("fabric", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.FabricChartRef).RepoName(),
					Version:         cfg.FabricChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(fabricValuesTemplate,
					"ref", target.Fallback(cfg.FabricImageRef),
					"proxyRef", target.Fallback(MiscConfig(get).RBACProxyImageRef),
				)),
				cnc.KubeConfigMap("fabric-config", "default", "config.yaml", cnc.YAMLFrom(
					fabricCfg,
				)),
				dhcp,
				cnc.If(cfg.ControlProxy, cnc.KubeHelmChart("fabric-proxy", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           OCIScheme + targetInCluster.Fallback(cfg.ControlProxyChartRef).RepoName(),
					Version:         cfg.ControlProxyChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(fabricProxyTemplate,
					"ref", target.Fallback(cfg.ControlProxyRef),
					"nodePort", fmt.Sprintf("%d", ControlProxyNodePort),
				))),
			),
		})

	run(BundleControlInstall, StageInstall3Fabric, "kubectl-fabric-install",
		&cnc.FilesORAS{
			Ref: cfg.CtlRef,
			Files: []cnc.File{
				{
					Name:          "hhfctl",
					InstallTarget: "/opt/bin",
					InstallMode:   0o755,
					InstallName:   "kubectl-fabric",
				},
			},
		})

	install(BundleControlInstall, StageInstall3Fabric, "fabric-wait",
		&cnc.WaitKube{
			Name: "deployment/fabric-controller-manager",
		})

	run(BundleControlInstall, StageInstall3Fabric, "fabric-wiring",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "wiring.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-wiring.yaml",
			},
			Content: cnc.FromValue(wiringData.String()),
		})

	install(BundleControlInstall, StageInstall3Fabric, "control-agent-wait",
		&cnc.WaitKube{
			Name: "controlagent/" + controlNodeName,
		})

	return nil
}
