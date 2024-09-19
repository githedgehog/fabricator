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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ControlNodeSpec struct {
	TargetDevice string `json:"targetDevice,omitempty"` // TODO need to support some soft raid?

	MgmtIface string `json:"mgmtIface,omitempty"` // TODO need to support bond?
	MgmtIP    string `json:"mgmtIP,omitempty"`

	ExtIface string `json:"extIface,omitempty"` // TODO need to support bond?
	ExtIP    string `json:"extIP,omitempty"`    // TODO accept DHCP as well, installer should check the ip on the interface and add to the tls-san
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
