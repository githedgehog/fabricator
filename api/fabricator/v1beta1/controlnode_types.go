// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"

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

func (c *ControlNode) Default() {
}

func (c *ControlNode) Validate(_ context.Context, fabCfg *FabConfig, allowNotHydrated bool) error {
	if fabCfg == nil {
		return fmt.Errorf("fabricator config must be non-nil") //nolint:goerr113
	}

	if c.Namespace != FabNamespace {
		return fmt.Errorf("control node must be in the fabricator namespace %q", FabNamespace) //nolint:goerr113
	}

	if !allowNotHydrated {
		dummyAddr, err := c.Spec.Dummy.IP.Parse()
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

		managementAddr, err := c.Spec.Management.IP.Parse()
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

	if _, _, err := c.Spec.External.IP.Parse(); err != nil {
		return fmt.Errorf("parsing external IP: %w", err)
	}

	if c.Spec.Management.Interface == "" {
		return fmt.Errorf("management interface must be set") //nolint:goerr113
	}

	if c.Spec.External.Interface == "" {
		return fmt.Errorf("external interface must be set") //nolint:goerr113
	}

	return nil
}
