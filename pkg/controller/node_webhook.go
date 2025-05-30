// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	kctrl "sigs.k8s.io/controller-runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
)

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-node,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=nodes,verbs=create;update;delete,versions=v1beta1,name=mnode.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-node,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=nodes,verbs=create;update;delete,versions=v1beta1,name=vnode.kb.io,admissionReviewVersions=v1

type NodeWebhook struct {
	kclient.Reader
}

func SetupNodeWebhookWith(mgr kctrl.Manager) error {
	w := &NodeWebhook{
		Reader: mgr.GetClient(),
	}

	if err := kctrl.NewWebhookManagedBy(mgr).
		For(&fabapi.FabNode{}).
		WithDefaulter(w).
		WithValidator(w).
		Complete(); err != nil {
		return fmt.Errorf("creating webhook: %w", err) //nolint:goerr113
	}

	return nil
}

func (w *NodeWebhook) Default(_ context.Context, obj runtime.Object) error {
	n, ok := obj.(*fabapi.FabNode)
	if !ok {
		return fmt.Errorf("expected a Node object but got %T", obj) //nolint:goerr113
	}

	n.Default()

	return nil
}

func (w *NodeWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	n, ok := obj.(*fabapi.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a Node object but got %T", obj) //nolint:goerr113
	}

	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, n.Validate(ctx, &f.Spec.Config, false) //nolint:wrapcheck
}

func (w *NodeWebhook) ValidateUpdate(ctx context.Context, oldObj runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	_, ok := oldObj.(*fabapi.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a Node old object but got %T", oldObj) //nolint
	}

	c, ok := newObj.(*fabapi.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a Node new object but got %T", newObj) //nolint:goerr113
	}

	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, c.Validate(ctx, &f.Spec.Config, false) //nolint:wrapcheck
}

func (w *NodeWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, fmt.Errorf("not allowed to delete Node object") //nolint:goerr113
}
