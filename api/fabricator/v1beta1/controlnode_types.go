// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"

	"go.githedgehog.com/fabricator/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ControlNodeSpec struct {
	Bootstrap  ControlNodeBootstrap  `json:"bootstrap,omitempty"`
	Management ControlNodeManagement `json:"management,omitempty"`
	External   ControlNodeExternal   `json:"external,omitempty"`
	Dummy      ControlNodeDummy      `json:"dummy,omitempty"`
}

type ControlNodeBootstrap struct {
	Disk string `json:"disk,omitempty"`
}

type ControlNodeManagement struct {
	IP        meta.Prefix `json:"ip,omitempty"`
	Interface string      `json:"interface,omitempty"`
	// TODO support bond
}

type ControlNodeExternal struct {
	IP        meta.PrefixOrDHCP `json:"ip,omitempty"`
	Gateway   meta.Addr         `json:"gateway,omitempty"`
	DNS       []meta.Addr       `json:"dns,omitempty"`
	Interface string            `json:"interface,omitempty"`
	// TODO support bond
}

type ControlNodeDummy struct {
	IP meta.Prefix `json:"ip,omitempty"`
}

type ControlNodeStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type ControlNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ControlNodeSpec   `json:"spec,omitempty"`
	Status ControlNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ControlNodeList contains a list of ControlNode
type ControlNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ControlNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ControlNode{}, &ControlNodeList{})
}

func (c *ControlNode) Validate(ctx context.Context, fabCfg *FabConfig, allowNotHydrated bool) error {
	if fabCfg == nil {
		return nil
	}

	// TODO make interactive/non-interactive and iso/non-iso validation
	// TODO validate the control node spec

	return nil
}
