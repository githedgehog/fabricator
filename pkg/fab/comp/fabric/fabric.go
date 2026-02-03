// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fabric

import (
	"context"
	_ "embed"
	"fmt"
	"net/netip"
	"strconv"

	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/boot"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/alloy"
	"go.githedgehog.com/fabricator/pkg/fab/comp/controlproxy"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

const (
	APIChartRef           = "fabric/charts/fabric-api"
	CtrlChartRef          = "fabric/charts/fabric"
	CtrlRef               = "fabric/fabric"
	DHCPChartRef          = "fabric/charts/fabric-dhcpd"
	DHCPRef               = "fabric/fabric-dhcpd"
	BootChartRef          = "fabric/charts/fabric-boot"
	BootRef               = "fabric/fabric-boot"
	AgentRef              = "fabric/agent"
	CtlRef                = "fabric/hhfctl"
	BroadcomSonicRefBase  = "sonic-bcm-private"
	CelesticaSonicRefBase = "sonic-cls-private"
	OnieRefBase           = "onie-updater-private"

	BinDir         = "/opt/bin"
	CtlBinName     = "hhfctl"
	CtlDestBinName = "kubectl-fabric"

	MTU                   = 9100 // TODO actually use in in the fabric, make configurable
	ServerFacingMTUOffset = 64   // TODO use in the fabric, make configurable
	ServerFacingMTU       = MTU - ServerFacingMTUOffset
)

//go:embed ctrl_values.tmpl.yaml
var ctrlValuesTmpl string

//go:embed boot_values.tmpl.yaml
var bootValuesTmpl string

//go:embed dhcp_values.tmpl.yaml
var dhcpValuesTmpl string

func Install(control fabapi.ControlNode) comp.KubeInstall {
	return func(cfg fabapi.Fabricator) ([]kclient.Object, error) {
		controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
		if err != nil {
			return nil, fmt.Errorf("parsing control VIP: %w", err)
		}

		fabricCfg, err := GetFabricConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("getting fabric config: %w", err)
		}
		fabricCfgYaml, err := kyaml.Marshal(fabricCfg)
		if err != nil {
			return nil, fmt.Errorf("marshalling fabric config: %w", err)
		}

		bootCfg, err := GetFabricBootConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("getting fabric boot config: %w", err)
		}
		bootCfgYaml, err := kyaml.Marshal(bootCfg)
		if err != nil {
			return nil, fmt.Errorf("marshalling fabric boot config: %w", err)
		}

		apiHelm, err := comp.NewHelmChart(cfg, "fabric-api", APIChartRef,
			string(cfg.Status.Versions.Fabric.API), "", true, "")
		if err != nil {
			return nil, fmt.Errorf("creating fabric API helm chart: %w", err)
		}

		ctrlRef, err := comp.ImageURL(cfg, CtrlRef)
		if err != nil {
			return nil, fmt.Errorf("getting image URL for %q: %w", CtrlRef, err)
		}
		ctlrValues, err := tmplutil.FromTemplate("ctrl-values", ctrlValuesTmpl, map[string]any{
			"Repo": ctrlRef,
			"Tag":  string(cfg.Status.Versions.Fabric.Controller),
		})
		if err != nil {
			return nil, fmt.Errorf("ctrl values: %w", err)
		}
		ctrlHelm, err := comp.NewHelmChart(cfg, "fabric", CtrlChartRef,
			string(cfg.Status.Versions.Fabric.Controller), "", false, ctlrValues)
		if err != nil {
			return nil, fmt.Errorf("creating fabric helm chart: %w", err)
		}

		dhcpRef, err := comp.ImageURL(cfg, DHCPRef)
		if err != nil {
			return nil, fmt.Errorf("getting image URL for %q: %w", DHCPRef, err)
		}
		dhcpValues, err := tmplutil.FromTemplate("dhcp-values", dhcpValuesTmpl, map[string]any{
			"Repo":            dhcpRef,
			"Tag":             string(cfg.Status.Versions.Fabric.DHCPD),
			"ListenInterface": control.Spec.Management.Interface,
		})
		if err != nil {
			return nil, fmt.Errorf("dhcp values: %w", err)
		}
		dhcpHelm, err := comp.NewHelmChart(cfg, "fabric-dhcpd", DHCPChartRef,
			string(cfg.Status.Versions.Fabric.DHCPD), "", false, dhcpValues)
		if err != nil {
			return nil, fmt.Errorf("creating fabric DHCP helm chart: %w", err)
		}

		bootRef, err := comp.ImageURL(cfg, BootRef)
		if err != nil {
			return nil, fmt.Errorf("getting image URL for %q: %w", BootRef, err)
		}
		bootValues, err := tmplutil.FromTemplate("boot-values", bootValuesTmpl, map[string]any{
			"Repo": bootRef,
			"Tag":  string(cfg.Status.Versions.Fabric.Boot),
			"Host": controlVIP.Addr().String(),
		})
		if err != nil {
			return nil, fmt.Errorf("boot values: %w", err)
		}
		bootHelm, err := comp.NewHelmChart(cfg, "fabric-boot", BootChartRef,
			string(cfg.Status.Versions.Fabric.Boot), "", false, bootValues)
		if err != nil {
			return nil, fmt.Errorf("creating fabric boot helm chart: %w", err)
		}

		return []kclient.Object{
			apiHelm,
			comp.NewConfigMap("fabric-ctrl-config", map[string]string{
				"config.yaml": string(fabricCfgYaml),
			}),
			ctrlHelm,
			dhcpHelm,
			comp.NewConfigMap("fabric-boot-config", map[string]string{
				"config.yaml": string(bootCfgYaml),
			}),
			bootHelm,
		}, nil
	}
}

func InstallManagementDHCPSubnet(cfg fabapi.Fabricator) ([]kclient.Object, error) {
	mgmt, err := cfg.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing management subnet: %w", err)
	}

	controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing control VIP: %w", err)
	}

	return []kclient.Object{
		comp.NewDHCPSubnet(dhcpapi.ManagementSubnet, dhcpapi.DHCPSubnetSpec{
			Subnet:      dhcpapi.ManagementSubnet,
			ONIEOnly:    true,
			CIDRBlock:   mgmt.Masked().String(),
			Gateway:     mgmt.Addr().Next().String(),
			StartIP:     string(cfg.Spec.Config.Fabric.ManagementDHCPStart),
			EndIP:       string(cfg.Spec.Config.Fabric.ManagementDHCPEnd),
			DefaultURL:  "http://" + controlVIP.Addr().String() + ":32000/onie", // TODO const
			DNSServers:  []string{},
			TimeServers: []string{},
		}),
	}, nil
}

func GetFabricConfig(f fabapi.Fabricator) (*fmeta.FabricConfig, error) {
	// TODO align APIs (user creds)
	users := []fmeta.UserCreds{}
	for name, user := range f.Spec.Config.Fabric.DefaultSwitchUsers {
		users = append(users, fmeta.UserCreds{
			Name:     name,
			Role:     user.Role,
			Password: user.PasswordHash,
			SSHKeys:  user.AuthorizedKeys,
		})
	}

	controlVIP, err := f.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing control VIP: %w", err)
	}

	registry := netip.AddrPortFrom(controlVIP.Addr(), comp.RegistryPort).String()

	observability := fmeta.Observability{}
	if f.Spec.Config.Fabric.Observability != nil {
		observability = *f.Spec.Config.Fabric.Observability
	}

	gwComms := map[uint32]string{}
	for prioStr, commStr := range f.Spec.Config.Gateway.Communities {
		prio, err := strconv.ParseUint(prioStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("config: gatewayCommunity priority %s is invalid: %w", prioStr, err)
		}

		gwComms[uint32(prio)] = commStr
	}

	// TODO align APIs (fabric config field names, check agent spec too)
	return &fmeta.FabricConfig{
		ControlVIP:             string(f.Spec.Config.Control.VIP),
		APIServer:              netip.AddrPortFrom(controlVIP.Addr(), k3s.APIPort).String(),
		AgentRepo:              comp.JoinURLParts(registry, comp.RegistryPrefix, AgentRef),
		VPCIRBVLANRanges:       f.Spec.Config.Fabric.VPCIRBVLANs,
		VPCPeeringVLANRanges:   f.Spec.Config.Fabric.VPCWorkaroundVLANs,
		TH5WorkaroundVLANRange: f.Spec.Config.Fabric.TH5WorkaroundVLANs,
		VPCPeeringDisabled:     false, // TODO remove?
		ReservedSubnets: []string{
			// TODO what else?
			string(f.Spec.Config.Control.ManagementSubnet),
			string(f.Spec.Config.Fabric.FabricSubnet),
			string(f.Spec.Config.Fabric.ProtocolSubnet),
			string(f.Spec.Config.Fabric.VTEPSubnet),
			string(f.Spec.Config.Fabric.VPCWorkaroundSubnet),
			string(f.Spec.Config.Fabric.MCLAGSessionSubnet),
			string(f.Spec.Config.Fabric.ProxyExternalSubnet),
		},
		Users:                    users,
		FabricMode:               f.Spec.Config.Fabric.Mode,
		BaseVPCCommunity:         f.Spec.Config.Fabric.BaseVPCCommunity,
		VPCLoopbackSubnet:        string(f.Spec.Config.Fabric.VPCWorkaroundSubnet),
		FabricMTU:                MTU,                   // TODO use
		ServerFacingMTUOffset:    ServerFacingMTUOffset, // TODO use
		ESLAGMACBase:             f.Spec.Config.Fabric.ESLAGMACBase,
		ESLAGESIPrefix:           f.Spec.Config.Fabric.ESLAGESIPrefix,
		AlloyRepo:                comp.JoinURLParts(registry, comp.RegistryPrefix, alloy.BinRef),
		AlloyVersion:             string(alloy.Version(f)),
		AlloyTargets:             f.Spec.Config.Observability.Targets,
		Observability:            observability,
		ControlProxyURL:          fmt.Sprintf("http://%s:%d", controlVIP.Addr().String(), controlproxy.NodePort),
		DefaultMaxPathsEBGP:      64,
		AllowExtraSwitchProfiles: false,
		MCLAGSessionSubnet:       string(f.Spec.Config.Fabric.MCLAGSessionSubnet),
		GatewayASN:               f.Spec.Config.Gateway.ASN,
		GatewayAPISync:           f.Spec.Config.Gateway.Enable,
		LoopbackWorkaround:       false,
		ProtocolSubnet:           string(f.Spec.Config.Fabric.ProtocolSubnet),
		VTEPSubnet:               string(f.Spec.Config.Fabric.VTEPSubnet),
		FabricSubnet:             string(f.Spec.Config.Fabric.FabricSubnet),
		L2ProxyExternalSubnet:    string(f.Spec.Config.Fabric.ProxyExternalSubnet),
		DisableBFD:               f.Spec.Config.Fabric.DisableBFD,
		IncludeSONiCCLSPlus:      f.Spec.Config.Fabric.IncludeCLS,
		SpineASN:                 f.Spec.Config.Fabric.SpineASN,
		LeafASNStart:             f.Spec.Config.Fabric.LeafASNStart,
		LeafASNEnd:               f.Spec.Config.Fabric.LeafASNEnd,
		ManagementSubnet:         string(f.Spec.Config.Control.ManagementSubnet),
		ManagementDHCPStart:      string(f.Spec.Config.Fabric.ManagementDHCPStart),
		ManagementDHCPEnd:        string(f.Spec.Config.Fabric.ManagementDHCPEnd),
		GatewayCommunities:       gwComms,
		GatewayBFD:               true,
	}, nil
}

func GetFabricBootConfig(f fabapi.Fabricator) (*boot.ServerConfig, error) {
	regURL, err := comp.RegistryURL(f)
	if err != nil {
		return nil, fmt.Errorf("getting registry URL: %w", err)
	}

	nosRepos := map[fmeta.NOSType]string{}
	for nosType := range f.Status.Versions.Fabric.NOS {
		if !isIncludeNOS(f, nosType) {
			continue
		}

		nosRepos[nosType] = comp.JoinURLParts(regURL, comp.RegistryPrefix, getNOSRefBase(nosType), string(nosType))
	}

	nosVersions := map[fmeta.NOSType]string{}
	for nosType, version := range f.Status.Versions.Fabric.NOS {
		if !isIncludeNOS(f, nosType) {
			continue
		}

		nosVersions[nosType] = string(version)
	}

	onieRepos := map[string]string{}
	for platform := range f.Status.Versions.Fabric.ONIE {
		onieRepos[platform] = comp.JoinURLParts(regURL, comp.RegistryPrefix, OnieRefBase, platform)
	}

	onieVersions := map[string]string{}
	for platform, version := range f.Status.Versions.Fabric.ONIE {
		onieVersions[platform] = string(version)
	}

	return &boot.ServerConfig{
		ControlVIP:           string(f.Spec.Config.Control.VIP),
		NOSRepos:             nosRepos,
		NOSVersions:          nosVersions,
		ONIERepos:            onieRepos,
		ONIEPlatformVersions: onieVersions,
	}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	arts := comp.OCIArtifacts{
		APIChartRef:  cfg.Status.Versions.Fabric.API,
		CtrlChartRef: cfg.Status.Versions.Fabric.Controller,
		CtrlRef:      cfg.Status.Versions.Fabric.Controller,
		DHCPChartRef: cfg.Status.Versions.Fabric.DHCPD,
		DHCPRef:      cfg.Status.Versions.Fabric.DHCPD,
		BootChartRef: cfg.Status.Versions.Fabric.Boot,
		BootRef:      cfg.Status.Versions.Fabric.Boot,
		AgentRef:     cfg.Status.Versions.Fabric.Agent,
		CtlRef:       cfg.Status.Versions.Fabric.Ctl,
		alloy.BinRef: alloy.Version(cfg),
	}

	for nosType, version := range cfg.Status.Versions.Fabric.NOS {
		if nosType == "" || version == "" {
			return nil, fmt.Errorf("empty NOS type or version") //nolint:goerr113
		}

		if !isIncludeNOS(cfg, nosType) {
			continue
		}

		arts[comp.JoinURLParts(getNOSRefBase(nosType), string(nosType))] = version
	}

	for platform, version := range cfg.Status.Versions.Fabric.ONIE {
		if platform == "" || version == "" {
			return nil, fmt.Errorf("empty ONIE platform or version") //nolint:goerr113
		}

		arts[comp.JoinURLParts(OnieRefBase, platform)] = version
	}

	return arts, nil
}

func getNOSRefBase(nosType fmeta.NOSType) string {
	switch nosType {
	case fmeta.NOSTypeSONiCBCMBase, fmeta.NOSTypeSONiCBCMCampus, fmeta.NOSTypeSONiCBCMVS:
		return BroadcomSonicRefBase
	case fmeta.NOSTypeSONiCCLSPlusBroadcom, fmeta.NOSTypeSONiCCLSPlusMarvell, fmeta.NOSTypeSONiCCLSPlusVS:
		return CelesticaSonicRefBase
	default:
		return "invalid"
	}
}

func isIncludeNOS(cfg fabapi.Fabricator, nosType fmeta.NOSType) bool {
	switch nosType {
	case fmeta.NOSTypeSONiCBCMBase, fmeta.NOSTypeSONiCBCMCampus, fmeta.NOSTypeSONiCBCMVS:
		return true
	case fmeta.NOSTypeSONiCCLSPlusBroadcom, fmeta.NOSTypeSONiCCLSPlusMarvell, fmeta.NOSTypeSONiCCLSPlusVS:
		return cfg.Spec.Config.Fabric.IncludeCLS
	default:
		return false
	}
}

var (
	_ comp.KubeStatus = StatusAPI
	_ comp.KubeStatus = StatusCtrl
	_ comp.KubeStatus = StatusDHCP
	_ comp.KubeStatus = StatusBoot
)

func StatusAPI(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	return comp.MergeKubeStatuses(ctx, kube, cfg, //nolint:wrapcheck
		comp.GetCRDStatus("switches.wiring.githedgehog.com", "v1beta1"),
		comp.GetCRDStatus("connections.wiring.githedgehog.com", "v1beta1"),
	)
}

func StatusCtrl(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, CtrlRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", CtrlRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Fabric.Controller)

	return comp.GetDeploymentStatus("fabric-ctrl", "manager", image)(ctx, kube, cfg)
}

func StatusDHCP(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, DHCPRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", DHCPRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Fabric.DHCPD)

	return comp.GetDeploymentStatus("fabric-dhcpd", "fabric-dhcpd", image)(ctx, kube, cfg)
}

func StatusBoot(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error) {
	ref, err := comp.ImageURL(cfg, BootRef)
	if err != nil {
		return fabapi.CompStatusUnknown, fmt.Errorf("getting image URL for %q: %w", BootRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Fabric.Boot)

	return comp.GetDeploymentStatus("fabric-boot", "fabric-boot", image)(ctx, kube, cfg)
}
