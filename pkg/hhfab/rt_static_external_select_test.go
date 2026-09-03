// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"slices"
	"testing"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// seServer describes a server attached to a VPC subnet over a connection of the given type.
type seServer struct {
	name      string
	sw        string
	vpc       string
	subnet    string
	unbundled bool
}

// seUnbundledConn builds an unbundled server-to-switch connection, the kind staticExternalTest
// picks its target from.
func seUnbundledConn(server, sw string) *wiringapi.Connection {
	return &wiringapi.Connection{
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      server + "--unbundled--" + sw,
			Namespace: kmetav1.NamespaceDefault,
			Labels:    map[string]string{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeUnbundled},
		},
		Spec: wiringapi.ConnectionSpec{
			Unbundled: &wiringapi.ConnUnbundled{
				Link: wiringapi.ServerToSwitchLink{
					Server: wiringapi.NewBasePortName(server + "/enp2s1"),
					Switch: wiringapi.NewBasePortName(sw + "/E1/1"),
				},
			},
		},
	}
}

// seBundledConn builds a bundled server-to-switch connection, which is never a candidate but still
// keeps its server attached to a VPC.
func seBundledConn(server, sw string) *wiringapi.Connection {
	return &wiringapi.Connection{
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      server + "--bundled--" + sw,
			Namespace: kmetav1.NamespaceDefault,
			Labels:    map[string]string{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeBundled},
		},
		Spec: wiringapi.ConnectionSpec{
			Bundled: &wiringapi.ConnBundled{
				Links: []wiringapi.ServerToSwitchLink{
					{
						Server: wiringapi.NewBasePortName(server + "/enp2s1"),
						Switch: wiringapi.NewBasePortName(sw + "/E1/2"),
					},
					{
						Server: wiringapi.NewBasePortName(server + "/enp2s2"),
						Switch: wiringapi.NewBasePortName(sw + "/E1/3"),
					},
				},
			},
		},
	}
}

// seFixture builds a fake client holding the given servers, their connections, the VPCs they are
// attached to and the matching VPCAttachments, and returns the unbundled connections as the
// candidate list alongside the VPC list. revCandidates and revVPCs flip the order of each returned
// slice on its own, standing in for the unordered kube.List results selectStaticExternalTarget has
// to cope with.
func seFixture(t *testing.T, servers []seServer, revCandidates, revVPCs bool) (kclient.Client, []wiringapi.Connection, *vpcapi.VPCList) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := wiringapi.AddToScheme(scheme); err != nil {
		t.Fatalf("adding wiringapi to scheme: %v", err)
	}
	if err := vpcapi.AddToScheme(scheme); err != nil {
		t.Fatalf("adding vpcapi to scheme: %v", err)
	}

	objs := []kclient.Object{}
	vpcNames := []string{}
	candidates := []wiringapi.Connection{}

	for _, s := range servers {
		var conn *wiringapi.Connection
		if s.unbundled {
			conn = seUnbundledConn(s.name, s.sw)
			candidates = append(candidates, *conn)
		} else {
			conn = seBundledConn(s.name, s.sw)
		}
		objs = append(objs, conn)

		if !slices.Contains(vpcNames, s.vpc) {
			vpcNames = append(vpcNames, s.vpc)
			objs = append(objs, &vpcapi.VPC{
				ObjectMeta: kmetav1.ObjectMeta{Name: s.vpc, Namespace: kmetav1.NamespaceDefault},
			})
		}

		objs = append(objs, &vpcapi.VPCAttachment{
			ObjectMeta: kmetav1.ObjectMeta{
				Name:      conn.Name + "--" + s.vpc + "--" + s.subnet,
				Namespace: kmetav1.NamespaceDefault,
				Labels: map[string]string{
					wiringapi.LabelConnection: conn.Name,
					wiringapi.LabelVPC:        s.vpc,
				},
			},
			Spec: vpcapi.VPCAttachmentSpec{
				Subnet:     s.vpc + "/" + s.subnet,
				Connection: conn.Name,
			},
		})
	}

	vpcList := &vpcapi.VPCList{}
	for _, name := range vpcNames {
		vpcList.Items = append(vpcList.Items, vpcapi.VPC{
			ObjectMeta: kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault},
		})
	}

	if revCandidates {
		slices.Reverse(candidates)
	}
	if revVPCs {
		slices.Reverse(vpcList.Items)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(), candidates, vpcList
}

// TestSelectStaticExternalTarget checks the candidate picked is the same one whatever order the
// candidates and VPCs come back in, including when only one of the two lists is reversed, and that
// an infeasible topology is reported as a skip rather than a pick.
func TestSelectStaticExternalTarget(t *testing.T) {
	// env-5 mesh/l3vni shape: two unbundled candidates, but replacing server-7
	// empties vpc-02, so only server-5 leaves 2 VPCs with an attached server
	env5 := []seServer{
		{name: "server-1", sw: "ds5000-01", vpc: "vpc-01", subnet: "subnet-01"},
		{name: "server-2", sw: "ds5000-01", vpc: "vpc-01", subnet: "subnet-02"},
		{name: "server-5", sw: "ds5000-02", vpc: "vpc-01", subnet: "subnet-03", unbundled: true},
		{name: "server-7", sw: "ds5000-03", vpc: "vpc-02", subnet: "subnet-01", unbundled: true},
	}

	tests := []struct {
		name       string
		servers    []seServer
		wantConn   string
		wantTarget string
		wantIn     string
		wantOthr   string
	}{
		{
			name:       "picks the only feasible candidate",
			servers:    env5,
			wantConn:   "server-5--unbundled--ds5000-02",
			wantTarget: "vpc-01",
			wantIn:     "vpc-01",
			wantOthr:   "vpc-02",
		},
		{
			// vpc-01 keeps server-1 once server-2 is replaced, so the target's own VPC is still
			// usable as inVPC and wins on name order
			name: "picks the lowest-named candidate when several are feasible",
			servers: []seServer{
				{name: "server-1", sw: "ds5000-01", vpc: "vpc-01", subnet: "subnet-01"},
				{name: "server-2", sw: "ds5000-02", vpc: "vpc-01", subnet: "subnet-02", unbundled: true},
				{name: "server-3", sw: "ds5000-03", vpc: "vpc-02", subnet: "subnet-01"},
				{name: "server-4", sw: "ds5000-04", vpc: "vpc-02", subnet: "subnet-02", unbundled: true},
			},
			wantConn:   "server-2--unbundled--ds5000-02",
			wantTarget: "vpc-01",
			wantIn:     "vpc-01",
			wantOthr:   "vpc-02",
		},
		{
			name: "no candidate leaves 2 VPCs with an attached server",
			servers: []seServer{
				{name: "server-1", sw: "ds5000-01", vpc: "vpc-01", subnet: "subnet-01", unbundled: true},
				{name: "server-2", sw: "ds5000-02", vpc: "vpc-02", subnet: "subnet-01", unbundled: true},
			},
			wantConn: "",
		},
	}

	// candidate order and VPC order come from separate kube.List calls, so flip them independently
	orders := []struct {
		name          string
		revCandidates bool
		revVPCs       bool
	}{
		{name: "candidates asc, VPCs asc"},
		{name: "candidates desc, VPCs asc", revCandidates: true},
		{name: "candidates asc, VPCs desc", revVPCs: true},
		{name: "candidates desc, VPCs desc", revCandidates: true, revVPCs: true},
	}

	for _, tt := range tests {
		for _, order := range orders {
			t.Run(tt.name+" ("+order.name+")", func(t *testing.T) {
				kube, candidates, vpcList := seFixture(t, tt.servers, order.revCandidates, order.revVPCs)

				sel, err := selectStaticExternalTarget(context.Background(), kube, candidates, vpcList)

				if tt.wantConn == "" {
					if !errors.Is(err, errNotEnoughVPCs) {
						t.Fatalf("expected errNotEnoughVPCs, got %v", err)
					}
					if sel != nil {
						t.Errorf("expected no selection, got %+v", sel)
					}

					return
				}

				if err != nil {
					t.Fatalf("selectStaticExternalTarget: %v", err)
				}
				if sel.conn.Name != tt.wantConn {
					t.Errorf("candidate: expected %s, got %s", tt.wantConn, sel.conn.Name)
				}
				if sel.targetVPC != tt.wantTarget {
					t.Errorf("targetVPC: expected %s, got %s", tt.wantTarget, sel.targetVPC)
				}
				if sel.inVPC.Name != tt.wantIn {
					t.Errorf("inVPC: expected %s, got %s", tt.wantIn, sel.inVPC.Name)
				}
				if sel.otherVPC.Name != tt.wantOthr {
					t.Errorf("otherVPC: expected %s, got %s", tt.wantOthr, sel.otherVPC.Name)
				}
				if sel.inServer == "" || sel.otherServer == "" {
					t.Errorf("expected both servers set, got inServer=%q otherServer=%q", sel.inServer, sel.otherServer)
				}
				target := sel.conn.Spec.Unbundled.Link.Server.DeviceName()
				if sel.inServer == target {
					t.Errorf("inServer %s is the server being replaced", sel.inServer)
				}
				if sel.otherServer == target {
					t.Errorf("otherServer %s is the server being replaced", sel.otherServer)
				}
			})
		}
	}
}

// A candidate whose connection has no VPCAttachment must be passed over rather than aborting the
// whole test, so a usable candidate later in the list still gets picked.
func TestSelectStaticExternalTargetSkipsUnattachedCandidate(t *testing.T) {
	kube, candidates, vpcList := seFixture(t, []seServer{
		{name: "server-1", sw: "ds5000-01", vpc: "vpc-01", subnet: "subnet-01"},
		{name: "server-2", sw: "ds5000-02", vpc: "vpc-01", subnet: "subnet-02", unbundled: true},
		{name: "server-3", sw: "ds5000-03", vpc: "vpc-02", subnet: "subnet-01"},
	}, false, false)

	// an unbundled connection with no VPCAttachment behind it, sorting ahead of server-2's
	unattached := seUnbundledConn("server-0", "ds5000-04")
	if err := kube.Create(context.Background(), unattached); err != nil {
		t.Fatalf("creating unattached connection: %v", err)
	}
	candidates = append([]wiringapi.Connection{*unattached}, candidates...)

	sel, err := selectStaticExternalTarget(context.Background(), kube, candidates, vpcList)
	if err != nil {
		t.Fatalf("selectStaticExternalTarget: %v", err)
	}
	if sel.conn.Name != "server-2--unbundled--ds5000-02" {
		t.Errorf("candidate: expected server-2--unbundled--ds5000-02, got %s", sel.conn.Name)
	}
	if sel.inVPC.Name != "vpc-01" || sel.otherVPC.Name != "vpc-02" {
		t.Errorf("expected inVPC vpc-01 and otherVPC vpc-02, got %s/%s", sel.inVPC.Name, sel.otherVPC.Name)
	}
}
