// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"slices"
	"strings"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// IsSubnetReachableOverInterconnect reports whether subnets of VPCs in two different Fabrics reach
// each other over the interconnect: each VPC has an External peering to the interconnect External
// of its own Fabric that permits the subnet and covers the other subnet, and both Externals are
// attached. Without a gateway nothing else carries traffic between Fabrics.
func IsSubnetReachableOverInterconnect(ctx context.Context, kube kclient.Reader, vpc1Name, vpc1Subnet, vpc2Name, vpc2Subnet string) (Reachability, error) {
	vpcs := [2]vpcapi.VPC{}
	for idx, name := range []string{vpc1Name, vpc2Name} {
		if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: name}, &vpcs[idx]); err != nil {
			return Reachability{}, fmt.Errorf("getting VPC %s: %w", name, err)
		}
	}

	fabric1 := fabricOrDefault(vpcs[0].Spec.Topology.Fabric)
	fabric2 := fabricOrDefault(vpcs[1].Spec.Topology.Fabric)
	if fabric1 == fabric2 {
		return Reachability{}, nil
	}

	subnet1, subnet2 := vpcs[0].Spec.Subnets[vpc1Subnet], vpcs[1].Spec.Subnets[vpc2Subnet]
	if subnet1 == nil || subnet2 == nil {
		return Reachability{}, fmt.Errorf("subnet %s of VPC %s or %s of VPC %s not found", vpc1Subnet, vpc1Name, vpc2Subnet, vpc2Name) //nolint:err113
	}

	externals := &vpcapi.ExternalList{}
	if err := kube.List(ctx, externals); err != nil {
		return Reachability{}, fmt.Errorf("listing externals: %w", err)
	}

	for _, ext1 := range externals.Items {
		peerName, ok := ext1.Annotations[VLABInterconnectAnnotation]
		if !ok || fabricOrDefault(ext1.Spec.Topology.Fabric) != fabric1 {
			continue
		}

		for _, ext2 := range externals.Items {
			if ext2.Name != peerName || fabricOrDefault(ext2.Spec.Topology.Fabric) != fabric2 || ext2.Annotations[VLABInterconnectAnnotation] != ext1.Name {
				continue
			}

			peering1, err := externalPeeringCovering(ctx, kube, vpc1Name, vpc1Subnet, ext1.Name, subnet2.Subnet)
			if err != nil {
				return Reachability{}, err
			}
			peering2, err := externalPeeringCovering(ctx, kube, vpc2Name, vpc2Subnet, ext2.Name, subnet1.Subnet)
			if err != nil {
				return Reachability{}, err
			}
			if peering1 == "" || peering2 == "" {
				continue
			}

			return Reachability{
				Reachable: true,
				Reason:    ReachabilityReasonInterconnect,
				Peering:   peering1 + "," + peering2,
			}, nil
		}
	}

	return Reachability{}, nil
}

// externalPeeringCovering is the name of an External peering of the VPC with the External that
// permits the VPC subnet and a prefix covering the destination, when the External is attached
func externalPeeringCovering(ctx context.Context, kube kclient.Reader, vpc, subnet, external, dest string) (string, error) {
	peerings := &vpcapi.ExternalPeeringList{}
	if err := kube.List(ctx, peerings, kclient.InNamespace(kmetav1.NamespaceDefault), kclient.MatchingLabels{vpcapi.LabelVPC: vpc}); err != nil {
		return "", fmt.Errorf("listing external peerings of VPC %s: %w", vpc, err)
	}

	for _, peering := range peerings.Items {
		if peering.Spec.Permit.External.Name != external || !slices.Contains(peering.Spec.Permit.VPC.Subnets, subnet) {
			continue
		}

		for _, prefix := range peering.Spec.Permit.External.Prefixes {
			covers, err := apiutil.SubnetContains(prefix.Prefix, dest)
			if err != nil {
				return "", fmt.Errorf("checking prefix %s of external peering %s: %w", prefix.Prefix, peering.Name, err)
			}
			if !covers {
				continue
			}

			attached, err := apiutil.IsExternalAttached(ctx, kube, external)
			if err != nil {
				return "", fmt.Errorf("checking external %s is attached: %w", external, err)
			}
			if attached {
				return peering.Name, nil
			}
		}
	}

	return "", nil
}

// isExternalSubnetReachableViaPeering is apiutil.IsExternalSubnetReachable without the External
// peerings to interconnect Externals: those carry traffic to another Fabric, and reach anything
// else only through a gateway peering in that Fabric, which has its own check.
func isExternalSubnetReachableViaPeering(ctx context.Context, kube kclient.Reader, sourceServer, destSubnet string) (bool, error) {
	sourceSubnets, err := apiutil.GetAttachedSubnets(ctx, kube, sourceServer)
	if err != nil {
		return false, fmt.Errorf("getting attached subnets for server %s: %w", sourceServer, err)
	}

	for subnetName := range sourceSubnets {
		vpc, subnet, ok := strings.Cut(subnetName, "/")
		if !ok {
			return false, fmt.Errorf("source subnet name must be full VPC subnet name (<vpc-name>/<subnet-name>)") //nolint:err113
		}

		peerings := &vpcapi.ExternalPeeringList{}
		if err := kube.List(ctx, peerings, kclient.InNamespace(kmetav1.NamespaceDefault), kclient.MatchingLabels{vpcapi.LabelVPC: vpc}); err != nil {
			return false, fmt.Errorf("listing external peerings of VPC %s: %w", vpc, err)
		}

		for _, peering := range peerings.Items {
			if !slices.Contains(peering.Spec.Permit.VPC.Subnets, subnet) {
				continue
			}

			ext := &vpcapi.External{}
			if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: peering.Spec.Permit.External.Name}, ext); err != nil {
				return false, fmt.Errorf("getting external %s: %w", peering.Spec.Permit.External.Name, err)
			}
			if _, interconnect := ext.Annotations[VLABInterconnectAnnotation]; interconnect {
				continue
			}

			for _, prefix := range peering.Spec.Permit.External.Prefixes {
				covers, err := apiutil.SubnetContains(prefix.Prefix, destSubnet)
				if err != nil {
					return false, fmt.Errorf("checking prefix %s of external peering %s: %w", prefix.Prefix, peering.Name, err)
				}
				if !covers {
					continue
				}

				attached, err := apiutil.IsExternalAttached(ctx, kube, ext.Name)
				if err != nil {
					return false, fmt.Errorf("checking external %s is attached: %w", ext.Name, err)
				}
				if attached {
					return true, nil
				}
			}
		}
	}

	return false, nil
}
