// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"net/netip"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func icVPC(name, fabric, subnet string) *vpcapi.VPC {
	return &vpcapi.VPC{
		Name: name, Namespace: kmetav1.NamespaceDefault,
		Spec: vpcapi.VPCSpec{
			Topology: vpcapi.VPCTopology{Fabric: fabric},
			Subnets:  map[string]*vpcapi.VPCSubnet{"subnet-01": {Subnet: subnet}},
		},
	}
}

func icExternalPeering(name, vpc, external, prefix string) *vpcapi.ExternalPeering {
	return &vpcapi.ExternalPeering{
		Name: name, Namespace: kmetav1.NamespaceDefault, Labels: map[string]string{vpcapi.LabelVPC: vpc},
		Spec: vpcapi.ExternalPeeringSpec{Permit: vpcapi.ExternalPeeringSpecPermit{
			VPC:      vpcapi.ExternalPeeringSpecVPC{Name: vpc, Subnets: []string{"subnet-01"}},
			External: vpcapi.ExternalPeeringSpecExternal{Name: external, Prefixes: []vpcapi.ExternalPeeringSpecPrefix{{Prefix: prefix}}},
		}},
	}
}

func icAttachment(external string) *vpcapi.ExternalAttachment {
	return &vpcapi.ExternalAttachment{
		Name: "leaf--" + external, Namespace: kmetav1.NamespaceDefault,
		Labels: map[string]string{vpcapi.LabelExternal: external},
	}
}

func TestIsSubnetReachableOverInterconnect(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, vpcapi.AddToScheme(scheme))

	toB := testInterconnect("to-fabric-b", "to-default", "", "default")
	toA := testInterconnect("to-default", "to-fabric-b", "fabric-b", "fabric-b")
	base := func() []kclient.Object {
		return []kclient.Object{
			icVPC("vpc-01", "", "10.0.1.0/24"), icVPC("vpc-02", "", "10.0.2.0/24"), icVPC("vpc-03", "fabric-b", "10.1.1.0/24"),
			&toB, &toA, icAttachment("to-fabric-b"), icAttachment("to-default"),
			icExternalPeering("a", "vpc-01", "to-fabric-b", "10.1.0.0/16"),
			icExternalPeering("b", "vpc-03", "to-default", "10.0.0.0/16"),
		}
	}

	for _, tt := range []struct {
		name     string
		objs     func() []kclient.Object
		src, dst string
		want     bool
	}{
		{name: "both sides peered", objs: base, src: "vpc-01", dst: "vpc-03", want: true},
		{name: "reverse direction", objs: base, src: "vpc-03", dst: "vpc-01", want: true},
		{name: "same fabric", objs: base, src: "vpc-01", dst: "vpc-02", want: false},
		{name: "other VPC has no peering", objs: base, src: "vpc-02", dst: "vpc-03", want: false},
		{
			name: "prefix does not cover the other subnet",
			objs: func() []kclient.Object {
				return append(base(), icExternalPeering("c", "vpc-02", "to-fabric-b", "10.9.0.0/16"))
			},
			src: "vpc-02", dst: "vpc-03", want: false,
		},
		{
			name: "external not attached",
			objs: func() []kclient.Object {
				return append(base()[:5], base()[7:]...)
			},
			src: "vpc-01", dst: "vpc-03", want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objs()...).Build()
			r, err := IsSubnetReachableOverInterconnect(context.Background(), kube, tt.src, "subnet-01", tt.dst, "subnet-01")
			require.NoError(t, err)
			require.Equal(t, tt.want, r.Reachable)
			if tt.want {
				require.Equal(t, ReachabilityReasonInterconnect, r.Reason)
			}
		})
	}
}

func TestIsSubnetReachableOverInterconnectPairs(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, vpcapi.AddToScheme(scheme))

	toB, toA := testInterconnect("to-fabric-b", "to-default", "", "default"), testInterconnect("to-default", "to-fabric-b", "fabric-b", "fabric-b")
	toB2, toA2 := testInterconnect("to-fb-2", "to-def-2", "", "default"), testInterconnect("to-def-2", "to-fb-2", "fabric-b", "fabric-b")
	objs := []kclient.Object{
		icVPC("vpc-01", "", "10.0.1.0/24"), icVPC("vpc-02", "", "10.0.2.0/24"),
		icVPC("vpc-03", "fabric-b", "10.1.1.0/24"), icVPC("vpc-04", "fabric-b", "10.1.2.0/24"),
		&toB, &toA, &toB2, &toA2,
		icAttachment("to-fabric-b"), icAttachment("to-default"), icAttachment("to-fb-2"), icAttachment("to-def-2"),
		icExternalPeering("a", "vpc-01", "to-fabric-b", "10.1.0.0/16"),
		icExternalPeering("b", "vpc-03", "to-default", "10.0.0.0/16"),
		icExternalPeering("c", "vpc-02", "to-fb-2", "10.1.0.0/16"),
		icExternalPeering("d", "vpc-04", "to-def-2", "10.0.0.0/16"),
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	// VPCs of the same pair reach each other, VPCs on separate pairs do not
	for _, tt := range []struct {
		src, dst string
		want     bool
	}{
		{"vpc-01", "vpc-03", true},
		{"vpc-02", "vpc-04", true},
		{"vpc-01", "vpc-04", false},
		{"vpc-02", "vpc-03", false},
	} {
		r, err := IsSubnetReachableOverInterconnect(context.Background(), kube, tt.src, "subnet-01", tt.dst, "subnet-01")
		require.NoError(t, err)
		require.Equal(t, tt.want, r.Reachable, "%s to %s", tt.src, tt.dst)
	}
}

// icServer is a server attached to the subnet of a VPC over an unbundled connection
func icServer(name, switchName, vpc string) []kclient.Object {
	conn := seUnbundledConn(name, switchName)
	conn.Labels = map[string]string(wiringapi.MatchingLabelsForListLabelServer(name))

	return []kclient.Object{
		&wiringapi.Server{Name: name, Namespace: kmetav1.NamespaceDefault},
		conn,
		&vpcapi.VPCAttachment{
			Name: conn.Name + "--" + vpc, Namespace: kmetav1.NamespaceDefault,
			Labels: map[string]string{wiringapi.LabelConnection: conn.Name, wiringapi.LabelVPC: vpc},
			Spec:   vpcapi.VPCAttachmentSpec{Subnet: vpc + "/subnet-01", Connection: conn.Name},
		},
	}
}

func TestExternalReachableViaPeeringIgnoresInterconnect(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, vpcapi.AddToScheme(scheme))
	require.NoError(t, wiringapi.AddToScheme(scheme))

	toB := testInterconnect("to-fabric-b", "to-default", "", "default")
	toA := testInterconnect("to-default", "to-fabric-b", "fabric-b", "fabric-b")
	internet := vpcapi.External{Name: "ext-bgp-01", Namespace: kmetav1.NamespaceDefault}
	toA.Namespace, toB.Namespace = kmetav1.NamespaceDefault, kmetav1.NamespaceDefault

	objs := slices.Concat([]kclient.Object{
		icVPC("vpc-01", "", "10.0.1.0/24"), icVPC("vpc-02", "fabric-b", "10.1.1.0/24"),
		&toB, &toA, &internet,
		icAttachment("to-fabric-b"), icAttachment("to-default"), icAttachment("ext-bgp-01"),
		icExternalPeering("a", "vpc-01", "ext-bgp-01", "0.0.0.0/0"),
		icExternalPeering("b", "vpc-02", "to-default", "0.0.0.0/0"),
	}, icServer("server-01", "leaf-01", "vpc-01"), icServer("server-02", "leaf-03", "vpc-02"))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	reachable, err := isExternalSubnetReachableViaPeering(context.Background(), kube, "server-01", "0.0.0.0/0")
	require.NoError(t, err)
	require.True(t, reachable, "peering to a real External")

	reachable, err = isExternalSubnetReachableViaPeering(context.Background(), kube, "server-02", "0.0.0.0/0")
	require.NoError(t, err)
	require.False(t, reachable, "peering to the interconnect External only")
}

func icTransitPeering(transitCIDR string) *gwapi.GatewayPeering {
	return &gwapi.GatewayPeering{
		Name: "to-fabric-b--ext-bgp-01", Namespace: kmetav1.NamespaceDefault,
		Spec: gwapi.PeeringSpec{
			Topology: gwapi.GatewayPeeringTopology{Fabric: "default"},
			Peering: map[string]*gwapi.PeeringEntry{
				"ext.to-fabric-b": {Expose: []gwapi.PeeringEntryExpose{{IPs: []gwapi.PeeringEntryIP{{CIDR: transitCIDR}}}}},
				"ext.ext-bgp-01":  {Expose: []gwapi.PeeringEntryExpose{{DefaultDestination: true}}},
			},
		},
	}
}

func TestIsExternalReachableOverInterconnect(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, vpcapi.AddToScheme(scheme))
	require.NoError(t, wiringapi.AddToScheme(scheme))
	require.NoError(t, gwapi.AddToScheme(scheme))

	toB := testInterconnect("to-fabric-b", "to-default", "", "default")
	toA := testInterconnect("to-default", "to-fabric-b", "fabric-b", "fabric-b")
	toA.Namespace, toB.Namespace = kmetav1.NamespaceDefault, kmetav1.NamespaceDefault
	internet := vpcapi.External{Name: "ext-bgp-01", Namespace: kmetav1.NamespaceDefault}

	base := func(vpcPrefix string, gwPeering *gwapi.GatewayPeering) []kclient.Object {
		objs := []kclient.Object{
			icVPC("vpc-01", "", "10.0.1.0/24"), icVPC("vpc-02", "fabric-b", "10.1.1.0/24"),
			&toB, &toA, &internet,
			icAttachment("to-fabric-b"), icAttachment("to-default"), icAttachment("ext-bgp-01"),
			icExternalPeering("b", "vpc-02", "to-default", vpcPrefix),
		}
		objs = append(objs, icServer("server-01", "leaf-01", "vpc-01")...)
		objs = append(objs, icServer("server-02", "leaf-03", "vpc-02")...)
		if gwPeering != nil {
			objs = append(objs, gwPeering)
		}

		return objs
	}

	notPeering := icTransitPeering("10.1.0.0/16")
	notPeering.Spec.Peering["ext.to-fabric-b"].Expose[0].IPs = append(notPeering.Spec.Peering["ext.to-fabric-b"].Expose[0].IPs, gwapi.PeeringEntryIP{Not: "10.1.1.128/25"})

	for _, tt := range []struct {
		name         string
		objs         []kclient.Object
		source       string
		want         bool
		wantExcluded []netip.Prefix
	}{
		{name: "through the gateway of the other Fabric", objs: base("0.0.0.0/0", icTransitPeering("10.1.0.0/16")), source: "server-02", want: true},
		{name: "no gateway peering", objs: base("0.0.0.0/0", nil), source: "server-02", want: false},
		{name: "gateway peering does not expose the VPC subnet", objs: base("0.0.0.0/0", icTransitPeering("10.2.0.0/16")), source: "server-02", want: false},
		{name: "VPC peering does not cover the prefix", objs: base("10.0.0.0/16", icTransitPeering("10.1.0.0/16")), source: "server-02", want: false},
		{name: "server in the Fabric of the External", objs: base("0.0.0.0/0", icTransitPeering("10.1.0.0/16")), source: "server-01", want: false},
		{name: "External not attached", objs: slices.DeleteFunc(base("0.0.0.0/0", icTransitPeering("10.1.0.0/16")), func(obj kclient.Object) bool {
			return obj.GetName() == "leaf--ext-bgp-01"
		}), source: "server-02", want: false},
		{name: "gateway peering takes part of the VPC subnet out", objs: base("0.0.0.0/0", notPeering), source: "server-02", want: true, wantExcluded: []netip.Prefix{netip.MustParsePrefix("10.1.1.128/25")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objs...).Build()
			r, err := IsExternalReachableOverInterconnect(context.Background(), kube, tt.source, "ext-bgp-01", []string{"0.0.0.0/0"})
			require.NoError(t, err)
			require.Equal(t, tt.want, r.Reachable)
			if tt.want {
				require.Equal(t, ReachabilityReasonInterconnectGateway, r.Reason)
				require.Equal(t, tt.wantExcluded, r.SourceExclusions)
			}
		})
	}
}

// the gateway peering the on-ready step builds is the one the matrix accepts as transit
func TestOnReadyTransitPeeringIsReachable(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, vpcapi.AddToScheme(scheme))
	require.NoError(t, wiringapi.AddToScheme(scheme))
	require.NoError(t, gwapi.AddToScheme(scheme))

	toB := testInterconnect("to-fabric-b", "to-default", "", "default")
	toA := testInterconnect("to-default", "to-fabric-b", "fabric-b", "fabric-b")
	internet := vpcapi.External{
		Name: "ext-bgp-01", Annotations: map[string]string{extBGPNATAnnotation: "192.168.91.0/24"},
	}
	namespaces := []vpcapi.IPv4Namespace{
		{Name: "default", Spec: vpcapi.IPv4NamespaceSpec{Subnets: []string{"10.0.0.0/16"}}},
		{
			Name: "fabric-b",
			Spec: vpcapi.IPv4NamespaceSpec{
				Topology: vpcapi.IPv4NamespaceTopology{Fabric: "fabric-b"},
				Subnets:  []string{"10.1.0.0/16"},
			},
		},
	}

	set := buildOnReadyPeerings(
		[]vpcapi.VPC{testVPC("vpc-01", ""), testVPC("vpc-02", "fabric-b")},
		[]vpcapi.External{toB, toA, internet}, []gwapi.Gateway{testGateway("")}, namespaces, true,
	)
	require.Len(t, set.Gateway, 1)

	toA.Namespace, toB.Namespace, internet.Namespace = kmetav1.NamespaceDefault, kmetav1.NamespaceDefault, kmetav1.NamespaceDefault
	objs := []kclient.Object{
		icVPC("vpc-01", "", "10.0.1.0/24"), icVPC("vpc-02", "fabric-b", "10.1.1.0/24"),
		&toB, &toA, &internet,
		icAttachment("to-fabric-b"), icAttachment("to-default"), icAttachment("ext-bgp-01"),
		icExternalPeering("b", "vpc-02", "to-default", "10.0.0.0/16"),
	}
	objs = slices.Grow(objs, len(set.Gateway)+2)
	for name, spec := range set.Gateway {
		spec.Topology.Fabric = "default"
		objs = append(objs, &gwapi.GatewayPeering{Name: name, Namespace: kmetav1.NamespaceDefault, Spec: *spec})
	}
	// the VPC prefixes the builder asked for, as the External peering would carry them
	objs[len(objs)-len(set.Gateway)-1] = icExternalPeering("b", "vpc-02", "to-default", "0.0.0.0/0")
	objs = append(objs, icServer("server-02", "leaf-03", "vpc-02")...)

	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r, err := IsExternalReachableOverInterconnect(context.Background(), kube, "server-02", "ext-bgp-01", []string{"0.0.0.0/0"})
	require.NoError(t, err)
	require.True(t, r.Reachable)
}

// test-connectivity decides what a server must reach outside with IsExternalSubnetReachable, so it
// has to see the transit through the gateway of the other Fabric too
func TestIsExternalSubnetReachableSeesTransit(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, vpcapi.AddToScheme(scheme))
	require.NoError(t, wiringapi.AddToScheme(scheme))
	require.NoError(t, gwapi.AddToScheme(scheme))

	toB := testInterconnect("to-fabric-b", "to-default", "", "default")
	toA := testInterconnect("to-default", "to-fabric-b", "fabric-b", "fabric-b")
	toA.Namespace, toB.Namespace = kmetav1.NamespaceDefault, kmetav1.NamespaceDefault
	internet := vpcapi.External{Name: "ext-bgp-01", Namespace: kmetav1.NamespaceDefault}

	vpcInfo := func(name string, subnets map[string]*gwapi.VPCInfoSubnet) *gwapi.VPCInfo {
		return &gwapi.VPCInfo{
			Name: name, Namespace: kmetav1.NamespaceDefault,
			Spec: gwapi.VPCInfoSpec{Subnets: subnets},
		}
	}

	for _, tt := range []struct {
		name      string
		gwPeering *gwapi.GatewayPeering
		want      bool
	}{
		{name: "with the gateway peering", gwPeering: icTransitPeering("10.1.0.0/16"), want: true},
		{name: "without the gateway peering", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objs := []kclient.Object{
				icVPC("vpc-01", "", "10.0.1.0/24"), icVPC("vpc-02", "fabric-b", "10.1.1.0/24"),
				&toB, &toA, &internet,
				icAttachment("to-fabric-b"), icAttachment("to-default"), icAttachment("ext-bgp-01"),
				icExternalPeering("b", "vpc-02", "to-default", "0.0.0.0/0"),
				vpcInfo("vpc-02", map[string]*gwapi.VPCInfoSubnet{"subnet-01": {CIDR: "10.1.1.0/24"}}),
				vpcInfo("ext.to-default", map[string]*gwapi.VPCInfoSubnet{"external": {CIDR: "0.0.0.0/0"}}),
				vpcInfo("ext.to-fabric-b", map[string]*gwapi.VPCInfoSubnet{"external": {CIDR: "0.0.0.0/0"}}),
				vpcInfo("ext.ext-bgp-01", map[string]*gwapi.VPCInfoSubnet{"external": {CIDR: "0.0.0.0/0"}}),
			}
			objs = append(objs, icServer("server-02", "leaf-03", "vpc-02")...)
			if tt.gwPeering != nil {
				objs = append(objs, tt.gwPeering)
			}

			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			r, err := IsExternalSubnetReachable(context.Background(), kube, "server-02", "0.0.0.0/0", true)
			require.NoError(t, err)
			require.Equal(t, tt.want, r.Reachable)
		})
	}
}
