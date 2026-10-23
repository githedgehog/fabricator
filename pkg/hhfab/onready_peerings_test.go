// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testVPC(name, fabric string, domains ...string) vpcapi.VPC {
	return vpcapi.VPC{
		ObjectMeta: kmetav1.ObjectMeta{Name: name},
		Spec:       vpcapi.VPCSpec{Topology: vpcapi.VPCTopology{Fabric: fabric, Domains: domains}},
	}
}

func testInterconnect(name, peer, fabric, namespace string) vpcapi.External {
	return vpcapi.External{
		ObjectMeta: kmetav1.ObjectMeta{Name: name, Annotations: map[string]string{VLABInterconnectAnnotation: peer}},
		Spec: vpcapi.ExternalSpec{
			Topology:      vpcapi.ExternalTopology{Fabric: fabric},
			IPv4Namespace: namespace,
		},
	}
}

func domainExternal(name, domain string) vpcapi.External {
	return vpcapi.External{
		ObjectMeta: kmetav1.ObjectMeta{Name: name},
		Spec:       vpcapi.ExternalSpec{Topology: vpcapi.ExternalTopology{Domain: domain}},
	}
}

func testGateway(fabric string) gwapi.Gateway {
	return gwapi.Gateway{Spec: gwapi.GatewaySpec{Topology: gwapi.GatewayTopology{Fabric: fabric}}}
}

func TestBuildOnReadyPeerings(t *testing.T) {
	namespaces := []vpcapi.IPv4Namespace{
		{ObjectMeta: kmetav1.ObjectMeta{Name: "default"}, Spec: vpcapi.IPv4NamespaceSpec{Subnets: []string{"10.0.0.0/16"}}},
		{
			ObjectMeta: kmetav1.ObjectMeta{Name: "fabric-b"},
			Spec: vpcapi.IPv4NamespaceSpec{
				Topology: vpcapi.IPv4NamespaceTopology{Fabric: "fabric-b"},
				Subnets:  []string{"10.1.0.0/16"},
			},
		},
	}
	interconnects := []vpcapi.External{
		testInterconnect("to-fabric-b", "to-default", "", "default"),
		testInterconnect("to-default", "to-fabric-b", "fabric-b", "fabric-b"),
	}

	internet := func(annotations map[string]string) vpcapi.External {
		return vpcapi.External{ObjectMeta: kmetav1.ObjectMeta{Name: "ext-bgp-01", Annotations: annotations}}
	}
	withPool := internet(map[string]string{extBGPNATAnnotation: "192.168.91.0/24"})
	transitPeering := map[string]*gwapi.PeeringSpec{
		"to-fabric-b--ext-bgp-01": {Peering: map[string]*gwapi.PeeringEntry{
			"ext.to-fabric-b": {Expose: []gwapi.PeeringEntryExpose{{
				IPs: []gwapi.PeeringEntryIP{{CIDR: "10.1.0.0/16"}},
				As:  []gwapi.PeeringEntryAs{{CIDR: "192.168.91.0/24"}},
				NAT: &gwapi.PeeringNAT{Masquerade: &gwapi.PeeringNATMasquerade{IdleTimeout: kmetav1.Duration{Duration: 5 * time.Minute}}},
			}}},
			"ext.ext-bgp-01": {Expose: []gwapi.PeeringEntryExpose{{DefaultDestination: true}}},
		}},
	}
	twoFabrics := []vpcapi.VPC{testVPC("vpc-01", ""), testVPC("vpc-02", "fabric-b")}

	for _, tt := range []struct {
		name           string
		vpcs           []vpcapi.VPC
		externals      []vpcapi.External
		gateways       []gwapi.Gateway
		gatewayEnabled bool
		want           []string
		wantGateway    map[string]*gwapi.PeeringSpec
	}{
		{
			name: "single fabric and domain",
			vpcs: []vpcapi.VPC{testVPC("vpc-01", ""), testVPC("vpc-02", "default"), testVPC("vpc-03", "")},
			want: []string{"1+2"},
		},
		{
			name:           "single fabric and domain with gateway",
			vpcs:           []vpcapi.VPC{testVPC("vpc-01", ""), testVPC("vpc-02", ""), testVPC("vpc-03", "")},
			gateways:       []gwapi.Gateway{testGateway("")},
			gatewayEnabled: true,
			want:           []string{"1+2", "2+3:gw:vpc1=subnet-01:vpc2=subnet-01"},
		},
		{
			name: "two domains",
			vpcs: []vpcapi.VPC{
				testVPC("vpc-01", "", "default"), testVPC("vpc-02", "", "default"),
				testVPC("vpc-03", "", "domain-b"), testVPC("vpc-04", "", "domain-b"),
			},
			want: []string{"01+02", "03+04"},
		},
		{
			name: "domains that share a domain are peered, shared-leaf VPC included",
			vpcs: []vpcapi.VPC{
				testVPC("vpc-01", "", "domain-b"), testVPC("vpc-02", "", "domain-b"),
				testVPC("vpc-03", "", "default", "domain-b"),
			},
			want: []string{"01+02", "01+03"},
		},
		{
			name: "first External of each domain is peered in a Fabric with several domains",
			vpcs: []vpcapi.VPC{
				testVPC("vpc-01", "", "domain-b"),
				testVPC("vpc-02", "", "default", "domain-b"),
			},
			externals: []vpcapi.External{
				domainExternal("ext-snp-01", "domain-b"), domainExternal("ext-bgp-01", "domain-b"),
			},
			want: []string{"01+02", "01~ext-bgp-01:subnets=subnet-01:prefixes=0.0.0.0/0"},
		},
		{
			name: "Externals of a single-domain Fabric are left alone",
			vpcs: []vpcapi.VPC{testVPC("vpc-01", ""), testVPC("vpc-02", "fabric-b")},
			externals: []vpcapi.External{
				domainExternal("ext-bgp-01", ""),
			},
			want: []string{},
		},
		{
			name: "two fabrics with the interconnect",
			vpcs: []vpcapi.VPC{
				testVPC("vpc-01", ""), testVPC("vpc-02", ""),
				testVPC("vpc-03", "fabric-b"), testVPC("vpc-04", "fabric-b"),
			},
			externals: interconnects,
			want: []string{
				"01+02", "03+04",
				"03~to-default:subnets=subnet-01:prefixes=10.0.0.0/16",
				"01~to-fabric-b:subnets=subnet-01:prefixes=10.1.0.0/16",
			},
		},
		{
			name: "each interconnect pair is peered with its own VPC",
			vpcs: []vpcapi.VPC{
				testVPC("vpc-01", ""), testVPC("vpc-02", ""),
				testVPC("vpc-03", "fabric-b"), testVPC("vpc-04", "fabric-b"),
			},
			externals: append(slices.Clone(interconnects),
				testInterconnect("to-fb-2", "to-def-2", "", "default"),
				testInterconnect("to-def-2", "to-fb-2", "fabric-b", "fabric-b"),
			),
			want: []string{
				"01+02", "03+04",
				"04~to-def-2:subnets=subnet-01:prefixes=10.0.0.0/16",
				"03~to-default:subnets=subnet-01:prefixes=10.0.0.0/16",
				"01~to-fabric-b:subnets=subnet-01:prefixes=10.1.0.0/16",
				"02~to-fb-2:subnets=subnet-01:prefixes=10.1.0.0/16",
			},
		},
		{
			name: "interconnect pair without a VPC of its own is skipped",
			vpcs: []vpcapi.VPC{
				testVPC("vpc-01", ""), testVPC("vpc-02", ""),
				testVPC("vpc-03", "fabric-b"),
			},
			externals: append(slices.Clone(interconnects),
				testInterconnect("to-fb-2", "to-def-2", "", "default"),
				testInterconnect("to-def-2", "to-fb-2", "fabric-b", "fabric-b"),
			),
			want: []string{
				"01+02",
				"03~to-default:subnets=subnet-01:prefixes=10.0.0.0/16",
				"01~to-fabric-b:subnets=subnet-01:prefixes=10.1.0.0/16",
				"02~to-fb-2:subnets=subnet-01:prefixes=10.1.0.0/16",
			},
		},
		{
			name: "gateway peering only in the fabric that has a gateway",
			vpcs: []vpcapi.VPC{
				testVPC("vpc-01", ""), testVPC("vpc-02", ""), testVPC("vpc-03", ""),
				testVPC("vpc-04", "fabric-b"), testVPC("vpc-05", "fabric-b"), testVPC("vpc-06", "fabric-b"),
			},
			gateways: []gwapi.Gateway{testGateway("default")},
			want:     []string{"01+02", "02+03:gw:vpc1=subnet-01:vpc2=subnet-01", "04+05"},
		},
		{
			name:      "transit through the Fabric that has a gateway",
			vpcs:      twoFabrics,
			externals: append(slices.Clone(interconnects), withPool),
			gateways:  []gwapi.Gateway{testGateway("")},
			want: []string{
				"02~to-default:subnets=subnet-01:prefixes=10.0.0.0/16,0.0.0.0/0",
				"01~to-fabric-b:subnets=subnet-01:prefixes=10.1.0.0/16",
			},
			wantGateway: transitPeering,
		},
		{
			name:      "no transit without a gateway",
			vpcs:      twoFabrics,
			externals: append(slices.Clone(interconnects), withPool),
			want: []string{
				"02~to-default:subnets=subnet-01:prefixes=10.0.0.0/16",
				"01~to-fabric-b:subnets=subnet-01:prefixes=10.1.0.0/16",
			},
		},
		{
			name:      "no transit without a NAT pool",
			vpcs:      twoFabrics,
			externals: append(slices.Clone(interconnects), internet(nil)),
			gateways:  []gwapi.Gateway{testGateway("")},
			want: []string{
				"02~to-default:subnets=subnet-01:prefixes=10.0.0.0/16",
				"01~to-fabric-b:subnets=subnet-01:prefixes=10.1.0.0/16",
			},
		},
		{
			name:      "group with fewer than two VPCs gets no VPC peering",
			vpcs:      []vpcapi.VPC{testVPC("vpc-01", ""), testVPC("vpc-02", ""), testVPC("vpc-03", "fabric-b")},
			externals: interconnects,
			want: []string{
				"01+02",
				"03~to-default:subnets=subnet-01:prefixes=10.0.0.0/16",
				"01~to-fabric-b:subnets=subnet-01:prefixes=10.1.0.0/16",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := buildOnReadyPeerings(tt.vpcs, tt.externals, tt.gateways, namespaces, tt.gatewayEnabled)
			require.Equal(t, tt.want, got.Requests)
			if tt.wantGateway == nil {
				require.Empty(t, got.Gateway)
			} else {
				require.Equal(t, tt.wantGateway, got.Gateway)
			}
		})
	}
}

func TestBuildExternalEndpointsSkipsInterconnect(t *testing.T) {
	externals := []vpcapi.External{
		testInterconnect("to-fabric-b", "to-default", "", "default"),
		{ObjectMeta: kmetav1.ObjectMeta{Name: "ext-bgp"}},
		{ObjectMeta: kmetav1.ObjectMeta{Name: "ext-static"}},
	}

	var names []string
	for _, ep := range buildExternalEndpoints(externals) {
		names = append(names, ep.External.ExternalName)
	}
	require.Equal(t, []string{"ext-bgp", "ext-static"}, names)
}

func TestExternalPeerableWithVPC(t *testing.T) {
	ext := func(fabric, domain string) *vpcapi.External {
		return &vpcapi.External{Spec: vpcapi.ExternalSpec{Topology: vpcapi.ExternalTopology{Fabric: fabric, Domain: domain}}}
	}
	vpc := func(fabric string, domains ...string) *vpcapi.VPC {
		v := testVPC("vpc", fabric, domains...)

		return &v
	}
	interconnect := testInterconnect("to-fabric-b", "to-default", "", "default")

	for _, tt := range []struct {
		name string
		ext  *vpcapi.External
		vpc  *vpcapi.VPC
		want bool
	}{
		{name: "defaults match", ext: ext("", ""), vpc: vpc(""), want: true},
		{name: "same domain", ext: ext("", "domain-b"), vpc: vpc("", "domain-b"), want: true},
		{name: "one of the VPC domains", ext: ext("", "domain-b"), vpc: vpc("", "default", "domain-b"), want: true},
		{name: "other domain", ext: ext("", "domain-b"), vpc: vpc("", "default"), want: false},
		{name: "external in domain, vpc default", ext: ext("", "domain-b"), vpc: vpc(""), want: false},
		{name: "other fabric", ext: ext("fabric-b", ""), vpc: vpc(""), want: false},
		{name: "interconnect", ext: &interconnect, vpc: vpc(""), want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, externalPeerableWithVPC(tt.ext, tt.vpc))
		})
	}
}
