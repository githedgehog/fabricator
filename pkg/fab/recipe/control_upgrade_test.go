// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	fcintapi "go.githedgehog.com/fabric/api/fcint/v1alpha1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// The k3s config is rewritten on upgrade only when it differs, since a write
// is what makes the upgrade restart k3s.
func TestUpdateConfigFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")

	changed, err := updateConfigFile(path, "a: 1\n")
	require.NoError(t, err)
	require.True(t, changed, "a missing file is a change")

	changed, err = updateConfigFile(path, "a: 1\n")
	require.NoError(t, err)
	require.False(t, changed, "the same content must not count as a change")

	changed, err = updateConfigFile(path, "a: 2\n")
	require.NoError(t, err)
	require.True(t, changed)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "a: 2\n", string(got))
}

// Failing to get the fabric controller is retried, but it being unready fails right away.
func TestCheckFabricCtrlReady(t *testing.T) {
	t.Parallel()

	fc := func(initialized string, ready kmetav1.ConditionStatus) *fcintapi.FabricController {
		return &fcintapi.FabricController{
			Name: fcintapi.FabricControllerName, Namespace: comp.FabNamespace,
			Status: fcintapi.FabricControllerStatus{
				Version:            "v1",
				InitializedVersion: initialized,
				Conditions:         []kmetav1.Condition{{Type: fcintapi.ConditionReady, Status: ready}},
			},
		}
	}

	for name, test := range map[string]struct {
		obj      *fcintapi.FabricController
		failGets int
		err      string
		gets     int
	}{
		"ready":                {obj: fc("v1", kmetav1.ConditionTrue), gets: 1},
		"connection reset":     {obj: fc("v1", kmetav1.ConditionTrue), failGets: 2, gets: 3},
		"not ready":            {obj: fc("v1", kmetav1.ConditionFalse), err: "is not ready", gets: 1},
		"previous initialized": {obj: fc("v0", kmetav1.ConditionTrue), err: "is not ready", gets: 1},
		"not found":            {err: "not found", gets: 1},
		"reset then not ready": {obj: fc("v1", kmetav1.ConditionFalse), failGets: 1, err: "is not ready", gets: 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, fcintapi.AddToScheme(scheme))
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if test.obj != nil {
				builder = builder.WithObjects(test.obj)
			}

			gets := 0
			kube := builder.WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, kube kclient.WithWatch, key kclient.ObjectKey, obj kclient.Object, opts ...kclient.GetOption) error {
					gets++
					if gets <= test.failGets {
						return fmt.Errorf("getting: %w", syscall.ECONNRESET)
					}

					return kube.Get(ctx, key, obj, opts...)
				},
			}).Build()

			err := checkFabricCtrlReady(t.Context(), kube)
			if test.err == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.err)
			}
			require.Equal(t, test.gets, gets)
		})
	}
}
