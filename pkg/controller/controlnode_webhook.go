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
	"go.githedgehog.com/fabricator/pkg/fab/comp"
)

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-controlnode,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=controlnodes,verbs=create;update;delete,versions=v1beta1,name=mcontrolnode.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-controlnode,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=controlnodes,verbs=create;update;delete,versions=v1beta1,name=vcontrolnode.kb.io,admissionReviewVersions=v1

type ControlNodeWebhook struct {
	kclient.Reader
}

func SetupControlNodeWebhookWith(mgr kctrl.Manager) error {
	w := &ControlNodeWebhook{
		Reader: mgr.GetClient(),
	}

	if err := kctrl.NewWebhookManagedBy(mgr, &fabapi.ControlNode{}).
		WithDefaulter(w).
		WithValidator(w).
		Complete(); err != nil {
		return fmt.Errorf("creating webhook: %w", err) //nolint:goerr113
	}

	return nil
}

func (w *ControlNodeWebhook) Default(_ context.Context, ctrl *fabapi.ControlNode) error {
	ctrl.Default()

	return nil
}

func (w *ControlNodeWebhook) ValidateCreate(ctx context.Context, ctrl *fabapi.ControlNode) (admission.Warnings, error) {
	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, ctrl.Validate(ctx, &f.Spec.Config, false) //nolint:wrapcheck
}

func (w *ControlNodeWebhook) ValidateUpdate(ctx context.Context, _ *fabapi.ControlNode, ctrl *fabapi.ControlNode) (admission.Warnings, error) {
	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, ctrl.Validate(ctx, &f.Spec.Config, false) //nolint:wrapcheck
}

func (w *ControlNodeWebhook) ValidateDelete(_ context.Context, _ *fabapi.ControlNode) (admission.Warnings, error) {
	return nil, fmt.Errorf("not allowed to delete ControlNode object") //nolint:goerr113
}
