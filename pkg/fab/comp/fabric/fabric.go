package fabric

import (
	_ "embed"
	"fmt"
	"net/netip"

	"go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	APIChartRef   = "fabric/charts/fabric-api"
	CtrlChartRef  = "fabric/charts/fabric"
	CtrlRef       = "fabric/fabric"
	DHCPChartRef  = "fabric/charts/fabric-dhcpd"
	DHCPRef       = "fabric/fabric-dhcpd"
	BootChartRef  = "fabric/charts/fabric-boot"
	BootRef       = "fabric/fabric-boot"
	AgentRef      = "fabric/agent"
	CtlRef        = "fabric/hhfctl"
	AlloyRef      = "fabric/alloy"
	ProxyChartRef = "fabric/charts/fabric-proxy"
	ProxyRef      = "fabric/fabric-proxy"
	SonicRef      = "sonic-bcom-private"

	ProxyNodePort = 31028
)

//go:embed ctrl_values.tmpl.yaml
var ctrlValuesTmpl string

//go:embed dhcp_values.tmpl.yaml
var dhcpValuesTmpl string

//go:embed proxy_values.tmpl.yaml
var proxyValuesTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	fabricCfg, err := GetFabricConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("getting fabric config: %w", err)
	}

	fabricCfgYaml, err := yaml.Marshal(fabricCfg)
	if err != nil {
		return nil, fmt.Errorf("marshalling fabric config: %w", err)
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

	controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing control VIP: %w", err)
	}
	dhcpValues, err := tmplutil.FromTemplate("dhcp-values", dhcpValuesTmpl, map[string]any{
		"Repo":          dhcpRef,
		"Tag":           string(cfg.Status.Versions.Fabric.DHCPD),
		"ListenAddress": controlVIP.Addr().String(),
	})
	if err != nil {
		return nil, fmt.Errorf("dhcp values: %w", err)
	}
	dhcpHelm, err := comp.NewHelmChart(cfg, "fabric-dhcpd", DHCPChartRef,
		string(cfg.Status.Versions.Fabric.DHCPD), "", false, dhcpValues)
	if err != nil {
		return nil, fmt.Errorf("creating fabric DHCP helm chart: %w", err)
	}

	proxyRef, err := comp.ImageURL(cfg, ProxyRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ProxyRef, err)
	}
	proxyValues, err := tmplutil.FromTemplate("proxy-values", proxyValuesTmpl, map[string]any{
		"Repo": proxyRef,
		"Tag":  string(cfg.Status.Versions.Fabric.Proxy),
		"Port": ProxyNodePort,
	})
	if err != nil {
		return nil, fmt.Errorf("proxy values: %w", err)
	}
	proxyHelm, err := comp.NewHelmChart(cfg, "fabric-proxy", ProxyChartRef,
		string(cfg.Status.Versions.Fabric.ProxyChart), "", false, proxyValues)
	if err != nil {
		return nil, fmt.Errorf("creating fabric proxy helm chart: %w", err)
	}

	// TODO boot

	return []client.Object{
		apiHelm,
		comp.NewConfigMap("fabric-config", map[string]string{
			"config.yaml": string(fabricCfgYaml),
		}),
		ctrlHelm,
		dhcpHelm,
		proxyHelm,
	}, nil
}

func GetFabricConfig(f fabapi.Fabricator) (*meta.FabricConfig, error) {
	// TODO align APIs (user creds)
	users := []meta.UserCreds{}
	for name, user := range f.Spec.Config.Fabric.DefaultSwitchUsers {
		users = append(users, meta.UserCreds{
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
	f.Spec.Config.Fabric.DefaultAlloyConfig.ControlProxyURL = fmt.Sprintf("http://%s:%d", controlVIP.Addr().String(), ProxyNodePort)

	// TODO align APIs (fabric config field names, check agent spec too)
	return &meta.FabricConfig{
		ControlVIP:           string(f.Spec.Config.Control.VIP),
		APIServer:            netip.AddrPortFrom(controlVIP.Addr(), k3s.APIPort).String(),
		AgentRepo:            comp.JoinURLParts(registry, AgentRef),
		VPCIRBVLANRanges:     f.Spec.Config.Fabric.VPCIRBVLANs,
		VPCPeeringVLANRanges: f.Spec.Config.Fabric.VPCWorkaroundVLANs,
		VPCPeeringDisabled:   false, // TODO remove?
		ReservedSubnets: []string{
			// TODO what else?
			string(f.Spec.Config.Control.ManagementSubnet),
			string(f.Spec.Config.Fabric.FabricSubnet),
			string(f.Spec.Config.Fabric.ProtocolSubnet),
			string(f.Spec.Config.Fabric.VTEPSubnet),
			string(f.Spec.Config.Fabric.VPCWorkaroundSubnet),
		},
		Users:                    users,
		FabricMode:               f.Spec.Config.Fabric.Mode,
		BaseVPCCommunity:         f.Spec.Config.Fabric.BaseVPCCommunity,
		VPCLoopbackSubnet:        string(f.Spec.Config.Fabric.VPCWorkaroundSubnet),
		FabricMTU:                9100, // TODO use
		ServerFacingMTUOffset:    64,   // TODO use
		ESLAGMACBase:             f.Spec.Config.Fabric.ESLAGMACBase,
		ESLAGESIPrefix:           f.Spec.Config.Fabric.ESLAGESIPrefix,
		AlloyRepo:                comp.JoinURLParts(registry, AlloyRef),
		AlloyVersion:             string(f.Status.Versions.Fabric.Alloy),
		Alloy:                    f.Spec.Config.Fabric.DefaultAlloyConfig,
		DefaultMaxPathsEBGP:      64,
		AllowExtraSwitchProfiles: false,
	}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		APIChartRef:  cfg.Status.Versions.Fabric.API,
		CtrlChartRef: cfg.Status.Versions.Fabric.Controller,
		CtrlRef:      cfg.Status.Versions.Fabric.Controller,
		DHCPChartRef: cfg.Status.Versions.Fabric.DHCPD,
		DHCPRef:      cfg.Status.Versions.Fabric.DHCPD,
		// BootChartRef:  cfg.Status.Versions.Fabric.Boot,
		// BootRef:       cfg.Status.Versions.Fabric.Boot,
		AgentRef:      cfg.Status.Versions.Fabric.Agent,
		CtlRef:        cfg.Status.Versions.Fabric.Ctl,
		AlloyRef:      cfg.Status.Versions.Fabric.Alloy,
		ProxyChartRef: cfg.Status.Versions.Fabric.ProxyChart,
		ProxyRef:      cfg.Status.Versions.Fabric.Proxy,
		SonicRef:      cfg.Status.Versions.Fabric.NOS["sonic-bcm-vs"], // TODO we need multiple versions for the same name?
	}, nil
}
