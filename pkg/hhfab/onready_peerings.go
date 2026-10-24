// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

const defaultDomain = "default"

const onReadyGatewayPeeringOpts = ":gw:vpc1=subnet-01:vpc2=subnet-01"

type vpcGroup struct {
	fabric  string
	domain  string
	domains []string
	vpcs    []string
}

func fabricOrDefault(name string) string {
	if name == "" {
		return wiringapi.DefaultFabric
	}

	return name
}

func vpcGroupKey(vpc *vpcapi.VPC) (string, []string) {
	domains := slices.Clone(vpc.Spec.Topology.Domains)
	if len(domains) == 0 {
		domains = []string{defaultDomain}
	}
	slices.Sort(domains)

	return fabricOrDefault(vpc.Spec.Topology.Fabric), domains
}

func externalDomain(ext *vpcapi.External) string {
	if ext.Spec.Topology.Domain == "" {
		return defaultDomain
	}

	return ext.Spec.Topology.Domain
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
// and third VPC of a group in a Fabric that has a Gateway, a VPC peering between the first VPCs of
// groups of a Fabric that share a domain, an External peering from the first VPC of each domain to
// the first External of that domain when the Fabric has several, and an External peering from the
// k-th VPC of each Fabric to the External of its k-th interconnect pair. When the Fabric behind the
// interconnect has a Gateway and an External with a NAT pool, that Fabric also gets a gateway
// peering between its interconnect External and that External, so the VPC reaches it through the
// Gateway.
func buildOnReadyPeerings(vpcs []vpcapi.VPC, externals []vpcapi.External, gateways []gwapi.Gateway,
	namespaces []vpcapi.IPv4Namespace, gatewayEnabled bool,
) onReadyPeeringSet {
	var groups []*vpcGroup
	byKey := map[[2]string]*vpcGroup{}

	sorted := slices.Clone(vpcs)
	slices.SortFunc(sorted, func(a, b vpcapi.VPC) int { return strings.Compare(a.Name, b.Name) })
	for idx := range sorted {
		fabric, domains := vpcGroupKey(&sorted[idx])
		key := [2]string{fabric, strings.Join(domains, ",")}
		group, ok := byKey[key]
		if !ok {
			group = &vpcGroup{fabric: fabric, domain: key[1], domains: domains}
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

	sortedExternals := slices.Clone(externals)
	slices.SortFunc(sortedExternals, func(a, b vpcapi.External) int { return strings.Compare(a.Name, b.Name) })

	// VPCs of different domains of a Fabric can be peered when they share a domain, which is
	// what a VPC on a shared leaf has with the VPCs of each of its domains
	for i, first := range groups {
		for _, second := range groups[i+1:] {
			if first.fabric == second.fabric && slices.ContainsFunc(first.domains, func(domain string) bool { return slices.Contains(second.domains, domain) }) {
				peerings = append(peerings, vpcRequestID(first.vpcs[0])+"+"+vpcRequestID(second.vpcs[0]))
			}
		}
	}

	// a Fabric with several domains has Externals in them, so the first External of each domain is
	// peered with the first VPC that is in that domain
	fabricDomains := map[string]map[string]bool{}
	for _, group := range groups {
		if fabricDomains[group.fabric] == nil {
			fabricDomains[group.fabric] = map[string]bool{}
		}
		for _, domain := range group.domains {
			fabricDomains[group.fabric][domain] = true
		}
	}
	domainExternals := map[[2]string]bool{}
	for idx := range sortedExternals {
		ext := &sortedExternals[idx]
		if _, interconnect := ext.Annotations[VLABInterconnectAnnotation]; interconnect {
			continue
		}

		fabric, domain := fabricOrDefault(ext.Spec.Topology.Fabric), externalDomain(ext)
		if len(fabricDomains[fabric]) < 2 || domainExternals[[2]string{fabric, domain}] {
			continue
		}

		var vpc string
		for _, group := range groups {
			if group.fabric == fabric && slices.Contains(group.domains, domain) {
				vpc = group.vpcs[0]

				break
			}
		}
		if vpc == "" {
			slog.Info("Skipping External peering, no VPC in the External's domain", "external", ext.Name, "fabric", fabric, "domain", domain)

			continue
		}

		domainExternals[[2]string{fabric, domain}] = true
		peerings = append(peerings, fmt.Sprintf("%s~%s:subnets=subnet-01:prefixes=%s", vpcRequestID(vpc), ext.Name, strings.Join(AllZeroPrefix, ",")))
	}

	externalsByName := map[string]*vpcapi.External{}
	for idx := range externals {
		externalsByName[externals[idx].Name] = &externals[idx]
	}

	// the k-th interconnect pair is peered with the k-th VPC of the group on both sides, so VPCs on
	// different pairs have no path to each other
	for idx := range sortedExternals {
		ext := &sortedExternals[idx]
		peerName, ok := ext.Annotations[VLABInterconnectAnnotation]
		if !ok {
			continue
		}

		fabric, domain := fabricOrDefault(ext.Spec.Topology.Fabric), externalDomain(ext)

		var vpc string
		for _, group := range groups {
			if group.fabric == fabric && group.domain == domain {
				if ordinal := interconnectOrdinal(ext.Name); ordinal < len(group.vpcs) {
					vpc = group.vpcs[ordinal]
				}

				break
			}
		}
		if vpc == "" {
			slog.Info("Skipping External peering, no VPC in the External's Fabric and domain for it", "external", ext.Name, "fabric", fabric, "domain", domain)

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

// interconnectOrdinal is the 0-based number of the interconnect pair an External belongs to, from
// the number the generator adds to the names of the second pair on
func interconnectOrdinal(name string) int {
	if _, number, ok := strings.CutLast(name, "-"); ok {
		if k, err := strconv.Atoi(number); err == nil && k > 1 {
			return k - 1
		}
	}

	return 0
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
	for idx := range externals {
		ext := &externals[idx]
		if _, isInterconnect := ext.Annotations[VLABInterconnectAnnotation]; isInterconnect {
			continue
		}
		if fabricOrDefault(ext.Spec.Topology.Fabric) != fabricOrDefault(interconnect.Spec.Topology.Fabric) || externalDomain(ext) != externalDomain(interconnect) {
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

// countInterconnectPairs is the number of interconnect pairs: each has an External on both sides
func countInterconnectPairs(externals []vpcapi.External) int {
	count := 0
	for idx := range externals {
		if _, ok := externals[idx].Annotations[VLABInterconnectAnnotation]; ok {
			count++
		}
	}

	return count / 2
}

// interconnectPairs lists the Externals of the cluster and counts the interconnect pairs
func (c *Config) interconnectPairs(ctx context.Context) (int, error) {
	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return 0, fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	externals := &vpcapi.ExternalList{}
	if err := kube.List(ctx, externals); err != nil {
		return 0, fmt.Errorf("listing externals: %w", err)
	}

	return countInterconnectPairs(externals.Items), nil
}
