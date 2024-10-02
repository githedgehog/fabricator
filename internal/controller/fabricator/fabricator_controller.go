// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fabricator

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fabricatorv1beta1 "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

// FabricatorReconciler reconciles a Fabricator object
type FabricatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Fabricator object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *FabricatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// TODO(user): your logic here

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FabricatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fabricatorv1beta1.Fabricator{}).
		Complete(r)
}
