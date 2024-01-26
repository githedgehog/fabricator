package fab

import (
	"bytes"
	_ "embed"
	"fmt"
	"log/slog"
	"slices"

	helm "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/manager/config"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed fabric_values.tmpl.yaml
var fabricValuesTemplate string

//go:embed fabric_dhcp_server_values.tmpl.yaml
var fabricDHCPServerTemplate string

//go:embed fabric_dhcpd_values.tmpl.yaml
var fabricDHCPDTemplate string

type Fabric struct {
	Ref                      cnc.Ref `json:"ref,omitempty"`
	FabricApiChartRef        cnc.Ref `json:"fabricApiChartRef,omitempty"`
	FabricChartRef           cnc.Ref `json:"fabricChartRef,omitempty"`
	FabricImageRef           cnc.Ref `json:"fabricImageRef,omitempty"`
	AgentRef                 cnc.Ref `json:"agentRef,omitempty"`
	ControlAgentRef          cnc.Ref `json:"controlAgentRef,omitempty"`
	CtlRef                   cnc.Ref `json:"ctlRef,omitempty"`
	FabricDHCPServerRef      cnc.Ref `json:"dhcpServerRef,omitempty"`
	FabricDHCPServerChartRef cnc.Ref `json:"dhcpServerChartRef,omitempty"`
	FabricDHCPDRef           cnc.Ref `json:"dhcpdRef,omitempty"`
	FabricDHCPDChartRef      cnc.Ref `json:"dhcpdChartRef,omitempty"`
	BaseVPCCommunity         string  `json:"baseVPCCommunity,omitempty"`
	ServerFacingMTUOffset    uint    `json:"serverFacingMTUOffset,omitempty"`
	DHCPServer               string  `json:"dhcpServer,omitempty"`
}

var _ cnc.Component = (*Fabric)(nil)

func (cfg *Fabric) Name() string {
	return "fabric"
}

func (cfg *Fabric) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *Fabric) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + FLAG_CATEGORY_CONFIG_BASE_SUFFIX,
			Name:        "base-vpc-community",
			Usage:       "base community to stamp on VPC routes",
			Destination: &cfg.BaseVPCCommunity,
			Value:       "50000:0",
		},
		&cli.UintFlag{
			Category:    cfg.Name() + FLAG_CATEGORY_CONFIG_BASE_SUFFIX,
			Name:        "server-facing-mtu-offset",
			Usage:       "offset to apply to server-facing MTU",
			Destination: &cfg.ServerFacingMTUOffset,
			Value:       64,
		},
		&cli.StringFlag{
			Category:    cfg.Name() + FLAG_CATEGORY_CONFIG_BASE_SUFFIX,
			Name:        "dhcpd",
			Usage:       "use 'hedgehog' DHCPD to enables multi ipv4 namespace DHCP with overlapping subnets (one of 'hedgehog', 'isc')",
			Destination: &cfg.DHCPServer,
			Value:       string(config.DHCPModeHedgehog),
		},
	}
}

func (cfg *Fabric) Hydrate(preset cnc.Preset, fabricMode config.FabricMode) error {
	cfg.Ref = cfg.Ref.Fallback(REF_FABRIC_VERSION)
	cfg.FabricApiChartRef = cfg.FabricApiChartRef.Fallback(REF_FABRIC_API_CHART)
	cfg.FabricChartRef = cfg.FabricChartRef.Fallback(REF_FABRIC_CHART)
	cfg.FabricImageRef = cfg.FabricImageRef.Fallback(REF_FABRIC_IMAGE)
	cfg.AgentRef = cfg.AgentRef.Fallback(REF_FABRIC_AGENT)
	cfg.ControlAgentRef = cfg.ControlAgentRef.Fallback(REF_FABRIC_CONTROL_AGENT)
	cfg.CtlRef = cfg.CtlRef.Fallback(REF_FABRIC_CTL)
	cfg.FabricDHCPServerRef = cfg.FabricDHCPServerRef.Fallback(REF_FABRIC_DHCP_SERVER)
	cfg.FabricDHCPServerChartRef = cfg.FabricDHCPServerChartRef.Fallback(REF_FABRIC_DHCP_SERVER_CHART)
	cfg.FabricDHCPDRef = cfg.FabricDHCPDRef.Fallback(REF_FABRIC_DHCPD)
	cfg.FabricDHCPDChartRef = cfg.FabricDHCPDChartRef.Fallback(REF_FABRIC_DHCPD_CHART)

	if !slices.Contains(config.DHCPModes, config.DHCPMode(cfg.DHCPServer)) {
		return errors.Errorf("invalid dhcp server mode %q", cfg.DHCPServer)
	}

	return nil
}

func (cfg *Fabric) Build(basedir string, preset cnc.Preset, fabricMode config.FabricMode, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.FabricApiChartRef = cfg.FabricApiChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricChartRef = cfg.FabricChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricImageRef = cfg.FabricImageRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.AgentRef = cfg.AgentRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.ControlAgentRef = cfg.ControlAgentRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.CtlRef = cfg.CtlRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPServerRef = cfg.FabricDHCPServerRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPServerChartRef = cfg.FabricDHCPServerChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPDRef = cfg.FabricDHCPDRef.Fallback(cfg.Ref, BaseConfig(get).Source)
	cfg.FabricDHCPDChartRef = cfg.FabricDHCPDChartRef.Fallback(cfg.Ref, BaseConfig(get).Source)

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

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-api-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricApiChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-image",
		&cnc.SyncOCI{
			Ref:    cfg.FabricImageRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-agent-seeder",
		&cnc.SyncOCI{
			Ref:    cfg.AgentRef,
			Target: target.Fallback(cnc.Ref{Name: "fabric/agent/x86_64", Tag: "latest"}),
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-agent",
		&cnc.SyncOCI{
			Ref:    cfg.AgentRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-dhcp-server-image",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPServerRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-dhcp-server-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPServerChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-dhcpd-image",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPDRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-dhcpd-chart",
		&cnc.SyncOCI{
			Ref:    cfg.FabricDHCPDChartRef,
			Target: target,
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-control-agent",
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

	install(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-control-agent-install",
		&cnc.ExecCommand{
			Name: "/opt/hedgehog/bin/agent",
			Args: []string{"install", "--control", "--agent-path", "/opt/hedgehog/bin/agent", "--agent-user", "root"},
		})

	users := []agentapi.UserCreds{}
	slog.Info("Base config", "dev", BaseConfig(get).Dev)
	if BaseConfig(get).Dev {
		users = append(users, DEV_SONIC_USERS...)
		slog.Info("Adding dev users", "users", users)
		for idx := range users {
			users[idx].SSHKeys = append(users[idx].SSHKeys, BaseConfig(get).AuthorizedKeys...)
			slog.Info("Adding dev ssh keys to user", "user", users[idx])
		}
	}

	var dhcp cnc.KubeObjectProvider
	if cfg.DHCPServer == "isc" {
		dhcp = cnc.KubeHelmChart("fabric-dhcp-server", "default", helm.HelmChartSpec{
			TargetNamespace: "default",
			Chart:           "oci://" + targetInCluster.Fallback(cfg.FabricDHCPServerChartRef).RepoName(),
			Version:         cfg.FabricDHCPServerChartRef.Tag,
			RepoCA:          ZotConfig(get).TLS.CA.Cert,
		}, cnc.FromTemplate(fabricDHCPServerTemplate,
			"ref", target.Fallback(cfg.FabricDHCPServerRef),
		))
	} else if cfg.DHCPServer == "hedgehog" {
		dhcp = cnc.KubeHelmChart("fabric-dhcpd", "default", helm.HelmChartSpec{
			TargetNamespace: "default",
			Chart:           "oci://" + targetInCluster.Fallback(cfg.FabricDHCPDChartRef).RepoName(),
			Version:         cfg.FabricDHCPDChartRef.Tag,
			RepoCA:          ZotConfig(get).TLS.CA.Cert,
		}, cnc.FromTemplate(fabricDHCPDTemplate,
			"ref", target.Fallback(cfg.FabricDHCPDRef),
		))
	}

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-install",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "fabric-install.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-fabric-install.yaml",
			},
			Content: cnc.FromKubeObjects(
				cnc.KubeHelmChart("fabric-api", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + targetInCluster.Fallback(cfg.FabricApiChartRef).RepoName(),
					Version:         cfg.FabricApiChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
					FailurePolicy:   "abort", // very important not to re-install crd charts
				}, cnc.FromValue("")),
				cnc.KubeHelmChart("fabric", "default", helm.HelmChartSpec{
					TargetNamespace: "default",
					Chart:           "oci://" + targetInCluster.Fallback(cfg.FabricChartRef).RepoName(),
					Version:         cfg.FabricChartRef.Tag,
					RepoCA:          ZotConfig(get).TLS.CA.Cert,
				}, cnc.FromTemplate(fabricValuesTemplate,
					"ref", target.Fallback(cfg.FabricImageRef),
					"proxyRef", target.Fallback(MiscConfig(get).RBACProxyImageRef),
				)),
				cnc.KubeConfigMap("fabric-config", "default", "config.yaml", cnc.YAMLFrom(
					&config.Fabric{
						ControlVIP:  CONTROL_VIP + CONTROL_VIP_MASK,
						APIServer:   fmt.Sprintf("%s:%d", CONTROL_VIP, K3S_API_PORT),
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
						DHCPMode:              config.DHCPMode(cfg.DHCPServer),
						DHCPDConfigMap:        "fabric-dhcp-server-config",
						DHCPDConfigKey:        "dhcpd.conf",
						FabricMode:            fabricMode,
						BaseVPCCommunity:      cfg.BaseVPCCommunity,
						VPCLoopbackSubnet:     "172.30.240.0/20", // TODO make configurable
						FabricMTU:             9100,              // TODO make configurable
						ServerFacingMTUOffset: uint16(cfg.ServerFacingMTUOffset),
						ESLAGMACBase:          "f2:00:00:00:00:00", // TODO make configurable
						ESLAGESIPrefix:        "00:f2:00:00:",      // TODO make configurable
					},
				)),
				dhcp,
			),
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "kubectl-fabric-install",
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

	install(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-wait",
		&cnc.WaitKube{
			Name: "deployment/fabric-controller-manager",
		})

	run(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "fabric-wiring",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "wiring.yaml",
				InstallTarget: "/var/lib/rancher/k3s/server/manifests",
				InstallName:   "hh-wiring.yaml",
			},
			Content: cnc.FromValue(wiringData.String()),
		})

	install(BundleControlInstall, STAGE_INSTALL_3_FABRIC, "control-agent-wait",
		&cnc.WaitKube{
			Name: "controlagent/" + controlNodeName,
		})

	return nil
}
