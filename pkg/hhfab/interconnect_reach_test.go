// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func icVPC(name, fabric, subnet string) *vpcapi.VPC {
	return &vpcapi.VPC{
		ObjectMeta: kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault},
		Spec: vpcapi.VPCSpec{
			Topology: vpcapi.VPCTopology{Fabric: fabric},
			Subnets:  map[string]*vpcapi.VPCSubnet{"subnet-01": {Subnet: subnet}},
		},
	}
}

func icExternalPeering(name, vpc, external, prefix string) *vpcapi.ExternalPeering {
	return &vpcapi.ExternalPeering{
		ObjectMeta: kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault, Labels: map[string]string{vpcapi.LabelVPC: vpc}},
		Spec: vpcapi.ExternalPeeringSpec{Permit: vpcapi.ExternalPeeringSpecPermit{
			VPC:      vpcapi.ExternalPeeringSpecVPC{Name: vpc, Subnets: []string{"subnet-01"}},
			External: vpcapi.ExternalPeeringSpecExternal{Name: external, Prefixes: []vpcapi.ExternalPeeringSpecPrefix{{Prefix: prefix}}},
		}},
	}
}

func icAttachment(external string) *vpcapi.ExternalAttachment {
	return &vpcapi.ExternalAttachment{
		ObjectMeta: kmetav1.ObjectMeta{
			Name: "leaf--" + external, Namespace: kmetav1.NamespaceDefault,
			Labels: map[string]string{vpcapi.LabelExternal: external},
		},
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
