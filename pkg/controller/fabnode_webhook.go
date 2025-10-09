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

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-fabnode,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=fabnodes,verbs=create;update;delete,versions=v1beta1,name=mnode.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-fabnode,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=fabnodes,verbs=create;update;delete,versions=v1beta1,name=vnode.kb.io,admissionReviewVersions=v1

type FabNodeWebhook struct {
	kclient.Reader
}

func SetupNodeWebhookWith(mgr kctrl.Manager) error {
	w := &FabNodeWebhook{
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

func (w *FabNodeWebhook) Default(_ context.Context, obj runtime.Object) error {
	n, ok := obj.(*fabapi.FabNode)
	if !ok {
		return fmt.Errorf("expected a FabNode object but got %T", obj) //nolint:goerr113
	}

	n.Default()

	return nil
}

func (w *FabNodeWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	n, ok := obj.(*fabapi.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a FabNode object but got %T", obj) //nolint:goerr113
	}

	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, n.Validate(ctx, &f.Spec.Config, false, w.Reader) //nolint:wrapcheck
}

func (w *FabNodeWebhook) ValidateUpdate(ctx context.Context, oldObj runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	_, ok := oldObj.(*fabapi.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a FabNode old object but got %T", oldObj) //nolint
	}

	c, ok := newObj.(*fabapi.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a FabNode new object but got %T", newObj) //nolint:goerr113
	}

	f := &fabapi.Fabricator{}
	if err := w.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: comp.FabName}, f); err != nil {
		return nil, fmt.Errorf("failed to get Fabricator object: %w", err)
	}

	return nil, c.Validate(ctx, &f.Spec.Config, false, w.Reader) //nolint:wrapcheck
}

func (w *FabNodeWebhook) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	_, ok := obj.(*fabapi.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a FabNode object but got %T", obj) //nolint:goerr113
	}

	// TODO check when ControlNode is migrated to FabNode
	// if slices.Contains(n.Spec.Roles, fabapi.NodeRoleControl) {
	// 	return nil, fmt.Errorf("not allowed to delete control node") //nolint:goerr113
	// }

	return nil, nil
}
