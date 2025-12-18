// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"dario.cat/mergo"
	"github.com/go-playground/validator/v10"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/agent/dozer/bcm"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/libmeta/pkg/alloy"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

const (
	FabName      = "default"
	FabNamespace = "fab"

	ConditionApplied      = "Applied"
	ConditionReady        = "Ready"        // Control node(s) services
	ConditionGatewayReady = "GatewayReady" // Gateway services
)

type FabricatorSpec struct {
	Config    FabConfig    `json:"config,omitempty"`
	Overrides FabOverrides `json:"overrides,omitempty"`
}

type FabricatorStatus struct {
	IsBootstrap bool     `json:"isBootstrap,omitempty"`
	IsInstall   bool     `json:"isInstall,omitempty"`
	Versions    Versions `json:"versions,omitempty"`

	// Time of the last attempt to apply configuration
	LastAttemptTime kmetav1.Time `json:"lastAttemptTime,omitempty"`
	// Generation of the last attempt to apply configuration
	LastAttemptGen int64 `json:"lastAttemptGen,omitempty"`
	// Time of the last successful configuration application
	LastAppliedTime kmetav1.Time `json:"lastAppliedTime,omitempty"`
	// Generation of the last successful configuration application
	LastAppliedGen int64 `json:"lastAppliedGen,omitempty"`
	// Controller version that applied the last successful configuration
	LastAppliedController string `json:"lastAppliedController,omitempty"`
	// Time of the last status check
	LastStatusCheck kmetav1.Time `json:"lastStatusCheck,omitempty"`

	// Conditions of the fabricator, includes readiness marker for use with kubectl wait
	Conditions []kmetav1.Condition `json:"conditions"`

	Components ComponentsStatus `json:"components,omitempty"`

	// TODO reserved VLANs, subnets, etc.
}

type ComponentStatus string

const (
	CompStatusUnknown  ComponentStatus = ""
	CompStatusNotFound ComponentStatus = "NotFound"
	CompStatusPending  ComponentStatus = "Pending"
	CompStatusReady    ComponentStatus = "Ready"
	CompStatusSkipped  ComponentStatus = "Skipped"
)

var ComponentStatuses = []ComponentStatus{
	CompStatusUnknown,
	CompStatusNotFound,
	CompStatusPending,
	CompStatusReady,
	CompStatusSkipped,
}

// ! WARNING: Make sure to update the IsReady/IsGatewayReady methods if you add or remove components
type ComponentsStatus struct {
	FabricatorAPI        ComponentStatus            `json:"fabricatorAPI,omitempty"`
	FabricatorCtrl       ComponentStatus            `json:"fabricatorCtrl,omitempty"`
	FabricatorNodeConfig ComponentStatus            `json:"fabricatorNodeConfig,omitempty"`
	CertManagerCtrl      ComponentStatus            `json:"certManagerCtrl,omitempty"`
	CertManagerWebhook   ComponentStatus            `json:"certManagerWebhook,omitempty"`
	Reloader             ComponentStatus            `json:"reloader,omitempty"`
	Zot                  ComponentStatus            `json:"zot,omitempty"`
	NTP                  ComponentStatus            `json:"ntp,omitempty"`
	FabricAPI            ComponentStatus            `json:"fabricAPI,omitempty"`
	FabricCtrl           ComponentStatus            `json:"fabricCtrl,omitempty"`
	FabricBoot           ComponentStatus            `json:"fabricBoot,omitempty"`
	FabricDHCP           ComponentStatus            `json:"fabricDHCP,omitempty"`
	ControlProxy         ComponentStatus            `json:"controlProxy,omitempty"`
	ControlAlloy         ComponentStatus            `json:"controlAlloy,omitempty"`
	GatewayAPI           ComponentStatus            `json:"gatewayAPI,omitempty"`
	GatewayCtrl          ComponentStatus            `json:"gatewayCtrl,omitempty"`
	GatewayAlloy         ComponentStatus            `json:"gatewayAlloy,omitempty"`
	GatewayDataplane     map[string]ComponentStatus `json:"gatewayDataplane,omitempty"`
	GatewayFRR           map[string]ComponentStatus `json:"gatewayFRR,omitempty"`
}

// TODO simplify or generate it instead
func (c *ComponentsStatus) IsReady(cfg Fabricator, nodes []FabNode) bool {
	res := c.FabricatorAPI == CompStatusReady &&
		c.FabricatorCtrl == CompStatusReady &&
		c.FabricatorNodeConfig != CompStatusUnknown &&
		c.CertManagerCtrl == CompStatusReady &&
		c.CertManagerWebhook == CompStatusReady &&
		c.Reloader == CompStatusReady &&
		c.Zot == CompStatusReady &&
		c.NTP == CompStatusReady &&
		c.FabricAPI == CompStatusReady &&
		c.FabricCtrl == CompStatusReady &&
		c.FabricBoot == CompStatusReady &&
		c.FabricDHCP == CompStatusReady &&
		c.ControlProxy == CompStatusReady &&
		c.ControlAlloy == CompStatusReady

	if cfg.Spec.Config.Gateway.Enable {
		res = res &&
			c.GatewayAPI == CompStatusReady &&
			c.GatewayCtrl == CompStatusReady &&
			c.GatewayAlloy != CompStatusUnknown

		gws := map[string]bool{}
		for _, node := range nodes {
			if !slices.Contains(node.Spec.Roles, NodeRoleGateway) {
				continue
			}

			gws[node.Name] = true
		}

		for name, dp := range c.GatewayDataplane {
			res = res &&
				gws[name] &&
				dp != CompStatusUnknown
		}

		for name, frr := range c.GatewayFRR {
			res = res &&
				gws[name] &&
				frr != CompStatusUnknown
		}
	} else {
		res = res &&
			c.GatewayAPI == CompStatusSkipped &&
			c.GatewayCtrl == CompStatusSkipped &&
			c.GatewayAlloy == CompStatusSkipped &&
			len(c.GatewayDataplane) == 0 &&
			len(c.GatewayFRR) == 0
	}

	return res
}

func (c *ComponentsStatus) IsGatewayReady(cfg Fabricator, nodes []FabNode) bool {
	if !cfg.Spec.Config.Gateway.Enable {
		return true
	}

	res := c.GatewayAPI == CompStatusReady &&
		c.GatewayCtrl == CompStatusReady &&
		c.GatewayAlloy == CompStatusReady &&
		c.FabricatorNodeConfig != CompStatusUnknown

	gws := map[string]bool{}
	for _, node := range nodes {
		if !slices.Contains(node.Spec.Roles, NodeRoleGateway) {
			continue
		}

		gws[node.Name] = true
	}

	for name, dp := range c.GatewayDataplane {
		res = res &&
			gws[name] &&
			dp == CompStatusReady
	}

	for name, frr := range c.GatewayFRR {
		res = res &&
			gws[name] &&
			frr == CompStatusReady
	}

	return res
}

type FabOverrides struct {
	Versions Versions `json:"versions,omitempty"`
}

type FabConfig struct {
	Control       ControlConfig       `json:"control,omitempty"`
	Registry      RegistryConfig      `json:"registry,omitempty"`
	Fabric        FabricConfig        `json:"fabric,omitempty"`
	Gateway       GatewayConfig       `json:"gateway,omitempty"`
	Observability ObservabilityConfig `json:"observability,omitempty"`
}

type ControlConfig struct {
	ManagementSubnet meta.Prefix `json:"managementSubnet,omitempty"`
	VIP              meta.Prefix `json:"controlVIP,omitempty"`
	TLSSAN           []string    `json:"tlsSAN,omitempty"`

	JoinToken string `json:"joinToken,omitempty"`

	KubeClusterSubnet meta.Prefix `json:"kubeClusterSubnet,omitempty"`
	KubeServiceSubnet meta.Prefix `json:"kubeServiceSubnet,omitempty"`
	KubeClusterDNS    meta.Addr   `json:"kubeClusterDNS,omitempty"`

	DummySubnet meta.Prefix `json:"dummySubnet,omitempty"`

	DefaultUser ControlUser `json:"defaultUser,omitempty"`

	NTPServers []string `json:"ntpServers,omitempty"`

	Observability *ControlObservability `json:"observability,omitempty"`
}

type ControlUser struct {
	PasswordHash   string   `json:"password,omitempty"`
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`
}

type ControlObservability struct {
	KubePodLogs bool `json:"kubePodLogs,omitempty"`
	KubeEvents  bool `json:"kubeEvents,omitempty"`
}

type RegistryMode string

const (
	RegistryModeAirgap   RegistryMode = "airgap"
	RegistryModeUpstream RegistryMode = "upstream"
)

type RegistryConfig struct {
	Mode     RegistryMode                   `json:"mode,omitempty"`
	Upstream *ControlConfigRegistryUpstream `json:"upstream,omitempty"`
}

func (r RegistryConfig) IsAirgap() bool {
	return r.Mode == RegistryModeAirgap
}

type ControlConfigRegistryUpstream struct {
	Repo        string `json:"repo,omitempty"`   // ghcr.io
	Prefix      string `json:"prefix,omitempty"` // githedgehog
	NoTLSVerify bool   `json:"noTLSVerify,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
}

type FabricConfig struct {
	Mode fmeta.FabricMode `json:"mode,omitempty"`

	ManagementDHCPStart meta.Addr `json:"managementDHCPStart,omitempty"`
	ManagementDHCPEnd   meta.Addr `json:"managementDHCPEnd,omitempty"`

	SpineASN     uint32 `json:"spineASN,omitempty"`
	LeafASNStart uint32 `json:"leafASNStart,omitempty"`
	LeafASNEnd   uint32 `json:"leafASNEnd,omitempty"`

	ProtocolSubnet meta.Prefix `json:"protocolSubnet,omitempty"`
	VTEPSubnet     meta.Prefix `json:"vtepSubnet,omitempty"`
	FabricSubnet   meta.Prefix `json:"fabricSubnet,omitempty"`

	BaseVPCCommunity string            `json:"baseVPCCommunity,omitempty"`
	VPCIRBVLANs      []fmeta.VLANRange `json:"vpcIRBVLANs,omitempty"`

	LoopbackWorkaroundDisable bool              `json:"loopbackWorkaroundDisable,omitempty"` // TODO remove, kept for backward compat, ignored
	VPCWorkaroundVLANs        []fmeta.VLANRange `json:"vpcWorkaroundVLANs,omitempty"`
	VPCWorkaroundSubnet       meta.Prefix       `json:"vpcWorkaroundSubnet,omitempty"`

	TH5WorkaroundVLANs []fmeta.VLANRange `json:"th5WorkaroundVLANs,omitempty"`

	ESLAGMACBase   string `json:"eslagMACBase,omitempty"`
	ESLAGESIPrefix string `json:"eslagESIPrefix,omitempty"`

	MCLAGSessionSubnet meta.Prefix `json:"mclagSessionSubnet,omitempty"`

	DefaultSwitchUsers map[string]SwitchUser `json:"defaultSwitchUsers,omitempty"`
	DefaultAlloyConfig fmeta.AlloyConfig     `json:"defaultAlloyConfig,omitempty"` // TODO remove, kept for backward compat, ignored

	IncludeONIE bool `json:"includeONIE,omitempty"`
	IncludeCLS  bool `json:"includeCLS,omitempty"`

	DisableBFD bool `json:"disableBFD,omitempty"`

	Observability *fmeta.Observability `json:"observability,omitempty"`
}

type SwitchUser struct {
	PasswordHash   string   `json:"password,omitempty"`
	Role           string   `json:"role,omitempty"`
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`
}

type GatewayConfig struct {
	Enable        bool                  `json:"enable,omitempty"`
	ASN           uint32                `json:"asn,omitempty"`
	MAC           string                `json:"mac,omitempty"`
	Observability *GatewayObservability `json:"observability,omitempty"`
	Agentless     bool                  `json:"agentless,omitempty"`
	Communities   map[string]string     `json:"communities,omitempty"`
}

type GatewayObservability struct {
	Dataplane GatewayObservabilityDataplane `json:"dataplane,omitempty"`
	FRR       GatewayObservabilityFRR       `json:"frr,omitempty"`
	Unix      GatewayObservabilityUnix      `json:"unix,omitempty"`
}

type GatewayObservabilityDataplane struct {
	Metrics         bool                      `json:"metrics,omitempty"`
	MetricsInterval uint                      `json:"metricsInterval,omitempty"`
	MetricsRelabel  []alloy.ScrapeRelabelRule `json:"metricsRelabel,omitempty"`
}

type GatewayObservabilityFRR struct {
	Metrics         bool                      `json:"metrics,omitempty"`
	MetricsInterval uint                      `json:"metricsInterval,omitempty"`
	MetricsRelabel  []alloy.ScrapeRelabelRule `json:"metricsRelabel,omitempty"`
}

type GatewayObservabilityUnix struct {
	Metrics           bool                      `json:"metrics,omitempty"`
	MetricsInterval   uint                      `json:"metricsInterval,omitempty"`
	MetricsRelabel    []alloy.ScrapeRelabelRule `json:"metricsRelabel,omitempty"`
	MetricsCollectors []string                  `json:"metricsCollectors,omitempty"`
}

type ObservabilityConfig struct {
	Defaults ObservabilityDefaults `json:"defaults,omitempty"`
	Labels   map[string]string     `json:"labels,omitempty"`
	Targets  alloy.Targets         `json:"targets,omitempty"`
}

type ObservabilityDefaults string

const (
	ObservabilityDefaultsAll     ObservabilityDefaults = ""        // just enable everything by default
	ObservabilityDefaultsNone    ObservabilityDefaults = "none"    // disable all observability
	ObservabilityDefaultsMinimal ObservabilityDefaults = "minimal" // disable almost everything for testing
)

var ObservabilityDefaultsList = []ObservabilityDefaults{
	ObservabilityDefaultsAll,
	ObservabilityDefaultsNone,
	ObservabilityDefaultsMinimal,
}

type Versions struct {
	Platform   PlatformVersions   `json:"platform,omitempty"`
	Fabricator FabricatorVersions `json:"fabricator,omitempty"`
	Fabric     FabricVersions     `json:"fabric,omitempty"`
	Gateway    GatewayVersions    `json:"gateway,omitempty"`
	VLAB       VLABVersions       `json:"vlab,omitempty"`
}

type PlatformVersions struct {
	K3s               meta.Version `json:"k3s,omitempty"`
	Zot               meta.Version `json:"zot,omitempty"`
	ZotChart          meta.Version `json:"zotChart,omitempty"`
	CertManager       meta.Version `json:"certManager,omitempty"`
	K9s               meta.Version `json:"k9s,omitempty"`
	Toolbox           meta.Version `json:"toolbox,omitempty"`
	Reloader          meta.Version `json:"reloader,omitempty"`
	ReloaderChart     meta.Version `json:"reloaderChart,omitempty"`
	NTP               meta.Version `json:"ntp,omitempty"`
	NTPChart          meta.Version `json:"ntpChart,omitempty"`
	Alloy             meta.Version `json:"alloy,omitempty"`
	ControlProxy      meta.Version `json:"controlProxy,omitempty"`
	ControlProxyChart meta.Version `json:"controlProxyChart,omitempty"`
	BashCompletion    meta.Version `json:"bashCompletion,omitempty"`
}

type FabricatorVersions struct {
	API            meta.Version `json:"api,omitempty"`
	Controller     meta.Version `json:"controller,omitempty"`
	ControlUSBRoot meta.Version `json:"controlISORoot,omitempty"`
	Ctl            meta.Version `json:"ctl,omitempty"`
	NodeConfig     meta.Version `json:"nodeConfig,omitempty"`
	Pause          meta.Version `json:"pause,omitempty"`
	Flatcar        meta.Version `json:"flatcar,omitempty"`
}

type FabricVersions struct {
	API        meta.Version                   `json:"api,omitempty"`
	Controller meta.Version                   `json:"controller,omitempty"`
	DHCPD      meta.Version                   `json:"dhcpd,omitempty"`
	Boot       meta.Version                   `json:"boot,omitempty"`
	Agent      meta.Version                   `json:"agent,omitempty"`
	Ctl        meta.Version                   `json:"ctl,omitempty"`
	NOS        map[fmeta.NOSType]meta.Version `json:"nos,omitempty"`
	ONIE       map[string]meta.Version        `json:"onie,omitempty"`
}

type GatewayVersions struct {
	API        meta.Version `json:"api,omitempty"`
	Controller meta.Version `json:"controller,omitempty"`
	Agent      meta.Version `json:"agent,omitempty"`
	Dataplane  meta.Version `json:"dataplane,omitempty"`
	FRR        meta.Version `json:"frr,omitempty"`
}

type VLABVersions struct {
	ONIE    meta.Version `json:"onie,omitempty"`
	Flatcar meta.Version `json:"flatcar,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=hedgehog;fabricator,shortName=fab
// +kubebuilder:printcolumn:name="CtrlVer",type=string,JSONPath=`.status.versions.fabricator.controller`,priority=0
// +kubebuilder:printcolumn:name="Gen",type=string,JSONPath=`.metadata.generation`,priority=0
// +kubebuilder:printcolumn:name="Appl",type=string,JSONPath=`.status.lastAppliedGen`,priority=0
// +kubebuilder:printcolumn:name="ApplT",type=date,JSONPath=`.status.lastAppliedTime`,priority=0
// +kubebuilder:printcolumn:name="Status",type=date,JSONPath=`.status.lastStatusCheck`,priority=0
// +kubebuilder:printcolumn:name="Registry",type=string,JSONPath=`.status.components.zot`,priority=1
// +kubebuilder:printcolumn:name="Ctrl",type=string,JSONPath=`.status.components.fabricatorCtrl`,priority=1
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,priority=0
// +kubebuilder:printcolumn:name="GwReady",type=string,JSONPath=`.status.conditions[?(@.type=="GatewayReady")].status`,priority=0
// Fabricator defines configuration for the Fabricator controller
type Fabricator struct {
	kmetav1.TypeMeta   `json:",inline"`
	kmetav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FabricatorSpec   `json:"spec,omitempty"`
	Status FabricatorStatus `json:"status,omitempty"`
}

const (
	KindFabricator = "Fabricator"
)

// +kubebuilder:object:root=true

// FabricatorList contains a list of Fabricator
type FabricatorList struct {
	kmetav1.TypeMeta `json:",inline"`
	kmetav1.ListMeta `json:"metadata,omitempty"`
	Items            []Fabricator `json:"items"`
}

var fabricatorValidate *validator.Validate

func init() {
	SchemeBuilder.Register(&Fabricator{}, &FabricatorList{})

	fabricatorValidate = validator.New()

	fabricatorValidate.RegisterCustomTypeFunc(func(field reflect.Value) interface{} {
		if version, ok := field.Interface().(meta.Version); ok {
			_, err := version.Parse()
			if err != nil {
				return err //nolint:wrapcheck
			}
		}

		return nil
	}, meta.Version(""))

	fabricatorValidate.RegisterCustomTypeFunc(func(field reflect.Value) interface{} {
		if addr, ok := field.Interface().(meta.Addr); ok {
			_, err := addr.Parse()
			if err != nil {
				return err //nolint:wrapcheck
			}
		}

		return nil
	}, meta.Addr(""))

	fabricatorValidate.RegisterCustomTypeFunc(func(field reflect.Value) interface{} {
		if prefix, ok := field.Interface().(meta.Prefix); ok {
			_, err := prefix.Parse()
			if err != nil {
				return err //nolint:wrapcheck
			}
		}

		return nil
	}, meta.Prefix(""))
}

func (f *Fabricator) Default() {
	if len(f.Spec.Config.Control.NTPServers) == 0 {
		f.Spec.Config.Control.NTPServers = []string{
			"time.cloudflare.com",
			"time1.google.com",
			"time2.google.com",
			"time3.google.com",
			"time4.google.com",
		}
	}

	if f.Spec.Config.Fabric.LeafASNEnd == 65534 {
		if f.Spec.Config.Gateway.ASN == 0 || f.Spec.Config.Gateway.ASN == 65534 {
			f.Spec.Config.Fabric.LeafASNEnd = 65533
			f.Spec.Config.Gateway.ASN = 65534
		}
	}

	// apply new default to IRB VLANs if it was unchanged, to avoid TH5 clash during upgrade
	if len(f.Spec.Config.Fabric.VPCIRBVLANs) == 1 && f.Spec.Config.Fabric.VPCIRBVLANs[0].From == 3000 && f.Spec.Config.Fabric.VPCIRBVLANs[0].To == 3999 {
		f.Spec.Config.Fabric.VPCIRBVLANs[0].To = 3899
	}

	if len(f.Spec.Config.Fabric.TH5WorkaroundVLANs) == 0 {
		f.Spec.Config.Fabric.TH5WorkaroundVLANs = []fmeta.VLANRange{
			{From: 3900, To: 3999},
		}
	}

	if len(f.Spec.Config.Gateway.Communities) == 0 {
		f.Spec.Config.Gateway.Communities = map[string]string{
			"0": "50001:0",
			"1": "50001:1",
			"2": "50001:2",
			"3": "50001:3",
			"4": "50001:4",
			"5": "50001:5",
			"6": "50001:6",
			"7": "50001:7",
			"8": "50001:8",
			"9": "50001:9",
		}
	}

	f.Spec.Config.Fabric.LoopbackWorkaroundDisable = false // it's ignored now

	o11yNotNone := f.Spec.Config.Observability.Defaults != ObservabilityDefaultsNone
	o11yMinimal := f.Spec.Config.Observability.Defaults == ObservabilityDefaultsMinimal

	if f.Spec.Config.Control.Observability == nil {
		f.Spec.Config.Control.Observability = &ControlObservability{
			KubePodLogs: o11yNotNone,
			KubeEvents:  o11yNotNone,
		}
	}

	if f.Spec.Config.Fabric.Observability == nil {
		f.Spec.Config.Fabric.Observability = &fmeta.Observability{
			Agent: fmeta.ObservabilityAgent{
				Metrics:         o11yNotNone,
				MetricsInterval: 60,
				Logs:            true,
			},
			Unix: fmeta.ObservabilityUnix{
				Metrics:           o11yNotNone,
				MetricsInterval:   60,
				MetricsCollectors: []string{"cpu", "loadavg", "meminfo", "filesystem"},
				Syslog:            true,
			},
		}
		if o11yMinimal {
			f.Spec.Config.Fabric.Observability.Agent.MetricsRelabel = []alloy.ScrapeRelabelRule{
				{
					Action:       "keep",
					Regex:        ".*(_in_bits|_status|_generation|_temperature|_transceiver).*",
					SourceLabels: []string{"__name__"},
				},
			}
			f.Spec.Config.Fabric.Observability.Unix.MetricsRelabel = []alloy.ScrapeRelabelRule{
				{
					Action:       "keep",
					Regex:        ".*(_load).*",
					SourceLabels: []string{"__name__"},
				},
			}
		}
	}

	if f.Spec.Config.Gateway.Observability == nil {
		f.Spec.Config.Gateway.Observability = &GatewayObservability{
			Dataplane: GatewayObservabilityDataplane{
				Metrics:         o11yNotNone,
				MetricsInterval: 60,
			},
			FRR: GatewayObservabilityFRR{
				Metrics:         o11yNotNone,
				MetricsInterval: 60,
			},
			Unix: GatewayObservabilityUnix{
				Metrics:           o11yNotNone,
				MetricsInterval:   60,
				MetricsCollectors: []string{"cpu", "loadavg", "meminfo", "filesystem"},
			},
		}
		if o11yMinimal {
			f.Spec.Config.Gateway.Observability.Unix.MetricsRelabel = []alloy.ScrapeRelabelRule{
				{
					Action:       "keep",
					Regex:        ".*(_load).*",
					SourceLabels: []string{"__name__"},
				},
			}
		}
	}

	if f.Spec.Config.Observability.Targets.Prometheus == nil {
		f.Spec.Config.Observability.Targets.Prometheus = map[string]alloy.PrometheusTarget{}
	}
	for name, target := range f.Spec.Config.Fabric.DefaultAlloyConfig.PrometheusTargets {
		if _, ok := f.Spec.Config.Observability.Targets.Prometheus[name]; !ok {
			f.Spec.Config.Observability.Targets.Prometheus[name] = alloy.PrometheusTarget{
				Target: alloy.Target{
					URL: target.URL,
					BasicAuth: &alloy.TargetBasicAuth{
						Username: target.BasicAuth.Username,
						Password: target.BasicAuth.Password,
					},
					BearerToken:        target.BearerToken,
					Labels:             target.Labels,
					InsecureSkipVerify: target.InsecureSkipVerify,
					CAPEM:              target.CAPEM,
					CertPEM:            target.CertPEM,
				},
				SendIntervalSeconds: target.SendIntervalSeconds,
			}
		}
	}

	if f.Spec.Config.Observability.Targets.Loki == nil {
		f.Spec.Config.Observability.Targets.Loki = map[string]alloy.LokiTarget{}
	}
	for name, target := range f.Spec.Config.Fabric.DefaultAlloyConfig.LokiTargets {
		if _, ok := f.Spec.Config.Observability.Targets.Loki[name]; !ok {
			f.Spec.Config.Observability.Targets.Loki[name] = alloy.LokiTarget{
				Target: alloy.Target{
					URL: target.URL,
					BasicAuth: &alloy.TargetBasicAuth{
						Username: target.BasicAuth.Username,
						Password: target.BasicAuth.Password,
					},
					BearerToken:        target.BearerToken,
					Labels:             target.Labels,
					InsecureSkipVerify: target.InsecureSkipVerify,
					CAPEM:              target.CAPEM,
					CertPEM:            target.CertPEM,
				},
			}
		}
	}

	if f.Spec.Config.Observability.Targets.Pyroscope == nil {
		f.Spec.Config.Observability.Targets.Pyroscope = map[string]alloy.PyroscopeTarget{}
	}

	f.Spec.Config.Fabric.DefaultAlloyConfig = fmeta.AlloyConfig{}

	for name, target := range f.Spec.Config.Observability.Targets.Prometheus {
		if target.Labels == nil {
			target.Labels = map[string]string{}
		}
		for k, v := range f.Spec.Config.Observability.Labels {
			if _, ok := target.Labels[k]; !ok {
				target.Labels[k] = v
			}
		}
		f.Spec.Config.Observability.Targets.Prometheus[name] = target
	}
	for name, target := range f.Spec.Config.Observability.Targets.Loki {
		if target.Labels == nil {
			target.Labels = map[string]string{}
		}
		for k, v := range f.Spec.Config.Observability.Labels {
			if _, ok := target.Labels[k]; !ok {
				target.Labels[k] = v
			}
		}
		f.Spec.Config.Observability.Targets.Loki[name] = target
	}
	for name, target := range f.Spec.Config.Observability.Targets.Pyroscope {
		if target.Labels == nil {
			target.Labels = map[string]string{}
		}
		for k, v := range f.Spec.Config.Observability.Labels {
			if _, ok := target.Labels[k]; !ok {
				target.Labels[k] = v
			}
		}
		f.Spec.Config.Observability.Targets.Pyroscope[name] = target
	}
}

func (f *Fabricator) Validate(ctx context.Context) error {
	f.Default()

	if f.Name != FabName {
		return fmt.Errorf("fabricator name must be %q", FabName) //nolint:goerr113
	}

	if f.Namespace != FabNamespace {
		return fmt.Errorf("fabricator namespace must be %q", FabNamespace) //nolint:goerr113
	}

	err := fabricatorValidate.StructCtx(ctx, f)
	if err != nil {
		return fmt.Errorf("validating: %w", err)
	}

	if f.Spec.Config.Registry.Mode != RegistryModeAirgap && f.Spec.Config.Registry.Mode != RegistryModeUpstream {
		return fmt.Errorf("invalid registry mode %q", f.Spec.Config.Registry.Mode) //nolint:goerr113
	}

	if f.Spec.Config.Registry.IsAirgap() && f.Spec.Config.Registry.Upstream != nil {
		return fmt.Errorf("airgap registry doesn't support upstream") //nolint:goerr113
	}

	if !f.Spec.Config.Registry.IsAirgap() {
		if f.Spec.Config.Registry.Upstream == nil {
			return fmt.Errorf("non-airgap registry requires upstream") //nolint:goerr113
		}

		if f.Spec.Config.Registry.Upstream.Repo == "" {
			return fmt.Errorf("upstream registry requires repo") //nolint:goerr113
		}
	}

	mgmtSubnet, err := f.Spec.Config.Control.ManagementSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing management subnet: %w", err)
	}

	mgmtDHCPStart, err := f.Spec.Config.Fabric.ManagementDHCPStart.Parse()
	if err != nil {
		return fmt.Errorf("parsing management DHCP start: %w", err)
	}
	if !mgmtSubnet.Contains(mgmtDHCPStart) {
		return fmt.Errorf("management DHCP start not in management subnet") //nolint:goerr113
	}

	mgmtDHCPEnd, err := f.Spec.Config.Fabric.ManagementDHCPEnd.Parse()
	if err != nil {
		return fmt.Errorf("parsing management DHCP end: %w", err)
	}
	if !mgmtSubnet.Contains(mgmtDHCPEnd) {
		return fmt.Errorf("management DHCP end not in management subnet") //nolint:goerr113
	}

	controlVIP, err := f.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}
	if !mgmtSubnet.Contains(controlVIP.Addr()) {
		return fmt.Errorf("control VIP not in management subnet") //nolint:goerr113
	}
	if controlVIP.Bits() != 32 {
		return fmt.Errorf("control VIP must be /32") //nolint:goerr113
	}

	kubeServiceSubnet, err := f.Spec.Config.Control.KubeServiceSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing kube service subnet: %w", err)
	}

	kubeClusterSubnet, err := f.Spec.Config.Control.KubeClusterSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing kube cluster subnet: %w", err)
	}

	kubeClusterDNS, err := f.Spec.Config.Control.KubeClusterDNS.Parse()
	if err != nil {
		return fmt.Errorf("parsing kube cluster DNS: %w", err)
	}
	if !kubeServiceSubnet.Contains(kubeClusterDNS) {
		return fmt.Errorf("kube cluster DNS not in kube service subnet") //nolint:goerr113
	}

	if kubeClusterSubnet.Overlaps(kubeServiceSubnet) {
		return fmt.Errorf("kube cluster subnet overlaps kube service subnet") //nolint:goerr113
	}

	if mgmtSubnet.Overlaps(kubeServiceSubnet) {
		return fmt.Errorf("management subnet overlaps kube service subnet") //nolint:goerr113
	}

	if mgmtSubnet.Overlaps(kubeClusterSubnet) {
		return fmt.Errorf("management subnet overlaps kube cluster subnet") //nolint:goerr113
	}

	dummySubnet, err := f.Spec.Config.Control.DummySubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing dummy subnet: %w", err)
	}

	{
		ph := f.Spec.Config.Control.DefaultUser.PasswordHash
		if ph == "" {
			return fmt.Errorf("default control user password hash is required") //nolint:goerr113
		}

		if !strings.HasPrefix(ph, "$5$") {
			return fmt.Errorf("default control user password hash must be bcrypt") //nolint:goerr113
		}
	}

	{
		admin := false
		for username, user := range f.Spec.Config.Fabric.DefaultSwitchUsers {
			if username == "admin" && user.Role == "admin" {
				admin = true
			}

			if !slices.Contains([]string{"admin", "operator"}, user.Role) {
				return fmt.Errorf("invalid switch user %q role %q", username, user.Role) //nolint:goerr113
			}

			if user.PasswordHash == "" {
				return fmt.Errorf("switch user %q password hash is required", username) //nolint:goerr113
			}

			if !strings.HasPrefix(user.PasswordHash, "$5$") {
				return fmt.Errorf("switch user %q password hash must be bcrypt", username) //nolint:goerr113
			}
		}

		if !admin {
			return fmt.Errorf("admin switch user with role admin is required") //nolint:goerr113
		}
	}

	fm := f.Spec.Config.Fabric.Mode
	if !slices.Contains(fmeta.FabricModes, fm) {
		return fmt.Errorf("invalid fabric mode %q", fm) //nolint:goerr113
	}

	if fm == fmeta.FabricModeSpineLeaf {
		if f.Spec.Config.Fabric.SpineASN == 0 {
			return fmt.Errorf("spine ASN is required for spine-leaf mode") //nolint:goerr113
		}
		if f.Spec.Config.Fabric.SpineASN >= f.Spec.Config.Fabric.LeafASNStart && f.Spec.Config.Fabric.SpineASN <= f.Spec.Config.Fabric.LeafASNEnd {
			return fmt.Errorf("spine ASN must be outside leaf ASN range") //nolint:goerr113
		}

		if f.Spec.Config.Fabric.LeafASNStart == 0 {
			return fmt.Errorf("leaf ASN start is required for spine-leaf mode") //nolint:goerr113
		}
		if f.Spec.Config.Fabric.LeafASNEnd == 0 {
			return fmt.Errorf("leaf ASN end is required for spine-leaf mode") //nolint:goerr113
		}
		if f.Spec.Config.Fabric.LeafASNStart >= f.Spec.Config.Fabric.LeafASNEnd {
			return fmt.Errorf("leaf ASN start must be less than leaf ASN end") //nolint:goerr113
		}

		if f.Spec.Config.Gateway.ASN == 0 {
			return fmt.Errorf("gateway ASN is required for spine-leaf mode") //nolint:goerr113
		}
		if f.Spec.Config.Gateway.ASN == f.Spec.Config.Fabric.SpineASN {
			return fmt.Errorf("gateway ASN must be different from spine ASN") //nolint:goerr113
		}
		if f.Spec.Config.Gateway.ASN >= f.Spec.Config.Fabric.LeafASNStart && f.Spec.Config.Gateway.ASN <= f.Spec.Config.Fabric.LeafASNEnd {
			return fmt.Errorf("gateway ASN must be outside leaf ASN range") //nolint:goerr113
		}
	}

	protoSubnet, err := f.Spec.Config.Fabric.ProtocolSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing protocol subnet: %w", err)
	}

	vtepSubnet, err := f.Spec.Config.Fabric.VTEPSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing VTEP subnet: %w", err)
	}

	fabricSubnet, err := f.Spec.Config.Fabric.FabricSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing fabric subnet: %w", err)
	}

	mclagSessionSubnet, err := f.Spec.Config.Fabric.MCLAGSessionSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing MCLAG session subnet: %w", err)
	}

	vpcLoopSubnet, err := f.Spec.Config.Fabric.VPCWorkaroundSubnet.Parse()
	if err != nil {
		return fmt.Errorf("parsing VPC workaround subnet: %w", err)
	}

	// TODO validate actual community
	if f.Spec.Config.Fabric.BaseVPCCommunity == "" {
		return fmt.Errorf("base VPC community is required") //nolint:goerr113
	}

	// TODO validate actual VLANs and that it's a reasonable range
	if len(f.Spec.Config.Fabric.VPCIRBVLANs) == 0 {
		return fmt.Errorf("VPC IRB VLANs are required") //nolint:goerr113
	}

	// TODO validate actual VLANs and that it's a reasonable range
	if len(f.Spec.Config.Fabric.VPCWorkaroundVLANs) == 0 {
		return fmt.Errorf("VPC workaround VLANs are required") //nolint:goerr113
	}

	// TODO validate actual VLANs and that it's a reasonable range
	if len(f.Spec.Config.Fabric.TH5WorkaroundVLANs) == 0 {
		return fmt.Errorf("TH5 workaround VLANs are required") //nolint:goerr113
	}

	// Ensure that TH5 and IRB VLANs do not overlap. VPC workaround VLANs are not a problem
	// as they are used on subinterfaces instead
	if err := fmeta.CheckVLANRangesOverlap(append(slices.Clone(f.Spec.Config.Fabric.TH5WorkaroundVLANs), f.Spec.Config.Fabric.VPCIRBVLANs...)); err != nil {
		return fmt.Errorf("overlap between TH5 workaround VLAN ranges and IRB VLAN ranges: %w", err)
	}

	// TODO validate MAC base
	if f.Spec.Config.Fabric.ESLAGMACBase == "" {
		return fmt.Errorf("ESLAG MAC base is required") //nolint:goerr113
	}

	if f.Spec.Config.Fabric.ESLAGESIPrefix == "" {
		return fmt.Errorf("ESLAG ESI prefix is required") //nolint:goerr113
	}

	// TODO validate Alloy config

	reservedSubnets := []netip.Prefix{
		mgmtSubnet,
		protoSubnet,
		vtepSubnet,
		fabricSubnet,
		dummySubnet,
		mclagSessionSubnet,
		vpcLoopSubnet,
	}

	for someIdx, some := range reservedSubnets {
		for otherIdx, other := range reservedSubnets {
			if someIdx == otherIdx {
				continue
			}

			if some.Overlaps(other) {
				return fmt.Errorf("subnets %s and %s overlap", some, other) //nolint:goerr113
			}
		}
	}

	if !slices.Contains(ObservabilityDefaultsList, f.Spec.Config.Observability.Defaults) {
		return fmt.Errorf("invalid observability defaults mode: %s", f.Spec.Config.Observability.Defaults) //nolint:err113
	}

	for prioStr, commStr := range f.Spec.Config.Gateway.Communities {
		if _, err := strconv.ParseUint(prioStr, 10, 32); err != nil {
			return fmt.Errorf("config: gatewayCommunity priority %s is invalid: %w", prioStr, err)
		}
		parts := strings.Split(commStr, ":")
		if len(parts) != 2 {
			return fmt.Errorf("config: gatewayCommunity community %s format is invalid", commStr) //nolint:err113
		}
		if _, err := strconv.ParseUint(parts[0], 10, 16); err != nil {
			return fmt.Errorf("config: gatewayCommunity community %s is invalid: %w", commStr, err)
		}
		if _, err := strconv.ParseUint(parts[1], 10, 16); err != nil {
			return fmt.Errorf("config: gatewayCommunity community %s is invalid: %w", commStr, err)
		}
	}

	return f.CheckForKnownSwitchUsers()
}

func (f *Fabricator) CalculateVersions(def Versions) error {
	f.Status.Versions = *f.Spec.Overrides.Versions.DeepCopy()

	if err := mergo.Merge(&f.Status.Versions, def); err != nil {
		return fmt.Errorf("merging versions: %w", err)
	}

	if !f.Spec.Config.Fabric.IncludeONIE {
		f.Status.Versions.Fabric.ONIE = map[string]meta.Version{}
	}

	return nil
}

var knownSwitchUsers = []string{
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
	fmeta.AlloyUser,
}

func (f *Fabricator) CheckForKnownSwitchUsers() error {
	for userName := range f.Spec.Config.Fabric.DefaultSwitchUsers {
		if slices.Contains(knownSwitchUsers, userName) {
			return fmt.Errorf("switch user can't be named %q", userName) //nolint:goerr113
		}
	}

	return nil
}
