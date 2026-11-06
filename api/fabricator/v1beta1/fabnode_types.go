// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/samber/lo"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	fmeta "go.githedgehog.com/fabric/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
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

// DefaultHugePages1G and DefaultHugePages2M are the hugepages reserved on the gateway node unless overridden by the
// gwapi.AnnotationHugePages1G/2M annotations, the same annotations on its Gateway make the fabric controller request
// them for the dataplane, so they must match. The dataplane currently only uses 1G pages.
const (
	DefaultHugePages1G = 16
	DefaultHugePages2M = 0
)

// FabNodeStatus defines the observed state of Node.
type FabNodeStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=hhfr;hhint,shortName=fn
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
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &FabNode{}, &FabNodeList{})

		return nil
	})
}

func (n *FabNode) Default() {
}

func (n *FabNode) Validate(ctx context.Context, fabCfg *FabConfig, allowNotHydrated bool, kube kclient.Reader) error {
	if fabCfg == nil {
		return fmt.Errorf("fabricator config must be non-nil") //nolint:err113
	}

	if n.Namespace != FabNamespace {
		return fmt.Errorf("node must be in the fabricator namespace %q", FabNamespace) //nolint:err113
	}

	if len(lo.Uniq(n.Spec.Roles)) != len(n.Spec.Roles) {
		return fmt.Errorf("duplicate node roles %q", n.Spec.Roles) //nolint:err113
	}

	if !lo.Every(NodeRoles, n.Spec.Roles) {
		return fmt.Errorf("unexpected node roles %q", n.Spec.Roles) //nolint:err113
	}

	if _, _, err := n.HugePages(); err != nil {
		return err
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
			return fmt.Errorf("dummy IP %s not in dummy subnet %s", dummyAddr.String(), dummySubnet.String()) //nolint:err113
		}
		if dummyAddr.Bits() != 31 {
			return fmt.Errorf("dummy IP %s should be /31", dummyAddr.String()) //nolint:err113
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
			return fmt.Errorf("management IP %s not in management subnet %s", managementAddr.String(), managementSubnet.String()) //nolint:err113
		}

		if managementAddr.Bits() != managementSubnet.Bits() {
			return fmt.Errorf("management IP %s not the same subnet as management subnet %s", managementAddr.String(), managementSubnet.String()) //nolint:err113
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
			if err := kube.List(ctx, fabnodes, kclient.InNamespace(FabNamespace)); err != nil {
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
				return fmt.Errorf("dummy IP %s already in use", dummyAddr.String()) //nolint:err113
			}
			if _, exists := mgmtIPs[managementAddr.Addr()]; exists {
				return fmt.Errorf("management IP %s already in use", managementAddr.String()) //nolint:err113
			}
		}
	}

	if n.Spec.Management.Interface == "" {
		return fmt.Errorf("management interface must be set") //nolint:err113
	}

	if n.Spec.Bootstrap.Disk == "" {
		return fmt.Errorf("bootstrap disk must be set") //nolint:err113
	}

	return nil
}

// HugePages returns the 1G and 2M hugepages reserved on the gateway node, from the annotations or the defaults
func (n *FabNode) HugePages() (int, int, error) {
	pages1G, err := hugePages(n.Annotations, gwapi.AnnotationHugePages1G, DefaultHugePages1G, fmeta.MaxGatewayHugePages1G)
	if err != nil {
		return 0, 0, err
	}

	pages2M, err := hugePages(n.Annotations, gwapi.AnnotationHugePages2M, DefaultHugePages2M, fmeta.MaxGatewayHugePages2M)
	if err != nil {
		return 0, 0, err
	}

	// ignition skips kernel args that are already there, so the same hugepages=N would leave the 2M size without count
	if pages1G > 0 && pages1G == pages2M {
		return 0, 0, fmt.Errorf("1G and 2M hugepages can't be the same count %d", pages1G) //nolint:err113
	}

	return pages1G, pages2M, nil
}

func hugePages(anns map[string]string, key string, def, maxPages int) (int, error) {
	val, ok := anns[key]
	if !ok {
		return def, nil
	}

	pages, err := strconv.Atoi(val)
	if err != nil || pages < 0 || pages > maxPages {
		return 0, fmt.Errorf("annotation %s must be between 0 and %d, got %q", key, maxPages, val) //nolint:err113
	}

	return pages, nil
}
