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
	"go.githedgehog.com/fabricator/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ControlNodeSpec struct {
	Bootstrap  ControlNodeBootstrap  `json:"bootstrap,omitempty"`
	Management ControlNodeManagement `json:"management,omitempty"`
	External   ControlNodeExternal   `json:"external,omitempty"`
}

type ControlNodeBootstrap struct {
	Disk string `json:"disk,omitempty"`
}

type ControlNodeManagement struct {
	IP        meta.Addr `json:"ip,omitempty"`
	Interface string    `json:"interface,omitempty"`
	// TODO support bond
}

type ControlNodeExternal struct {
	IP        meta.AddrOrDHCP `json:"ip,omitempty"`
	Interface string          `json:"interface,omitempty"`
	// TODO support bond
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

func (c *ControlNode) Validate(fabCfg *FabConfig) error {
	if fabCfg == nil {
		return nil
	}

	// TODO make interactive/non-interactive and iso/non-iso validation
	// TODO validate the control node spec

	return nil
}