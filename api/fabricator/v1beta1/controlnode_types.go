// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"
	"regexp"

	"go.githedgehog.com/fabricator/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	MACAddr   string      `json:"mac,omitempty"`
	// TODO support bond
}

type ControlNodeExternal struct {
	IP        meta.PrefixOrDHCP `json:"ip,omitempty"`
	Gateway   meta.Addr         `json:"gateway,omitempty"`
	DNS       []meta.Addr       `json:"dns,omitempty"`
	Interface string            `json:"interface,omitempty"`
	MACAddr   string            `json:"mac,omitempty"`
	// TODO support bond
}

type ControlNodeDummy struct {
	IP meta.Prefix `json:"ip,omitempty"`
}

type ControlNodeStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type ControlNode struct {
	kmetav1.TypeMeta   `json:",inline"`
	kmetav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ControlNodeSpec   `json:"spec,omitempty"`
	Status ControlNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ControlNodeList contains a list of ControlNode
type ControlNodeList struct {
	kmetav1.TypeMeta `json:",inline"`
	kmetav1.ListMeta `json:"metadata,omitempty"`
	Items            []ControlNode `json:"items"`
}

// Accepts colon and hyphen separated mac addresses, no mixed separators, no cisco style.
var macRE = regexp.MustCompile(`^([[:xdigit:]]{2}:){5}[[:xdigit:]]{2}$|^([[:xdigit:]]{2}-){5}[[:xdigit:]]{2}$`)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &ControlNode{}, &ControlNodeList{})

		return nil
	})
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

	if c.Spec.Management.Interface == "" && c.Spec.Management.MACAddr == "" {
		return fmt.Errorf("management interface name or MAC address must be set") //nolint:goerr113
	}
	if c.Spec.Management.MACAddr != "" {
		if !macRE.MatchString(c.Spec.Management.MACAddr) {
			return fmt.Errorf("invalid management mac address: %q", c.Spec.Management.MACAddr) //nolint:goerr113
		}
	}
	if c.Spec.External.MACAddr != "" {
		if !macRE.MatchString(c.Spec.External.MACAddr) {
			return fmt.Errorf("invalid external mac address: %q", c.Spec.External.MACAddr) //nolint:goerr113
		}
	}

	if c.Spec.External.Interface == "" && c.Spec.Management.MACAddr == "" {
		return fmt.Errorf("external interface name or MAC address must be set") //nolint:goerr113
	}

	if c.Spec.Bootstrap.Disk == "" {
		return fmt.Errorf("bootstrap disk must be set") //nolint:goerr113
	}

	return nil
}
