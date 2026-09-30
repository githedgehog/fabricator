// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"testing"

	"github.com/stretchr/testify/require"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestApplyStopsAfterAnEmptyRequestedPhase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	// A topology with switches and a VPC but no servers. Asking to stop after
	// the servers phase must stop there, even though it has nothing to apply,
	// rather than carrying on and creating the VPC.
	l := apiutil.NewLoader()
	require.NoError(t, l.Add(ctx,
		&wiringapi.Switch{ObjectMeta: kmetav1.ObjectMeta{Name: "sw1", Namespace: kmetav1.NamespaceDefault}},
		&vpcapi.VPC{ObjectMeta: kmetav1.ObjectMeta{Name: "vpc1", Namespace: kmetav1.NamespaceDefault}},
	))

	scheme, err := benchScheme()
	require.NoError(t, err)

	kube := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err = Apply(ctx, kube, l, ApplyOpts{Workers: 1, Phase: PhaseServers})
	require.NoError(t, err)

	key := kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault}

	key.Name = "sw1"
	require.NoError(t, kube.Get(ctx, key, &wiringapi.Switch{}), "phases before the stop are applied")

	key.Name = "vpc1"
	err = kube.Get(ctx, key, &vpcapi.VPC{})
	require.True(t, kapierrors.IsNotFound(err), "phases after the stop must not run, got %v", err)
}
