// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package k3s_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	flowcontrolapi "k8s.io/api/flowcontrol/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// stockLevels are the stock limited priority levels and their shares, which is
// what the totals are sized to preserve.
var stockLevels = map[string]int{
	"catch-all":       5,
	"global-default":  20,
	"leader-election": 10,
	"node-high":       40,
	"system":          30,
	"workload-high":   40,
	"workload-low":    100,
}

// Adding our levels must not cost any stock level a seat: the raised totals
// have to give each one at least what it gets on a stock 600-seat cluster.
func TestFlowControlKeepsStockSeats(t *testing.T) {
	t.Parallel()

	stockShares, ourShares := 0, k3s.SharesControl+k3s.SharesAgents
	for _, shares := range stockLevels {
		stockShares += shares
	}

	require.Equal(t, k3s.TotalSeats, k3s.MaxRequestsInflight+k3s.MaxMutatingRequestsInflight, "the inflight flags must add up to TotalSeats")

	for name, shares := range stockLevels {
		stock := 600 * shares / stockShares
		ours := k3s.TotalSeats * shares / (stockShares + ourShares)
		require.GreaterOrEqual(t, ours, stock, "%s would drop from %d to %d seats", name, stock, ours)
	}
}

// The schemas have to sit after every stock schema that matches something
// specific (up to 900) and before the catch-all service-accounts one (9000),
// leases first, and point at levels that exist.
func TestFlowControlSchemas(t *testing.T) {
	t.Parallel()

	objs, err := k3s.InstallFlowControl(fabapi.Fabricator{})
	require.NoError(t, err)

	levels := map[string]bool{"leader-election": true}
	schemas := map[string]*flowcontrolapi.FlowSchema{}

	for _, obj := range objs {
		switch obj := obj.(type) {
		case *flowcontrolapi.PriorityLevelConfiguration:
			require.Equal(t, flowcontrolapi.PriorityLevelEnablementLimited, obj.Spec.Type)
			require.True(t, strings.HasPrefix(obj.Name, "hh-"), obj.Name)
			levels[obj.Name] = true
		case *flowcontrolapi.FlowSchema:
			require.True(t, strings.HasPrefix(obj.Name, "hh-"), obj.Name)
			schemas[obj.Name] = obj
		default:
			t.Fatalf("unexpected object %T", obj)
		}
	}

	require.Len(t, schemas, 3)
	for name, schema := range schemas {
		require.True(t, levels[schema.Spec.PriorityLevelConfiguration.Name], "%s points at missing level %s", name, schema.Spec.PriorityLevelConfiguration.Name)
		require.Greater(t, schema.Spec.MatchingPrecedence, int32(900), name)
		require.Less(t, schema.Spec.MatchingPrecedence, int32(9000), name)
	}

	lease, ctrl, agents := schemas[k3s.SchemaLeaderElection], schemas[k3s.SchemaControl], schemas[k3s.SchemaAgents]
	require.Less(t, lease.Spec.MatchingPrecedence, ctrl.Spec.MatchingPrecedence, "leases must match before the controllers' catch-all rule")
	require.Less(t, ctrl.Spec.MatchingPrecedence, agents.Spec.MatchingPrecedence)

	// The controllers always keep at least half their seats; agents can borrow,
	// but never more than their own size again.
	for _, obj := range objs {
		if plc, ok := obj.(*flowcontrolapi.PriorityLevelConfiguration); ok {
			switch plc.Name {
			case k3s.PriorityControl:
				require.LessOrEqual(t, *plc.Spec.Limited.LendablePercent, int32(50))
			case k3s.PriorityAgents:
				require.NotNil(t, plc.Spec.Limited.BorrowingLimitPercent, "unset means unlimited borrowing")
				require.LessOrEqual(t, *plc.Spec.Limited.BorrowingLimitPercent, int32(100))
			}
		}
	}

	// Leases are what keeps a controller leader, so they must stay in a level
	// that lends nothing, whatever hh-control lends.
	require.Equal(t, "leader-election", lease.Spec.PriorityLevelConfiguration.Name)

	// Every ServiceAccount in fab, whatever its name, is a controller.
	for _, schema := range []*flowcontrolapi.FlowSchema{lease, ctrl} {
		subject := schema.Spec.Rules[0].Subjects[0]
		require.Equal(t, flowcontrolapi.SubjectKindServiceAccount, subject.Kind, schema.Name)
		require.Equal(t, "fab", subject.ServiceAccount.Namespace, schema.Name)
		require.Equal(t, flowcontrolapi.NameAll, subject.ServiceAccount.Name, schema.Name)
	}
}

// The objects have to go through the same create-or-update path the
// fabricator controller uses, and re-applying them must be a no-op rather than
// an update on every reconcile.
func TestFlowControlEnforce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	kube := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).Build()

	require.NoError(t, comp.EnforceKubeInstall(ctx, kube, fabapi.Fabricator{}, k3s.InstallFlowControl))

	objs, err := k3s.InstallFlowControl(fabapi.Fabricator{})
	require.NoError(t, err)

	for _, obj := range objs {
		res, err := comp.CreateOrUpdate(ctx, kube, obj)
		require.NoError(t, err, "%T %s", obj, obj.GetName())
		require.Equal(t, ctrlutil.OperationResultNone, res, "%T %s changed on re-apply", obj, obj.GetName())
	}

	plcs := &flowcontrolapi.PriorityLevelConfigurationList{}
	require.NoError(t, kube.List(ctx, plcs))
	require.Len(t, plcs.Items, 2)

	schemas := &flowcontrolapi.FlowSchemaList{}
	require.NoError(t, kube.List(ctx, schemas))
	require.Len(t, schemas.Items, 3)
}

func TestServerConfigRaisesInflight(t *testing.T) {
	t.Parallel()

	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Spec.Config.Control.VIP = "172.30.0.1/32"
	control := fabapi.ControlNode{
		ObjectMeta: kmetav1.ObjectMeta{Name: "control-1"},
		Spec: fabapi.ControlNodeSpec{
			Management: fabapi.ControlNodeManagement{IP: meta.Prefix("172.30.0.5/21"), Interface: "enp2s1"},
		},
	}

	cfg, err := k3s.ServerConfig(f, control)
	require.NoError(t, err)
	require.Contains(t, cfg, "max-requests-inflight=")
	require.Contains(t, cfg, "max-mutating-requests-inflight=")

	require.Contains(t, cfg, "quota-backend-bytes=34359738368")

	// It is rewritten on upgrade, so it has to say so to anyone editing it.
	require.Contains(t, cfg, "Managed by Hedgehog Fabricator")
	require.Contains(t, cfg, k3s.ConfigDropInDir)
}
