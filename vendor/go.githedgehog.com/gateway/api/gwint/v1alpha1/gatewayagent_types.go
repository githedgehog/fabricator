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
	// AgentVersion is the desired version of the gateway agent to trigger generation changes on controller upgrades
	AgentVersion string                       `json:"agentVersion,omitempty"`
	Gateway      gwapi.GatewaySpec            `json:"gateway,omitempty"`
	VPCs         map[string]VPCInfoData       `json:"vpcs,omitempty"`
	Peerings     map[string]gwapi.PeeringSpec `json:"peerings,omitempty"`
}

// GatewayAgentStatus defines the observed state of GatewayAgent.
type GatewayAgentStatus struct {
	// AgentVersion is the version of the gateway agent
	AgentVersion string `json:"agentVersion,omitempty"`
	// Time of the last successful configuration application
	LastAppliedTime kmetav1.Time `json:"lastAppliedTime,omitempty"`
	// Generation of the last successful configuration application
	LastAppliedGen int64 `json:"lastAppliedGen,omitempty"`
	// Time of the last heartbeat from the agent
	LastHeartbeat kmetav1.Time `json:"lastHeartbeat,omitempty"`
	// State represents collected data from the dataplane API that includes FRR as well
	State GatewayState `json:"state,omitempty"`
}

// GatewayState represents collected data from the dataplane API that includes FRR as well
type GatewayState struct {
	// LastCollectedTime is the time of the last successful collection of data from the dataplane API
	LastCollectedTime kmetav1.Time `json:"lastCollectedTime,omitempty"`
	// FRR is the status of the FRR daemon
	FRR FRRStatus `json:"frr,omitempty"`
	// VPCs is the status of the VPCs where key is the vpc (vpcinfo) name
	VPCs map[string]VPCStatus `json:"vpcs,omitempty"`
	// Peerings is the status of the VPCs peerings where key is VPC1->VPC2 and data is for one direction only
	Peerings map[string]PeeringStatus `json:"peerings,omitempty"`
}

// FRRStatus represents the status of the FRR daemon
type FRRStatus struct {
	// LastAppliedGen is the generation of the last successful application of a configuration to the FRR
	LastAppliedGen int64 `json:"lastAppliedGen,omitempty"`
}

type VPCStatus struct {
	// Packets is the number of packets sent on the vpc
	Packets uint64 `json:"p,omitempty"`
	// Bytes is the number of bytes sent on the vpc
	Bytes uint64 `json:"b,omitempty"`
	// Drops is the number of packets dropped on the vpc
	Drops uint64 `json:"d,omitempty"`
}

// PeeringStatus represents the status of a peering between a pair of VPCs in one direction
type PeeringStatus struct {
	// Packets is the number of packets sent on the peering
	Packets uint64 `json:"p,omitempty"`
	// Bytes is the number of bytes sent on the peering
	Bytes uint64 `json:"b,omitempty"`
	// Drops is the number of packets dropped on the peering
	Drops uint64 `json:"d,omitempty"`
	// BytesPerSecond is the number of bytes sent per second on the peering
	BytesPerSecond float64 `json:"bps,omitempty"`
	// PktsPerSecond is the number of packets sent per second on the peering
	PktsPerSecond float64 `json:"pps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=hedgehog;hedgehog-gateway,shortName=gwag
// +kubebuilder:printcolumn:name="Applied",type=date,JSONPath=`.status.lastAppliedTime`,priority=0
// +kubebuilder:printcolumn:name="AppliedG",type=integer,JSONPath=`.status.lastAppliedGen`,priority=0
// +kubebuilder:printcolumn:name="CurrentG",type=integer,JSONPath=`.metadata.generation`,priority=0
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.agentVersion`,priority=0
// +kubebuilder:printcolumn:name="ProtoIP",type=string,JSONPath=`.spec.gateway.protocolIP`,priority=1
// +kubebuilder:printcolumn:name="VTEPIP",type=string,JSONPath=`.spec.gateway.vtepIP`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,priority=0
// GatewayAgent is the Schema for the gatewayagents API.
type GatewayAgent struct {
	kmetav1.TypeMeta   `json:",inline"`
	kmetav1.ObjectMeta `json:"metadata,omitempty"`

	// +structType=atomic
	Spec GatewayAgentSpec `json:"spec,omitempty"`

	// +structType=atomic
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
