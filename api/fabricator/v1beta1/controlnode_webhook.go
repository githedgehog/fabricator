// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var controlnodelog = logf.Log.WithName("controlnode-resource")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *ControlNode) SetupWebhookWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete(); err != nil {
		return fmt.Errorf("creating webhook: %w", err) //nolint:goerr113
	}

	return nil
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-fabricator-githedgehog-com-v1beta1-controlnode,mutating=true,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=controlnodes,verbs=create;update;delete,versions=v1beta1,name=mcontrolnode.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &ControlNode{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *ControlNode) Default() {
	controlnodelog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-fabricator-githedgehog-com-v1beta1-controlnode,mutating=false,failurePolicy=fail,sideEffects=None,groups=fabricator.githedgehog.com,resources=controlnodes,verbs=create;update;delete,versions=v1beta1,name=vcontrolnode.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &ControlNode{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *ControlNode) ValidateCreate() (admission.Warnings, error) {
	controlnodelog.Info("validate create", "name", r.Name)

	// TODO(user): fill in your validation logic upon object creation.
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *ControlNode) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	controlnodelog.Info("validate update", "name", r.Name)

	_ = old.(*ControlNode)

	// TODO(user): fill in your validation logic upon object update.
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *ControlNode) ValidateDelete() (admission.Warnings, error) {
	controlnodelog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
