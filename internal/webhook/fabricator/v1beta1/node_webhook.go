// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	fabricatorv1beta1 "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var nodelog = logf.Log.WithName("node-resource")

// SetupNodeWebhookWithManager registers the webhook for Node in the manager.
func SetupNodeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&fabricatorv1beta1.FabNode{}).
		WithValidator(&NodeCustomValidator{}).
		WithDefaulter(&NodeCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-node,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=nodes,verbs=create;update,versions=v1beta1,name=mnode-v1beta1.kb.io,admissionReviewVersions=v1

// NodeCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Node when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type NodeCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &NodeCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Node.
func (d *NodeCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	node, ok := obj.(*fabricatorv1beta1.FabNode)

	if !ok {
		return fmt.Errorf("expected an Node object but got %T", obj)
	}
	nodelog.Info("Defaulting for Node", "name", node.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-node,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=nodes,verbs=create;update,versions=v1beta1,name=vnode-v1beta1.kb.io,admissionReviewVersions=v1

// NodeCustomValidator struct is responsible for validating the Node resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NodeCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &NodeCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Node.
func (v *NodeCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	node, ok := obj.(*fabricatorv1beta1.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a Node object but got %T", obj)
	}
	nodelog.Info("Validation for Node upon creation", "name", node.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Node.
func (v *NodeCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	node, ok := newObj.(*fabricatorv1beta1.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a Node object for the newObj but got %T", newObj)
	}
	nodelog.Info("Validation for Node upon update", "name", node.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Node.
func (v *NodeCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	node, ok := obj.(*fabricatorv1beta1.FabNode)
	if !ok {
		return nil, fmt.Errorf("expected a Node object but got %T", obj)
	}
	nodelog.Info("Validation for Node upon deletion", "name", node.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
