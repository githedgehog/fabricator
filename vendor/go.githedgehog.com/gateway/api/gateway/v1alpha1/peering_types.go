// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PeeringSpec defines the desired state of Peering.
type PeeringSpec struct {
	// Peerings is a map of peering entries for each VPC participating in the peering (keyed by VPC name)
	Peering map[string]*PeeringEntry `json:"peering,omitempty"`
}

type PeeringEntry struct {
	IPs     []PeeringEntryIP      `json:"ips,omitempty"`
	As      []PeeringEntryAs      `json:"as,omitempty"`
	Ingress []PeeringEntryIngress `json:"ingress,omitempty"`
	// TODO add natType: stateful # as there are not enough IPs in the "as" pool
	// TODO add metric: 0 # add 0 to the advertised route metrics
}

type PeeringEntryIP struct {
	CIDR      string `json:"cidr,omitempty"`
	Not       string `json:"not,omitempty"`
	VPCSubnet string `json:"vpcSubnet,omitempty"`
}

type PeeringEntryAs struct {
	CIDR string `json:"cidr,omitempty"`
	Not  string `json:"not,omitempty"`
}

type PeeringEntryIngress struct {
	Allow *PeeringEntryIngressAllow `json:"allow,omitempty"`
	// TODO add deny?
}

type PeeringEntryIngressAllow struct {
	// TODO add actual fields
	// stateless: true
	// 	 tcp:
	//     srcPort: 443
}

// PeeringStatus defines the observed state of Peering.
type PeeringStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Peering is the Schema for the peerings API.
type Peering struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PeeringSpec   `json:"spec,omitempty"`
	Status PeeringStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PeeringList contains a list of Peering.
type PeeringList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Peering `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Peering{}, &PeeringList{})
}

func (p *Peering) Default() {
	// TODO add defaulting logic
}

func (p *Peering) Validate(_ context.Context, _ client.Reader) error {
	// TODO add validation logic
	return nil
}
