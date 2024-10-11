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
)

// log is for logging in this package.
var fabricatorlog = logf.Log.WithName("fabricator-resource")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *Fabricator) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete(); err != nil {
		return fmt.Errorf("creating webhook: %w", err) //nolint:goerr113
	}

	return nil
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-fabricator,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=fabricators,verbs=create;update;delete,versions=v1beta1,name=mfabricator.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Fabricator{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
// func (r *Fabricator) Default() {
// 	fabricatorlog.Info("default", "name", r.Name)

// 	// TODO(user): fill in your defaulting logic.
// }

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-fabricator,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=fabricators,verbs=create;update;delete,versions=v1beta1,name=vfabricator.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Fabricator{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Fabricator) ValidateCreate() (admission.Warnings, error) {
	fabricatorlog.Info("validate create", "name", r.Name)

	if err := r.Validate(context.TODO()); err != nil {
		return nil, err
	}

	// TODO(user): fill in your validation logic upon object creation.
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Fabricator) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	fabricatorlog.Info("validate update", "name", r.Name)

	_ = old.(*Fabricator)

	if err := r.Validate(context.TODO()); err != nil {
		return nil, err
	}

	// TODO(user): fill in your validation logic upon object update.
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Fabricator) ValidateDelete() (admission.Warnings, error) {
	fabricatorlog.Info("validate delete", "name", r.Name)

	// TODO doesn't allow deletion

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
