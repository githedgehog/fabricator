/*
Copyright 2024 Hedgehog.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1beta1

import (
	"context"
	"fmt"
	"reflect"

	"dario.cat/mergo"
	"github.com/go-playground/validator/v10"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabricator/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type FabricatorSpec struct {
	Config    FabConfig    `json:"config,omitempty"`
	Overrides FabOverrides `json:"overrides,omitempty"`
}

type FabricatorStatus struct {
	IsBootstrap bool     `json:"isBootstrap,omitempty"`
	Versions    Versions `json:"versions,omitempty"`
	// TODO reserved VLANs, subnets, etc.
}

type FabOverrides struct {
	Versions Versions `json:"versions,omitempty"`
}

type FabConfig struct {
	Control  ControlConfig  `json:"control,omitempty"`
	Registry RegistryConfig `json:"registry,omitempty"`
	Fabric   FabricConfig   `json:"fabric,omitempty"`
}

type ControlConfig struct {
	ManagementSubnet meta.Prefix `json:"managementSubnet,omitempty"` // TODO should be reserved
	VIP              meta.Addr   `json:"controlVIP,omitempty"`       // TODO maybe ManagementVIP?
	TLSSAN           []string    `json:"tlsSAN,omitempty"`           // TODO make sure 127.0.0.1 and controlVIP are always present

	KubeClusterSubnet meta.Prefix `json:"kubeClusterSubnet,omitempty"`
	KubeServiceSubnet meta.Prefix `json:"kubeServiceSubnet,omitempty"`
	KubeClusterDNS    meta.Addr   `json:"kubeClusterDNS,omitempty"` // Should be from the service CIDR

	DefaultUser ControlUser `json:"defaultUser,omitempty"`
}

type ControlUser struct {
	PasswordHash   string   `json:"password,omitempty"`
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`
}

type RegistryConfig struct {
	// Airgap bool `json:"airgap,omitempty"` // TODO

	// TODO implement non-airgap
	// TODO if airgap is true, cache should be empty
	// Cache  ControlConfigRegistryCache `json:"cache,omitempty"`
}

type ControlConfigRegistryCache struct {
	Repo     string `json:"repo,omitempty"`   // ghcr.io
	Prefix   string `json:"prefix,omitempty"` // githedgehog
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type FabricConfig struct {
	Mode fmeta.FabricMode `json:"mode,omitempty"`

	ManagementDHCPStart meta.Addr `json:"managementDHCPStart,omitempty"` // TODO should be in mgmt subnet

	SpineASN     uint32 `json:"spineASN,omitempty"`
	LeafASNStart uint32 `json:"leafASNStart,omitempty"`
	LeafASNEnd   uint32 `json:"leafASNEnd,omitempty"`

	ProtocolSubnet meta.Prefix `json:"protocolSubnet,omitempty"` // TODO should be reserved
	VTEPSubnet     meta.Prefix `json:"vtepSubnet,omitempty"`     // TODO should be reserved
	FabricSubnet   meta.Prefix `json:"fabricSubnet,omitempty"`   // TODO should be reserved

	BaseVPCCommunity string            `json:"baseVPCCommunity,omitempty"` // TODO should be reserved
	VPCIRBVLANs      []fmeta.VLANRange `json:"vpcIRBVLANs,omitempty"`      // TODO should be reserved

	VPCWorkaroundVLANs  []fmeta.VLANRange `json:"vpcWorkaroundVLANs,omitempty"`  // don't need to be reserved
	VPCWorkaroundSubnet meta.Prefix       `json:"vpcWorkaroundSubnet,omitempty"` // TODO have to be reserved!

	ESLAGMACBase   string `json:"eslagMACBase,omitempty"`
	ESLAGESIPrefix string `json:"eslagESIPrefix,omitempty"`

	DefaultSwitchUsers map[string]SwitchUser `json:"defaultSwitchUsers,omitempty"` // TODO make sure admin user is always present
	DefaultAlloyConfig fmeta.AlloyConfig     `json:"defaultAlloyConfig,omitempty"`
}

type SwitchUser struct {
	PasswordHash   string   `json:"password,omitempty"`
	Role           string   `json:"role,omitempty"` // TODO enum/validate
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`
}

type Versions struct {
	Platform   PlatformVersions   `json:"platform,omitempty"`
	Fabricator FabricatorVersions `json:"fabricator,omitempty"`
	Fabric     FabricVersions     `json:"fabric,omitempty"`
	VLAB       VLABVersions       `json:"vlab,omitempty"`
}

type PlatformVersions struct {
	K3s          meta.Version `json:"k3s,omitempty"`
	Zot          meta.Version `json:"zot,omitempty"`
	CertManager  meta.Version `json:"certManager,omitempty"`
	K9s          meta.Version `json:"k9s,omitempty"`
	Toolbox      meta.Version `json:"toolbox,omitempty"`
	Reloader     meta.Version `json:"reloader,omitempty"`
	ControlProxy meta.Version `json:"controlProxy,omitempty"`
}

type FabricatorVersions struct {
	API            meta.Version `json:"api,omitempty"`
	Controller     meta.Version `json:"controller,omitempty"`
	ControlISORoot meta.Version `json:"controlISORoot,omitempty"`
}

type FabricVersions struct {
	API          meta.Version            `json:"api,omitempty"`
	Controller   meta.Version            `json:"controller,omitempty"`
	DHCPD        meta.Version            `json:"dhcpd,omitempty"`
	Boot         meta.Version            `json:"boot,omitempty"`
	Agent        meta.Version            `json:"agent,omitempty"`
	ControlAgent meta.Version            `json:"controlAgent,omitempty"`
	Ctl          meta.Version            `json:"ctl,omitempty"`
	Alloy        meta.Version            `json:"alloy,omitempty"`
	NOS          map[string]meta.Version `json:"nos,omitempty"`
}

type VLABVersions struct {
	ONIE    meta.Version `json:"onie,omitempty"`
	Flatcar meta.Version `json:"flatcar,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=hedgehog;fabricator,shortName=fab
// Fabricator defines configuration for the Fabricator controller
type Fabricator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FabricatorSpec   `json:"spec,omitempty"`
	Status FabricatorStatus `json:"status,omitempty"`
}

const (
	KindFabricator = "Fabricator"
)

// +kubebuilder:object:root=true

// FabricatorList contains a list of Fabricator
type FabricatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Fabricator `json:"items"`
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

func (f *Fabricator) Validate(ctx context.Context) error {
	err := fabricatorValidate.StructCtx(ctx, f)
	if err != nil {
		return fmt.Errorf("validating: %w", err)
	}

	return nil
}

func (f *Fabricator) CalculateVersions(def Versions) error {
	f.Status.Versions = *f.Spec.Overrides.Versions.DeepCopy()

	if err := mergo.Merge(&f.Status.Versions, def); err != nil {
		return fmt.Errorf("merging versions: %w", err)
	}

	return nil
}
