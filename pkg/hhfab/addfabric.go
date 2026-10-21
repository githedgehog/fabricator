// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// AddFabric creates the wiring objects that were left out of the install, the way an operator adding them to a running
// deployment would, and waits for the switches to be ready. The objects are already hydrated when the wiring is
// loaded, with values that continue after the ones of the installed objects.
func (c *Config) AddFabric(ctx context.Context, _ *VLAB) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	objs, err := apiutil.ListStaged(ctx, c.Client)
	if err != nil {
		return fmt.Errorf("listing staged objects: %w", err)
	}
	if len(objs) == 0 {
		return fmt.Errorf("no staged objects, generate the wiring with --add-fabric") //nolint:err113
	}

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	slog.Info("Adding fabric", "objects", len(objs))

	for _, obj := range objs {
		obj = obj.DeepCopyObject().(kclient.Object) //nolint:forcetypeassert

		anns := obj.GetAnnotations()
		delete(anns, apiutil.StagedAnnotation)
		obj.SetAnnotations(anns)
		wiringapi.CleanupFabricLabels(obj.GetLabels())
		obj.SetResourceVersion("")
		obj.SetUID("")
		obj.SetManagedFields(nil)

		if err := kube.Create(ctx, obj); err != nil {
			if kapierrors.IsAlreadyExists(err) {
				slog.Info("Already exists", "kind", fmt.Sprintf("%T", obj), "name", obj.GetName())

				continue
			}

			return fmt.Errorf("creating %T %s: %w", obj, obj.GetName(), err)
		}

		slog.Info("Created", "kind", fmt.Sprintf("%T", obj), "name", obj.GetName())
	}

	if err := WaitReady(ctx, kube, WaitReadyOpts{AppliedFor: 15 * time.Second, Timeout: 20 * time.Minute}); err != nil {
		return fmt.Errorf("waiting for ready: %w", err)
	}

	return nil
}
