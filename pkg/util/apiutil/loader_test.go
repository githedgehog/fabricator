// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Wiring is stored the way the admission webhooks would store it, so code that
// reads one object while looking at another, like validation, sees them
// defaulted alike.
func TestLoadAddWiringDefaults(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()

	require.NoError(t, l.LoadAddWiring(ctx, []byte(`
apiVersion: wiring.githedgehog.com/v1beta1
kind: SwitchGroup
metadata:
  name: eslag-1
`)))

	group := &wiringapi.SwitchGroup{}
	require.NoError(t, l.GetClient().Get(ctx, kclient.ObjectKey{Name: "eslag-1", Namespace: kmetav1.NamespaceDefault}, group))
	require.Equal(t, wiringapi.DefaultFabric, group.Spec.Topology.Fabric)
	require.Equal(t, wiringapi.ListLabelValue, group.Labels[wiringapi.ListLabelFabric(wiringapi.DefaultFabric)])
}

// Only wiring kinds are accepted, as with LoadAdd for them.
func TestLoadAddWiringRejectsOtherKinds(t *testing.T) {
	t.Parallel()

	require.Error(t, apiutil.NewLoader().LoadAddWiring(t.Context(), []byte(`
apiVersion: fabricator.githedgehog.com/v1beta1
kind: Fabricator
metadata:
  name: default
`)))
}
