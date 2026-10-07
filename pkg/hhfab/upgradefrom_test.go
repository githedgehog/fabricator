// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUpgradeFromWithoutFabric(t *testing.T) {
	t.Parallel()

	for release, want := range map[string]bool{
		"26.03":    true,
		"26.03.1":  true,
		"26.04":    true,
		"v26.04.2": true,
		" 26.04 ":  true,
		"26.02":    false,
		"26.05":    false,
		"26.05.1":  false,
		"25.04":    false,
		"26":       false,
		"":         false,
	} {
		require.Equal(t, want, upgradeFromWithoutFabric(release), "release %q", release)
	}
}

func upgradeTestFab() fabapi.Fabricator {
	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
	f.Default()

	return f
}

func listFabrics(t *testing.T, l *apiutil.Loader) []wiringapi.Fabric {
	t.Helper()

	fabrics := &wiringapi.FabricList{}
	require.NoError(t, l.List(t.Context(), fabrics))

	return fabrics.Items
}

// Upgrading from a release from before Fabric objects, the wiring gets the
// Fabric/default the controller would create; otherwise it is left alone.
func TestInjectUpgradeDefaultFabric(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	f := upgradeTestFab()

	cfg, err := fabric.GetFabricConfig(f)
	require.NoError(t, err)

	l := apiutil.NewLoader()
	require.NoError(t, injectUpgradeDefaultFabric(ctx, l, f, "26.04.1"))

	fabrics := listFabrics(t, l)
	require.Len(t, fabrics, 1)
	require.Equal(t, wiringapi.DefaultFabric, fabrics[0].Name)
	require.Equal(t, kmetav1.NamespaceDefault, fabrics[0].Namespace)
	require.Equal(t, wiringapi.DefaultFabricSpec(cfg), fabrics[0].Spec)

	for _, upgradeFrom := range []string{"", "26.05"} {
		l := apiutil.NewLoader()
		require.NoError(t, injectUpgradeDefaultFabric(ctx, l, f, upgradeFrom))
		require.Empty(t, listFabrics(t, l), "upgrade from %q", upgradeFrom)
	}
}

// A Fabric/default the wiring already defines is kept as it is.
func TestInjectUpgradeDefaultFabricKeepsDefined(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	f := upgradeTestFab()

	cfg, err := fabric.GetFabricConfig(f)
	require.NoError(t, err)

	spec := wiringapi.DefaultFabricSpec(cfg)
	spec.DisableBFD = !spec.DisableBFD

	l := apiutil.NewLoader()
	require.NoError(t, l.Add(ctx, &wiringapi.Fabric{
		TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindFabric, APIVersion: wiringapi.GroupVersion.String()},
		ObjectMeta: kmetav1.ObjectMeta{Name: wiringapi.DefaultFabric, Namespace: kmetav1.NamespaceDefault},
		Spec:       spec,
	}))

	require.NoError(t, injectUpgradeDefaultFabric(ctx, l, f, "26.03"))

	fabrics := listFabrics(t, l)
	require.Len(t, fabrics, 1)
	require.Equal(t, spec.DisableBFD, fabrics[0].Spec.DisableBFD)
}
