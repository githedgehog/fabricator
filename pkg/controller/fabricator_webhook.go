// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	kctrl "sigs.k8s.io/controller-runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-fabricator,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=fabricators,verbs=create;update;delete,versions=v1beta1,name=mfabricator.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-fabricator,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=fabricators,verbs=create;update;delete,versions=v1beta1,name=vfabricator.kb.io,admissionReviewVersions=v1

type FabricatorWebhook struct {
	kclient.Reader
}

func SetupFabricatorWebhookWith(mgr kctrl.Manager) error {
	w := &FabricatorWebhook{
		Reader: mgr.GetClient(),
	}

	if err := kctrl.NewWebhookManagedBy(mgr, &fabapi.Fabricator{}).
		WithDefaulter(w).
		WithValidator(w).
		Complete(); err != nil {
		return fmt.Errorf("creating webhook: %w", err) //nolint:goerr113
	}

	return nil
}

func (w *FabricatorWebhook) Default(_ context.Context, f *fabapi.Fabricator) error {
	f.Default()

	return nil
}

func (w *FabricatorWebhook) ValidateCreate(ctx context.Context, f *fabapi.Fabricator) (admission.Warnings, error) {
	return nil, f.Validate(ctx) //nolint:wrapcheck
}

func (w *FabricatorWebhook) ValidateUpdate(ctx context.Context, _ *fabapi.Fabricator, f *fabapi.Fabricator) (admission.Warnings, error) {
	return nil, f.Validate(ctx) //nolint:wrapcheck
}

func (w *FabricatorWebhook) ValidateDelete(_ context.Context, _ *fabapi.Fabricator) (admission.Warnings, error) {
	return nil, fmt.Errorf("not allowed to delete Fabricator object") //nolint:goerr113
}
