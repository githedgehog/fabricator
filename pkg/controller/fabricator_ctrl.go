// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/version"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FabricatorReconciler reconciles a Fabricator object
type FabricatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *FabricatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("Fabricator").
		For(&fabapi.Fabricator{}).
		Complete(r); err != nil {
		return fmt.Errorf("setting up controller: %w", err)
	}

	return nil
}

// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators/finalizers,verbs=update

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=helm.cattle.io,resources=helmcharts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.cattle.io,resources=helmcharts/status,verbs=get

func (r *FabricatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	if req.Name != comp.FabName && req.Namespace != comp.FabNamespace {
		l.Info("Ignoring Fabricator")

		return ctrl.Result{}, nil
	}

	f := &fabapi.Fabricator{}
	if err := r.Get(ctx, req.NamespacedName, f); err != nil {
		l.Info("Fabricator already reconciled", "gen", f.Generation, "res", f.ResourceVersion)

		return ctrl.Result{}, fmt.Errorf("fetching Fabricator: %w", err)
	}

	if f.Status.LastAppliedController == version.Version && f.Status.LastAppliedGen == f.Generation {
		return ctrl.Result{}, nil
	}

	l.Info("Reconciling Fabricator", "gen", f.Generation, "res", f.ResourceVersion)

	if f.Status.Conditions == nil {
		f.Status.Conditions = []metav1.Condition{}
	}

	f.Status.IsBootstrap = false

	apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
		Type:               fabapi.ConditionApplied,
		Status:             metav1.ConditionFalse,
		Reason:             "ApplyPending",
		ObservedGeneration: f.Generation,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Message:            fmt.Sprintf("Config will be applied, gen=%d", f.Generation),
	})
	f.Status.LastAttemptGen = f.Generation
	f.Status.LastAttemptTime = metav1.Time{Time: time.Now()}

	if err := r.Status().Update(ctx, f); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating attempt status: %w", err)
	}

	if err := f.CalculateVersions(fab.Versions); err != nil {
		return ctrl.Result{}, fmt.Errorf("calculating versions: %w", err)
	}

	controls := &fabapi.ControlNodeList{}
	if err := r.List(ctx, controls); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing controls: %w", err)
	}
	if len(controls.Items) == 0 {
		return ctrl.Result{}, fmt.Errorf("no control nodes found") //nolint:goerr113
	}
	if len(controls.Items) > 1 {
		return ctrl.Result{}, fmt.Errorf("multiple control nodes found") //nolint:goerr113
	}
	control := controls.Items[0]

	if err := comp.EnforceKubeInstall(ctx, r.Client, *f, fabric.Install(control)); err != nil {
		return ctrl.Result{}, fmt.Errorf("enforcing fabric install: %w", err)
	}

	// TODO: reconcile all components

	apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
		Type:               fabapi.ConditionApplied,
		Status:             metav1.ConditionTrue,
		Reason:             "ApplySucceeded",
		ObservedGeneration: f.Generation,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Message:            fmt.Sprintf("Config applied, gen=%d", f.Generation),
	})
	f.Status.LastAppliedGen = f.Generation
	f.Status.LastAppliedTime = metav1.Time{Time: time.Now()}
	f.Status.LastAppliedController = version.Version

	if err := r.Status().Update(ctx, f); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating applied status: %w", err)
	}

	l.Info("Reconciled Fabricator", "gen", f.Generation, "res", f.ResourceVersion)

	return ctrl.Result{}, nil
}