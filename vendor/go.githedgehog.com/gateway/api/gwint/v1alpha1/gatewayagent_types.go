// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type VPCInfoData struct {
	gwapi.VPCInfoSpec   `json:",inline"`
	gwapi.VPCInfoStatus `json:",inline"`
}

// GatewayAgentSpec defines the desired state of GatewayAgent.
type GatewayAgentSpec struct {
	// CtrlVersion is the version of the gateway controller to trigger generation changes on controler upgrade
	CtrlVersion string                       `json:"ctrlVersion,omitempty"`
	Gateway     gwapi.GatewaySpec            `json:"gateway,omitempty"`
	VPCs        map[string]VPCInfoData       `json:"vpcs,omitempty"`
	Peerings    map[string]gwapi.PeeringSpec `json:"peerings,omitempty"`
}

// GatewayAgentStatus defines the observed state of GatewayAgent.
type GatewayAgentStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=hedgehog;hedgehog-gateway,shortName=gwag
// GatewayAgent is the Schema for the gatewayagents API.
type GatewayAgent struct {
	kmetav1.TypeMeta   `json:",inline"`
	kmetav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GatewayAgentSpec   `json:"spec,omitempty"`
	Status GatewayAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GatewayAgentList contains a list of GatewayAgent.
type GatewayAgentList struct {
	kmetav1.TypeMeta `json:",inline"`
	kmetav1.ListMeta `json:"metadata,omitempty"`
	Items            []GatewayAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GatewayAgent{}, &GatewayAgentList{})
}
