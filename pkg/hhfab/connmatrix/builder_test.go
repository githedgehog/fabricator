// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package connmatrix

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeKube(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, vpcapi.AddToScheme(scheme))

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func boolPtr(b bool) *bool { return &b }

func mkVPC(name string, subnets map[string]*vpcapi.VPCSubnet, opts ...func(*vpcapi.VPC)) *vpcapi.VPC {
	v := &vpcapi.VPC{
		ObjectMeta: kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault},
		Spec:       vpcapi.VPCSpec{Subnets: subnets},
	}
	for _, opt := range opts {
		opt(v)
	}

	return v
}

func mkVPCPeering(name, vpcA, vpcB string, subnetsA, subnetsB []string) *vpcapi.VPCPeering {
	return &vpcapi.VPCPeering{
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: kmetav1.NamespaceDefault,
			Labels: map[string]string{
				vpcapi.ListLabelVPC(vpcA): vpcapi.ListLabelValue,
				vpcapi.ListLabelVPC(vpcB): vpcapi.ListLabelValue,
			},
		},
		Spec: vpcapi.VPCPeeringSpec{
			Permit: []map[string]vpcapi.VPCPeer{
				{
					vpcA: {Subnets: subnetsA},
					vpcB: {Subnets: subnetsB},
				},
			},
		},
	}
}

func TestIntraVPCProvider(t *testing.T) {
	subnets := map[string]*vpcapi.VPCSubnet{
		"sub-a": {Subnet: "10.0.1.0/24"},
		"sub-b": {Subnet: "10.0.2.0/24"},
	}

	srvA := Endpoint{Server: "s1", Subnet: "vpc-1/sub-a"}
	srvB := Endpoint{Server: "s2", Subnet: "vpc-1/sub-b"}

	t.Run("default-allow produces bidirectional ALLOW", func(t *testing.T) {
		kube := newFakeKube(t, mkVPC("vpc-1", subnets))
		exps, err := (&IntraVPCProvider{}).BuildExpectations(context.Background(), kube, []Endpoint{srvA, srvB}, nil)
		require.NoError(t, err)
		require.Len(t, exps, 2, "one per direction")
		for _, e := range exps {
			require.Equal(t, VerdictAllow, e.Verdict)
			require.Equal(t, ReachabilityReasonIntraVPC, e.Reason)
		}
	})

	t.Run("isolated subnets without permit produce no entry", func(t *testing.T) {
		isolated := map[string]*vpcapi.VPCSubnet{
			"sub-a": {Subnet: "10.0.1.0/24", Isolated: boolPtr(true)},
			"sub-b": {Subnet: "10.0.2.0/24", Isolated: boolPtr(true)},
		}
		kube := newFakeKube(t, mkVPC("vpc-1", isolated))
		exps, err := (&IntraVPCProvider{}).BuildExpectations(context.Background(), kube, []Endpoint{srvA, srvB}, nil)
		require.NoError(t, err)
		require.Empty(t, exps)
	})

	t.Run("isolated subnets with explicit permit produce ALLOW", func(t *testing.T) {
		isolated := map[string]*vpcapi.VPCSubnet{
			"sub-a": {Subnet: "10.0.1.0/24", Isolated: boolPtr(true)},
			"sub-b": {Subnet: "10.0.2.0/24", Isolated: boolPtr(true)},
		}
		vpc := mkVPC("vpc-1", isolated, func(v *vpcapi.VPC) {
			v.Spec.Permit = [][]string{{"sub-a", "sub-b"}}
		})
		kube := newFakeKube(t, vpc)
		exps, err := (&IntraVPCProvider{}).BuildExpectations(context.Background(), kube, []Endpoint{srvA, srvB}, nil)
		require.NoError(t, err)
		require.Len(t, exps, 2)
	})

	t.Run("cross-VPC endpoints ignored", func(t *testing.T) {
		kube := newFakeKube(t,
			mkVPC("vpc-1", map[string]*vpcapi.VPCSubnet{"sub-a": {Subnet: "10.0.1.0/24"}}),
			mkVPC("vpc-2", map[string]*vpcapi.VPCSubnet{"sub-a": {Subnet: "10.0.2.0/24"}}),
		)
		eps := []Endpoint{
			{Server: "s1", Subnet: "vpc-1/sub-a"},
			{Server: "s2", Subnet: "vpc-2/sub-a"},
		}
		exps, err := (&IntraVPCProvider{}).BuildExpectations(context.Background(), kube, eps, nil)
		require.NoError(t, err)
		require.Empty(t, exps, "different VPCs: IntraVPCProvider must not emit")
	})
}

func TestSwitchPeeringProvider(t *testing.T) {
	vpcs := []client.Object{
		mkVPC("vpc-1", map[string]*vpcapi.VPCSubnet{
			"sub-a": {Subnet: "10.0.1.0/24"},
			"sub-b": {Subnet: "10.0.11.0/24"},
		}),
		mkVPC("vpc-2", map[string]*vpcapi.VPCSubnet{
			"sub-a": {Subnet: "10.0.2.0/24"},
		}),
	}
	epsA := Endpoint{Server: "s1", Subnet: "vpc-1/sub-a"}
	epsB := Endpoint{Server: "s2", Subnet: "vpc-2/sub-a"}
	epsA2 := Endpoint{Server: "s3", Subnet: "vpc-1/sub-b"}

	t.Run("peering without subnet filter allows all subnets", func(t *testing.T) {
		peering := mkVPCPeering("vpc-1--vpc-2", "vpc-1", "vpc-2", nil, nil)
		kube := newFakeKube(t, append(append([]client.Object{}, vpcs...), peering)...)
		exps, err := (&SwitchPeeringProvider{}).BuildExpectations(context.Background(), kube, []Endpoint{epsA, epsA2, epsB}, nil)
		require.NoError(t, err)
		// Both vpc-1 subnets can reach vpc-2/sub-a and vice versa.
		require.Len(t, exps, 4)
		for _, e := range exps {
			require.Equal(t, VerdictAllow, e.Verdict)
			require.Equal(t, ReachabilityReasonSwitchPeering, e.Reason)
			require.Equal(t, "vpc-1--vpc-2", e.Peering)
		}
	})

	t.Run("peering subnet filter restricts endpoints", func(t *testing.T) {
		peering := mkVPCPeering("vpc-1--vpc-2", "vpc-1", "vpc-2",
			[]string{"sub-a"}, []string{"sub-a"})
		kube := newFakeKube(t, append(append([]client.Object{}, vpcs...), peering)...)
		exps, err := (&SwitchPeeringProvider{}).BuildExpectations(context.Background(), kube, []Endpoint{epsA, epsA2, epsB}, nil)
		require.NoError(t, err)
		// Only sub-a ↔ sub-a should match; sub-b should not.
		require.Len(t, exps, 2)
		for _, e := range exps {
			require.True(t,
				(e.Source == epsA.Key() && e.Destination == epsB.Key()) ||
					(e.Source == epsB.Key() && e.Destination == epsA.Key()),
				"unexpected pair %v → %v", e.Source, e.Destination)
		}
	})

	t.Run("no peering produces no entries", func(t *testing.T) {
		kube := newFakeKube(t, vpcs...)
		exps, err := (&SwitchPeeringProvider{}).BuildExpectations(context.Background(), kube, []Endpoint{epsA, epsB}, nil)
		require.NoError(t, err)
		require.Empty(t, exps)
	})
}
