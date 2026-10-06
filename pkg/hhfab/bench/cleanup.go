// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	coreapi "k8s.io/api/core/v1"
	rbacapi "k8s.io/api/rbac/v1"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// AgentPrefix is the name prefix the Fabric controller gives the per-switch
// ServiceAccount, Role, RoleBinding and Secrets it creates for every Agent.
// Nothing garbage-collects those - the controller sets no owner references and
// returns early when the Switch is gone - so cleanup has to remove them
// explicitly.
const AgentPrefix = "agent--"

// CleanResult records what one kind's deletion did.
type CleanResult struct {
	Kind    string
	Deleted int
	Took    time.Duration
}

// Clean removes everything the bench created, in an order the delete webhooks
// accept: dependents before the objects they reference. Teardown at scale is
// itself a load test, so each kind is timed and reported.
//
// If fabrics is non-empty only those fabrics are removed, otherwise everything
// carrying the bench label is.
func Clean(ctx context.Context, kube kclient.Client, fabrics []string) ([]CleanResult, error) {
	results := []CleanResult{}

	// Record which switches belong to the bench before deleting them. Agents
	// are created by the controller and carry no bench label, so the labelled
	// Switches are the only trustworthy way to know which Agents are ours -
	// matching Agent names by shape would also hit real switches on a cluster
	// that has both.
	switchNames, err := benchSwitchNames(ctx, kube, fabrics)
	if err != nil {
		return nil, err
	}

	// Reverse dependency order. VPC-side objects first: a VPC cannot be deleted
	// while attachments reference it, and a Connection cannot go while an
	// attachment points at it. Fabrics go last, since the delete webhook refuses
	// one while any object still names it.
	kinds := []struct {
		name string
		list func() kclient.ObjectList
	}{
		{"VPCPeering", func() kclient.ObjectList { return &vpcapi.VPCPeeringList{} }},
		{"VPCAttachment", func() kclient.ObjectList { return &vpcapi.VPCAttachmentList{} }},
		{"VPC", func() kclient.ObjectList { return &vpcapi.VPCList{} }},
		{"Connection", func() kclient.ObjectList { return &wiringapi.ConnectionList{} }},
		{"Server", func() kclient.ObjectList { return &wiringapi.ServerList{} }},
		{"Switch", func() kclient.ObjectList { return &wiringapi.SwitchList{} }},
		{"SwitchGroup", func() kclient.ObjectList { return &wiringapi.SwitchGroupList{} }},
		{"VLANNamespace", func() kclient.ObjectList { return &wiringapi.VLANNamespaceList{} }},
		{"IPv4Namespace", func() kclient.ObjectList { return &vpcapi.IPv4NamespaceList{} }},
		{"Fabric", func() kclient.ObjectList { return &wiringapi.FabricList{} }},
	}

	for _, kind := range kinds {
		res, err := deleteLabeled(ctx, kube, kind.name, kind.list(), fabrics)
		results = append(results, res)
		if err != nil {
			return results, err
		}
	}

	agentRes, err := cleanAgents(ctx, kube, switchNames)
	results = append(results, agentRes...)
	if err != nil {
		return results, err
	}

	return results, nil
}

// deleteLabeled removes every object of one kind carrying the bench label.
func deleteLabeled(ctx context.Context, kube kclient.Client, kind string, list kclient.ObjectList, fabrics []string) (CleanResult, error) {
	res := CleanResult{Kind: kind}
	start := time.Now()

	if err := kube.List(ctx, list, kclient.HasLabels{LabelFabric}); err != nil {
		// A release from before Fabric objects does not serve the kind at all,
		// which leaves nothing of it to clean.
		if kmeta.IsNoMatchError(err) {
			return res, nil
		}

		return res, fmt.Errorf("listing %s: %w", kind, err)
	}

	objs := []kclient.Object{}
	for _, obj := range apiutil.KubeListItems(list) {
		if wantFabric(obj.GetLabels()[LabelFabric], fabrics) {
			objs = append(objs, obj)
		}
	}

	// Like the apply, a kind with tens of thousands of objects takes minutes,
	// so report how far it got rather than staying silent until it is done.
	var done atomic.Int64
	stopProgress := logProgress(ctx, "Deleting", "kind", kind, len(objs), &done)
	defer stopProgress()

	for _, obj := range objs {
		if err := kube.Delete(ctx, obj); err != nil && !isNotFound(err) {
			return res, fmt.Errorf("deleting %s %s: %w", kind, obj.GetName(), err)
		}

		res.Deleted++
		done.Add(1)
	}

	stopProgress()
	res.Took = time.Since(start)

	if res.Deleted > 0 {
		slog.Info("Deleted", "kind", kind, "count", res.Deleted, "took", res.Took.Truncate(time.Millisecond))
	}

	return res, nil
}

// cleanAgents removes the Agent objects and the per-switch ServiceAccount,
// Role, RoleBinding and Secrets the Fabric controller created, none of which
// are garbage collected when the Switch goes away.
func cleanAgents(ctx context.Context, kube kclient.Client, names []string) ([]CleanResult, error) {
	start := time.Now()

	agentRes := CleanResult{Kind: "Agent"}

	for _, name := range names {
		agent := &agentapi.Agent{ObjectMeta: kmetav1.ObjectMeta{Name: name, Namespace: kmetav1.NamespaceDefault}}

		deleted, err := deleteIfPresent(ctx, kube, agent)
		if err != nil {
			return nil, fmt.Errorf("deleting agent %s: %w", name, err)
		}
		if deleted {
			agentRes.Deleted++
		}
	}

	agentRes.Took = time.Since(start)
	if agentRes.Deleted > 0 {
		slog.Info("Deleted", "kind", "Agent", "count", agentRes.Deleted, "took", agentRes.Took.Truncate(time.Millisecond))
	}

	rbacRes := CleanResult{Kind: "Agent RBAC"}
	rbacStart := time.Now()

	for _, name := range names {
		sa := AgentPrefix + name

		objs := []kclient.Object{
			&coreapi.Secret{ObjectMeta: kmetav1.ObjectMeta{Name: sa, Namespace: kmetav1.NamespaceDefault}},
			&coreapi.Secret{ObjectMeta: kmetav1.ObjectMeta{Name: sa + "-satoken", Namespace: kmetav1.NamespaceDefault}},
			&rbacapi.RoleBinding{ObjectMeta: kmetav1.ObjectMeta{Name: sa, Namespace: kmetav1.NamespaceDefault}},
			&rbacapi.Role{ObjectMeta: kmetav1.ObjectMeta{Name: sa, Namespace: kmetav1.NamespaceDefault}},
			&coreapi.ServiceAccount{ObjectMeta: kmetav1.ObjectMeta{Name: sa, Namespace: kmetav1.NamespaceDefault}},
		}

		for _, obj := range objs {
			deleted, err := deleteIfPresent(ctx, kube, obj)
			if err != nil {
				return nil, fmt.Errorf("deleting %T %s: %w", obj, obj.GetName(), err)
			}
			if deleted {
				rbacRes.Deleted++
			}
		}
	}

	rbacRes.Took = time.Since(rbacStart)
	if rbacRes.Deleted > 0 {
		slog.Info("Deleted", "kind", "Agent RBAC", "count", rbacRes.Deleted, "took", rbacRes.Took.Truncate(time.Millisecond))
	}

	return []CleanResult{agentRes, rbacRes}, nil
}

// benchSwitchNames lists the switches the bench owns, which is also the set of
// Agents and per-switch RBAC objects to remove.
func benchSwitchNames(ctx context.Context, kube kclient.Client, fabrics []string) ([]string, error) {
	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches, kclient.HasLabels{LabelFabric}); err != nil {
		return nil, fmt.Errorf("listing switches: %w", err)
	}

	names := []string{}
	for _, sw := range switches.Items {
		if wantFabric(sw.Labels[LabelFabric], fabrics) {
			names = append(names, sw.Name)
		}
	}

	return names, nil
}

// wantFabric reports whether a fabric is in the requested set; an empty set
// means every bench fabric.
func wantFabric(fabric string, fabrics []string) bool {
	if fabric == "" {
		return false
	}
	if len(fabrics) == 0 {
		return true
	}

	return slices.Contains(fabrics, fabric)
}

func isNotFound(err error) bool {
	return kclient.IgnoreNotFound(err) == nil
}

// deleteIfPresent deletes obj and reports whether anything was there to
// delete. A missing object is not an error - a partial or repeated clean hits
// that constantly - but it must not be counted as removed either, or the
// summary claims deletions that never happened.
func deleteIfPresent(ctx context.Context, kube kclient.Client, obj kclient.Object) (bool, error) {
	err := kube.Delete(ctx, obj)

	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, err //nolint:wrapcheck // callers add the object identity
	}
}
