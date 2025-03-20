// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// FabNodeSpec defines the desired state of FabNode.
type FabNodeSpec struct {
	Roles      []FabNodeRole         `json:"roles"`
	Bootstrap  ControlNodeBootstrap  `json:"bootstrap,omitempty"`
	Management ControlNodeManagement `json:"management,omitempty"`
	Dummy      ControlNodeDummy      `json:"dummy,omitempty"`
}

type FabNodeRole string

const (
	NodeRoleGateway FabNodeRole = "gateway"
)

var NodeRoles = []FabNodeRole{
	NodeRoleGateway,
}

// FabNodeStatus defines the observed state of Node.
type FabNodeStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// FabNode is the Schema for the nodes API.
type FabNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FabNodeSpec   `json:"spec,omitempty"`
	Status FabNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FabNodeList contains a list of Node.
type FabNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FabNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FabNode{}, &FabNodeList{})
}

func (n *FabNode) Default() {
}

func (n *FabNode) Validate(_ context.Context, fabCfg *FabConfig, allowNotHydrated bool) error {
	if fabCfg == nil {
		return fmt.Errorf("fabricator config must be non-nil") //nolint:goerr113
	}

	if n.Namespace != FabNamespace {
		return fmt.Errorf("node must be in the fabricator namespace %q", FabNamespace) //nolint:goerr113
	}

	if len(lo.Uniq(n.Spec.Roles)) != len(n.Spec.Roles) {
		return fmt.Errorf("duplicate node roles %q", n.Spec.Roles) //nolint:goerr113
	}

	if !lo.Every(NodeRoles, n.Spec.Roles) {
		return fmt.Errorf("unexpected node roles %q", n.Spec.Roles) //nolint:goerr113
	}

	if !allowNotHydrated {
		dummyAddr, err := n.Spec.Dummy.IP.Parse()
		if err != nil {
			return fmt.Errorf("parsing dummy IP: %w", err)
		}

		dummySubnet, err := fabCfg.Control.DummySubnet.Parse()
		if err != nil {
			return fmt.Errorf("parsing dummy subnet: %w", err)
		}

		if !dummySubnet.Contains(dummyAddr.Addr()) {
			return fmt.Errorf("dummy IP %s not in dummy subnet %s", dummyAddr.String(), dummySubnet.String()) //nolint:goerr113
		}
		if dummyAddr.Bits() != 31 {
			return fmt.Errorf("dummy IP %s should be /31", dummyAddr.String()) //nolint:goerr113
		}

		managementAddr, err := n.Spec.Management.IP.Parse()
		if err != nil {
			return fmt.Errorf("parsing management IP: %w", err)
		}

		managementSubnet, err := fabCfg.Control.ManagementSubnet.Parse()
		if err != nil {
			return fmt.Errorf("parsing management subnet: %w", err)
		}

		if !managementSubnet.Contains(managementAddr.Addr()) {
			return fmt.Errorf("management IP %s not in management subnet %s", managementAddr.String(), managementSubnet.String()) //nolint:goerr113
		}

		if managementAddr.Bits() != managementSubnet.Bits() {
			return fmt.Errorf("management IP %s not the same subnet as management subnet %s", managementAddr.String(), managementSubnet.String()) //nolint:goerr113
		}
	}

	if n.Spec.Management.Interface == "" {
		return fmt.Errorf("management interface must be set") //nolint:goerr113
	}

	if n.Spec.Bootstrap.Disk == "" {
		return fmt.Errorf("bootstrap disk must be set") //nolint:goerr113
	}

	return nil
}
