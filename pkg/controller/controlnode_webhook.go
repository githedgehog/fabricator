// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-controlnode,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=controlnodes,verbs=create;update;delete,versions=v1beta1,name=mcontrolnode.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-controlnode,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=controlnodes,verbs=create;update;delete,versions=v1beta1,name=vcontrolnode.kb.io,admissionReviewVersions=v1

type ControlNodeWebhook struct {
	client.Reader
}

func SetupControlNodeWebhookWith(mgr ctrl.Manager) error {
	w := &ControlNodeWebhook{
		Reader: mgr.GetClient(),
	}

	if err := ctrl.NewWebhookManagedBy(mgr).
		For(&fabapi.ControlNode{}).
		WithDefaulter(w).
		WithValidator(w).
		Complete(); err != nil {
		return fmt.Errorf("creating webhook: %w", err) //nolint:goerr113
	}

	return nil
}

func (w *ControlNodeWebhook) Default(_ context.Context, obj runtime.Object) error {
	c, ok := obj.(*fabapi.ControlNode)
	if !ok {
		return fmt.Errorf("expected a ControlNode object but got %T", obj) //nolint:goerr113
	}

	c.Default()

	return nil
}

func (w *ControlNodeWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	c, ok := obj.(*fabapi.ControlNode)
	if !ok {
		return nil, fmt.Errorf("expected a ControlNode object but got %T", obj) //nolint:goerr113
	}

	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, client.ObjectKey{}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, c.Validate(ctx, &f.Spec.Config, false) //nolint:wrapcheck
}

func (w *ControlNodeWebhook) ValidateUpdate(ctx context.Context, oldObj runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	_, ok := oldObj.(*fabapi.ControlNode)
	if !ok {
		return nil, fmt.Errorf("expected a ControlNode old object but got %T", oldObj) //nolint:goerr113
	}

	c, ok := newObj.(*fabapi.ControlNode)
	if !ok {
		return nil, fmt.Errorf("expected a ControlNode new object but got %T", newObj) //nolint:goerr113
	}

	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, client.ObjectKey{}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, c.Validate(ctx, &f.Spec.Config, false) //nolint:wrapcheck
}

func (w *ControlNodeWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, fmt.Errorf("not allowed to delete ControlNode object") //nolint:goerr113
}
