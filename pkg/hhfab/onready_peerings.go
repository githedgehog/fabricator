// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

const defaultDomain = "default"

const onReadyGatewayPeeringOpts = ":gw:vpc1=subnet-01:vpc2=subnet-01"

type vpcGroup struct {
	fabric string
	domain string
	vpcs   []string
}

func fabricOrDefault(name string) string {
	if name == "" {
		return wiringapi.DefaultFabric
	}

	return name
}

func vpcGroupKey(vpc *vpcapi.VPC) (string, string) {
	domains := slices.Clone(vpc.Spec.Topology.Domains)
	if len(domains) == 0 {
		domains = []string{defaultDomain}
	}
	slices.Sort(domains)

	return fabricOrDefault(vpc.Spec.Topology.Fabric), strings.Join(domains, ",")
}

func vpcRequestID(name string) string {
	return strings.TrimPrefix(name, "vpc-")
}

// onReadyPeeringSet is what the on-ready setup-peerings step creates: requests in the syntax of
// setup-peerings, and gateway peerings that the syntax can not express (two Externals)
type onReadyPeeringSet struct {
	Requests []string
	Gateway  map[string]*gwapi.PeeringSpec
}

// buildOnReadyPeerings returns the peerings for the on-ready step. A single Fabric and domain keep
// the fixed requests, with several of them the peerings are built from the cluster: a VPC peering
// between the first two VPCs of each Fabric and domain group, a gateway peering between the second
// and third VPC of a group in a Fabric that has a Gateway, and an External peering from the first
// VPC of each Fabric to its interconnect External. When the Fabric behind the interconnect has a
// Gateway and an External with a NAT pool, that Fabric also gets a gateway peering between its
// interconnect External and that External, so the VPC reaches it through the Gateway.
func buildOnReadyPeerings(vpcs []vpcapi.VPC, externals []vpcapi.External, gateways []gwapi.Gateway,
	namespaces []vpcapi.IPv4Namespace, gatewayEnabled bool,
) onReadyPeeringSet {
	var groups []*vpcGroup
	byKey := map[[2]string]*vpcGroup{}

	sorted := slices.Clone(vpcs)
	slices.SortFunc(sorted, func(a, b vpcapi.VPC) int { return strings.Compare(a.Name, b.Name) })
	for idx := range sorted {
		fabric, domain := vpcGroupKey(&sorted[idx])
		key := [2]string{fabric, domain}
		group, ok := byKey[key]
		if !ok {
			group = &vpcGroup{fabric: fabric, domain: domain}
			byKey[key] = group
			groups = append(groups, group)
		}
		group.vpcs = append(group.vpcs, sorted[idx].Name)
	}

	if len(groups) <= 1 {
		peerings := []string{"1+2"}
		if gatewayEnabled {
			peerings = append(peerings, "2+3"+onReadyGatewayPeeringOpts)
		}

		return onReadyPeeringSet{Requests: peerings}
	}

	gatewayFabrics := map[string]bool{}
	for _, gw := range gateways {
		gatewayFabrics[fabricOrDefault(gw.Spec.Topology.Fabric)] = true
	}

	peerings := make([]string, 0, 2*len(groups)+len(externals))
	gatewayPeerings := map[string]*gwapi.PeeringSpec{}
	for _, group := range groups {
		if len(group.vpcs) < 2 {
			slog.Info("Skipping VPC peering, group has fewer than two VPCs", "fabric", group.fabric, "domains", group.domain, "vpcs", group.vpcs)
		} else {
			peerings = append(peerings, vpcRequestID(group.vpcs[0])+"+"+vpcRequestID(group.vpcs[1]))
		}

		if !gatewayFabrics[group.fabric] {
			continue
		}
		if len(group.vpcs) < 3 {
			slog.Info("Skipping gateway peering, group has fewer than three VPCs", "fabric", group.fabric, "domains", group.domain, "vpcs", group.vpcs)

			continue
		}
		peerings = append(peerings, vpcRequestID(group.vpcs[1])+"+"+vpcRequestID(group.vpcs[2])+onReadyGatewayPeeringOpts)
	}

	externalsByName := map[string]*vpcapi.External{}
	for idx := range externals {
		externalsByName[externals[idx].Name] = &externals[idx]
	}

	sortedExternals := slices.Clone(externals)
	slices.SortFunc(sortedExternals, func(a, b vpcapi.External) int { return strings.Compare(a.Name, b.Name) })
	for idx := range sortedExternals {
		ext := &sortedExternals[idx]
		peerName, ok := ext.Annotations[VLABInterconnectAnnotation]
		if !ok {
			continue
		}

		fabric := fabricOrDefault(ext.Spec.Topology.Fabric)
		domain := ext.Spec.Topology.Domain
		if domain == "" {
			domain = defaultDomain
		}

		var vpc string
		for _, group := range groups {
			if group.fabric == fabric && group.domain == domain {
				vpc = group.vpcs[0]

				break
			}
		}
		if vpc == "" {
			slog.Info("Skipping External peering, no VPC in the External's Fabric and domain", "external", ext.Name, "fabric", fabric, "domain", domain)

			continue
		}

		// the External of the other Fabric names the address space reachable through it
		var prefixes []string
		if peer, ok := externalsByName[peerName]; ok {
			prefixes = namespaceSubnets(namespaces, peer)

			if internet, pool := transitExternal(sortedExternals, peer); internet != nil && gatewayFabrics[fabricOrDefault(peer.Spec.Topology.Fabric)] {
				exposes, err := buildExposes(namespaceSubnets(namespaces, ext), []string{pool}, nil, NATModeMasquerade, nil)
				if err != nil {
					slog.Warn("Skipping gateway transit peering", "external", ext.Name, "err", err)
				} else {
					prefixes = append(prefixes, AllZeroPrefix...)
					gatewayPeerings[peer.Name+"--"+internet.Name] = &gwapi.PeeringSpec{
						Peering: map[string]*gwapi.PeeringEntry{
							gwapi.VPCInfoExtPrefix + peer.Name:     {Expose: exposes},
							gwapi.VPCInfoExtPrefix + internet.Name: {Expose: []gwapi.PeeringEntryExpose{{DefaultDestination: true}}},
						},
					}
				}
			}
		}

		req := fmt.Sprintf("%s~%s:subnets=subnet-01", vpcRequestID(vpc), ext.Name)
		if len(prefixes) > 0 {
			req += ":prefixes=" + strings.Join(prefixes, ",")
		}
		peerings = append(peerings, req)
	}

	return onReadyPeeringSet{Requests: peerings, Gateway: gatewayPeerings}
}

// namespaceSubnets are the subnets of the IPv4Namespace of the External, in the External's Fabric
func namespaceSubnets(namespaces []vpcapi.IPv4Namespace, ext *vpcapi.External) []string {
	var subnets []string
	fabric := fabricOrDefault(ext.Spec.Topology.Fabric)
	for _, ns := range namespaces {
		if ns.Name == ext.Spec.IPv4Namespace && fabricOrDefault(ns.Spec.Topology.Fabric) == fabric {
			subnets = append(subnets, ns.Spec.Subnets...)
		}
	}

	return subnets
}

// transitExternal is the first External of the interconnect External's Fabric and domain that
// has a BGP NAT pool and is not an interconnect External, with its pool. It is what the Fabric's
// Gateway reaches for the VPCs behind the interconnect.
func transitExternal(externals []vpcapi.External, interconnect *vpcapi.External) (*vpcapi.External, string) {
	domain := func(ext *vpcapi.External) string {
		if ext.Spec.Topology.Domain == "" {
			return defaultDomain
		}

		return ext.Spec.Topology.Domain
	}

	for idx := range externals {
		ext := &externals[idx]
		if _, isInterconnect := ext.Annotations[VLABInterconnectAnnotation]; isInterconnect {
			continue
		}
		if fabricOrDefault(ext.Spec.Topology.Fabric) != fabricOrDefault(interconnect.Spec.Topology.Fabric) || domain(ext) != domain(interconnect) {
			continue
		}
		if pool := ext.Annotations[extBGPNATAnnotation]; pool != "" {
			return ext, pool
		}
	}

	return nil, ""
}

// onReadyPeerings lists the cluster objects and builds the on-ready peerings from them
func (c *Config) onReadyPeerings(ctx context.Context) (onReadyPeeringSet, error) {
	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return onReadyPeeringSet{}, fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return onReadyPeeringSet{}, fmt.Errorf("listing VPCs: %w", err)
	}
	externals := &vpcapi.ExternalList{}
	if err := kube.List(ctx, externals); err != nil {
		return onReadyPeeringSet{}, fmt.Errorf("listing externals: %w", err)
	}
	gateways := &gwapi.GatewayList{}
	if err := kube.List(ctx, gateways); err != nil {
		return onReadyPeeringSet{}, fmt.Errorf("listing gateways: %w", err)
	}
	namespaces := &vpcapi.IPv4NamespaceList{}
	if err := kube.List(ctx, namespaces); err != nil {
		return onReadyPeeringSet{}, fmt.Errorf("listing IPv4 namespaces: %w", err)
	}

	return buildOnReadyPeerings(vpcs.Items, externals.Items, gateways.Items, namespaces.Items, c.Fab.Spec.Config.Gateway.Enable), nil
}
