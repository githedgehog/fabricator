// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	coreapi "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestHealthListsAgentsOnce(t *testing.T) {
	t.Parallel()

	// Every Agent with its padded status is hundreds of megabytes at scale, so
	// the objects and fabric sections must share one List rather than each
	// fetch it - otherwise health is a measurable load on what it measures.
	scheme, err := benchScheme()
	require.NoError(t, err)

	var agentLists atomic.Int32

	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&agentapi.Agent{ObjectMeta: kmetav1.ObjectMeta{Name: "leaf-1", Namespace: kmetav1.NamespaceDefault}}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c kclient.WithWatch, list kclient.ObjectList, opts ...kclient.ListOption) error {
				if _, ok := list.(*agentapi.AgentList); ok {
					agentLists.Add(1)
				}

				return c.List(ctx, list, opts...)
			},
		}).
		Build()

	require.NoError(t, Health(t.Context(), kube, nil, io.Discard, HealthOpts{Stats: true}))
	require.Equal(t, int32(1), agentLists.Load())
}

func TestDeleteIfPresentCountsOnlyRealDeletions(t *testing.T) {
	t.Parallel()

	// A partial or repeated clean hits missing objects all the time; that is
	// not an error, but counting it as a deletion makes the summary lie.
	scheme, err := benchScheme()
	require.NoError(t, err)

	present := &coreapi.ServiceAccount{ObjectMeta: kmetav1.ObjectMeta{Name: "agent--leaf-1", Namespace: kmetav1.NamespaceDefault}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(present).Build()

	deleted, err := deleteIfPresent(t.Context(), kube, present.DeepCopy())
	require.NoError(t, err)
	require.True(t, deleted)

	deleted, err = deleteIfPresent(t.Context(), kube, present.DeepCopy())
	require.NoError(t, err, "a missing object is not an error")
	require.False(t, deleted, "and it was not deleted by this call")
}
