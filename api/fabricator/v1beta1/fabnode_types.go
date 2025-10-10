// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/samber/lo"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// FabNodeSpec defines the desired state of FabNode.
type FabNodeSpec struct {
	Roles      []FabNodeRole         `json:"roles"`
	Bootstrap  ControlNodeBootstrap  `json:"bootstrap,omitempty"`
	Management ControlNodeManagement `json:"management,omitempty"`
	External   ControlNodeExternal   `json:"external,omitempty"`
	Dummy      ControlNodeDummy      `json:"dummy,omitempty"`
}

type FabNodeRole string

const (
	NodeRoleGateway       FabNodeRole = "gateway"
	NodeRoleObservability FabNodeRole = "observability"
)

var NodeRoles = []FabNodeRole{
	NodeRoleGateway,
	NodeRoleObservability,
}

// FabNodeStatus defines the observed state of Node.
type FabNodeStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=hedgehog;fabricator,shortName=fn
// +kubebuilder:printcolumn:name="Roles",type=string,JSONPath=`.spec.roles`,priority=0
// +kubebuilder:printcolumn:name="MgmtIP",type=string,JSONPath=`.spec.management.ip`,priority=0
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,priority=0
// FabNode is the Schema for the nodes API.
type FabNode struct {
	kmetav1.TypeMeta   `json:",inline"`
	kmetav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FabNodeSpec   `json:"spec,omitempty"`
	Status FabNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FabNodeList contains a list of Node.
type FabNodeList struct {
	kmetav1.TypeMeta `json:",inline"`
	kmetav1.ListMeta `json:"metadata,omitempty"`
	Items            []FabNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FabNode{}, &FabNodeList{})
}

func (n *FabNode) Default() {
}

func (n *FabNode) Validate(ctx context.Context, fabCfg *FabConfig, allowNotHydrated bool, kube kclient.Reader) error {
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

	// Check if this is an observability node
	isObservability := len(n.Spec.Roles) > 0 && n.Spec.Roles[0] == NodeRoleObservability

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

		if kube != nil {
			controlVIP, err := fabCfg.Control.VIP.Parse()
			if err != nil {
				return fmt.Errorf("parsing control VIP: %w", err)
			}
			controls := &ControlNodeList{}
			if err := kube.List(ctx, controls, kclient.InNamespace(FabNamespace)); err != nil {
				return fmt.Errorf("listing control nodes: %w", err)
			}
			fabnodes := &FabNodeList{}
			if err := kube.List(ctx, fabnodes, kclient.InNamespace(FabNamespace)); err != nil && !kmeta.IsNoMatchError(err) {
				return fmt.Errorf("listing fabricator nodes: %w", err)
			}
			dummyIPs := map[netip.Addr]bool{}
			mgmtIPs := map[netip.Addr]bool{
				controlVIP.Addr(): true,
			}

			for _, c := range controls.Items {
				if ip, err := c.Spec.Dummy.IP.Parse(); err == nil {
					dummyIPs[ip.Addr()] = true
				}
				if ip, err := c.Spec.Management.IP.Parse(); err == nil {
					mgmtIPs[ip.Addr()] = true
				}
			}
			for _, other := range fabnodes.Items {
				if other.Name == n.Name {
					continue
				}
				if ip, err := other.Spec.Dummy.IP.Parse(); err == nil {
					dummyIPs[ip.Addr()] = true
				}
				if ip, err := other.Spec.Management.IP.Parse(); err == nil {
					mgmtIPs[ip.Addr()] = true
				}
			}

			if _, exists := dummyIPs[dummyAddr.Addr()]; exists {
				return fmt.Errorf("dummy IP %s already in use", dummyAddr.String()) //nolint:goerr113
			}
			if _, exists := mgmtIPs[managementAddr.Addr()]; exists {
				return fmt.Errorf("management IP %s already in use", managementAddr.String()) //nolint:goerr113
			}
		}
	}

	// Observability nodes need external interface validation
	if isObservability {
		if _, _, err := n.Spec.External.IP.Parse(); err != nil {
			return fmt.Errorf("parsing external IP: %w", err)
		}

		if n.Spec.External.Interface == "" {
			return fmt.Errorf("external interface must be set for observability node") //nolint:goerr113
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
