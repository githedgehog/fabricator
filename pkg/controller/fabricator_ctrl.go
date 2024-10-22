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
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/f8s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/fab/comp/ntp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/reloader"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
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
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=helm.cattle.io,resources=helmcharts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.cattle.io,resources=helmcharts/status,verbs=get

// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates/status,verbs=get

func (r *FabricatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	if req.Name != comp.FabName && req.Namespace != comp.FabNamespace {
		l.Info("Ignoring Fabricator")

		return ctrl.Result{}, nil
	}

	f := &fabapi.Fabricator{}
	if err := r.Get(ctx, req.NamespacedName, f); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching fabricator: %w", err)
	}

	l = l.WithValues("gen", f.Generation, "res", f.ResourceVersion)

	if f.Status.Conditions == nil {
		f.Status.Conditions = []metav1.Condition{}
	}

	f.Status.IsBootstrap = false
	f.Status.IsInstall = false

	outdated := f.Status.LastAppliedController != version.Version || f.Status.LastAppliedGen != f.Generation
	if outdated || !apimeta.IsStatusConditionTrue(f.Status.Conditions, fabapi.ConditionApplied) {
		l.Info("Reconciling Fabricator")

		apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
			Type:               fabapi.ConditionApplied,
			Status:             metav1.ConditionFalse,
			Reason:             "ApplyPending",
			ObservedGeneration: f.Generation,
			Message:            fmt.Sprintf("Config will be applied, gen=%d", f.Generation),
		})
		apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
			Type:               fabapi.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             "ApplyPending",
			ObservedGeneration: f.Generation,
			Message:            "Config will be applied",
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

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, reloader.Install); err != nil {
			return ctrl.Result{}, fmt.Errorf("enforcing reloader install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, certmanager.Install); err != nil {
			return ctrl.Result{}, fmt.Errorf("enforcing cert-manager install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, zot.Install); err != nil {
			return ctrl.Result{}, fmt.Errorf("enforcing zot install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, fabric.Install(control)); err != nil {
			return ctrl.Result{}, fmt.Errorf("enforcing fabric install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, ntp.Install); err != nil {
			return ctrl.Result{}, fmt.Errorf("enforcing ntp install: %w", err)
		}

		// Should be probably always updated last
		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, f8s.Install); err != nil {
			return ctrl.Result{}, fmt.Errorf("enforcing fabricactor install: %w", err)
		}

		// TODO: reconcile all components and collect status

		apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
			Type:               fabapi.ConditionApplied,
			Status:             metav1.ConditionTrue,
			Reason:             "ApplySucceeded",
			ObservedGeneration: f.Generation,
			Message:            fmt.Sprintf("Config applied, gen=%d", f.Generation),
		})
		f.Status.LastAppliedGen = f.Generation
		f.Status.LastAppliedTime = metav1.Time{Time: time.Now()}
		f.Status.LastAppliedController = version.Version

		if err := r.Status().Update(ctx, f); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating applied status: %w", err)
		}

		l.Info("Reconciled Fabricator")
	} else {
		l.Info("Fabricator already reconciled")
	}

	if !apimeta.IsStatusConditionTrue(f.Status.Conditions, fabapi.ConditionReady) {
		l.Info("Checking for components status")

		var err error

		f.Status.Components.CertManagerCtrl, err = comp.GetKubeStatus(ctx, r.Client, *f, certmanager.StatusCtrl)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting cert-manager ctrl status: %w", err)
		}

		f.Status.Components.CertManagerWebhook, err = comp.GetKubeStatus(ctx, r.Client, *f, certmanager.StatusWebhook)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting cert-manager webhook status: %w", err)
		}

		f.Status.Components.Reloader, err = comp.GetKubeStatus(ctx, r.Client, *f, reloader.Status)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting reloader status: %w", err)
		}

		f.Status.Components.Zot, err = comp.GetKubeStatus(ctx, r.Client, *f, zot.Status)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting zot status: %w", err)
		}

		f.Status.Components.NTP, err = comp.GetKubeStatus(ctx, r.Client, *f, ntp.Status)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting ntp status: %w", err)
		}

		f.Status.Components.FabricCtrl, err = comp.GetKubeStatus(ctx, r.Client, *f, fabric.StatusCtrl)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting fabric ctrl status: %w", err)
		}

		f.Status.Components.FabricBoot, err = comp.GetKubeStatus(ctx, r.Client, *f, fabric.StatusBoot)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting fabric boot status: %w", err)
		}

		f.Status.Components.FabricDHCP, err = comp.GetKubeStatus(ctx, r.Client, *f, fabric.StatusDHCP)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting fabric dhcp status: %w", err)
		}

		f.Status.Components.FabricProxy, err = comp.GetKubeStatus(ctx, r.Client, *f, fabric.StatusProxy)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting fabric proxy status: %w", err)
		}

		if f.Status.Components.IsReady() {
			l.Info("Fabricator is ready")

			apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
				Type:               fabapi.ConditionReady,
				Status:             metav1.ConditionTrue,
				Reason:             "ComponentsReady",
				ObservedGeneration: f.Generation,
				Message:            "All components are ready",
			})

			if err := r.Status().Update(ctx, f); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating ready status: %w", err)
			}

			return ctrl.Result{}, nil
		} else { //nolint:revive
			l.Info("Fabricator is not ready")

			apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
				Type:               fabapi.ConditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             "ComponentsPending",
				ObservedGeneration: f.Generation,
				Message:            "Not all components are ready",
			})

			if err := r.Status().Update(ctx, f); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating not ready status: %w", err)
			}

			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}
