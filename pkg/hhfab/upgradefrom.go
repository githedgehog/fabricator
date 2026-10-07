// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// UpgradeFromEnv names the release a work dir is being upgraded from, e.g.
// 26.04 or 26.04.1. Wiring written for a release from before Fabric objects
// has no Fabric/default, which validation requires and which the controller
// creates in the cluster on upgrade. For those releases hhfab injects it when
// loading the wiring, so the rest of the upgrade sees what the cluster will.
//
// Keyed on the release rather than on the wiring lacking a Fabric, so that it
// stops applying by itself once those releases are no longer upgraded from.
//
// TODO: remove once upgrades from 26.03 and 26.04 are no longer supported.
const UpgradeFromEnv = "HHFAB_UPGRADE_FROM"

// upgradeFromWithoutFabrics lists, as major.minor, the releases whose wiring
// predates Fabric objects.
var upgradeFromWithoutFabrics = []string{"26.03", "26.04"}

// upgradeFromWithoutFabric reports whether a release predates Fabric objects,
// ignoring a leading "v" and the patch version.
func upgradeFromWithoutFabric(release string) bool {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(release), "v"), ".", 3)
	if len(parts) < 2 {
		return false
	}

	return slices.Contains(upgradeFromWithoutFabrics, parts[0]+"."+parts[1])
}

// injectUpgradeDefaultFabric adds Fabric/default, with the spec the controller
// seeds it with from the fabric config, to wiring that does not define it, when
// upgrading from a release from before Fabric objects.
func injectUpgradeDefaultFabric(ctx context.Context, l *apiutil.Loader, f fabapi.Fabricator, upgradeFrom string) error {
	if !upgradeFromWithoutFabric(upgradeFrom) {
		return nil
	}

	key := kclient.ObjectKey{Name: wiringapi.DefaultFabric, Namespace: kmetav1.NamespaceDefault}
	if err := l.GetClient().Get(ctx, key, &wiringapi.Fabric{}); err == nil {
		return nil
	} else if !kapierrors.IsNotFound(err) {
		return fmt.Errorf("checking for the default fabric: %w", err)
	}

	cfg, err := fabric.GetFabricConfig(f)
	if err != nil {
		return fmt.Errorf("getting fabric config: %w", err)
	}

	defaultFabric := &wiringapi.Fabric{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       wiringapi.KindFabric,
			APIVersion: wiringapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      wiringapi.DefaultFabric,
			Namespace: kmetav1.NamespaceDefault,
		},
		Spec: wiringapi.DefaultFabricSpec(cfg),
	}
	defaultFabric.Default()

	slog.Warn("Injecting the default Fabric into the wiring for the upgrade", "from", upgradeFrom, "env", UpgradeFromEnv)

	if err := l.Add(ctx, defaultFabric); err != nil {
		return fmt.Errorf("injecting the default fabric: %w", err)
	}

	return nil
}
