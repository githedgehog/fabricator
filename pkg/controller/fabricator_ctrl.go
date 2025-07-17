// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/controlproxy"
	"go.githedgehog.com/fabricator/pkg/fab/comp/f8r"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/fab/comp/gateway"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/ntp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/reloader"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	"go.githedgehog.com/fabricator/pkg/version"
	coreapi "k8s.io/api/core/v1"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kctrl "sigs.k8s.io/controller-runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fabricator.githedgehog.com,resources=fabricators/finalizers,verbs=update

//+kubebuilder:rbac:groups=dhcp.githedgehog.com,resources=dhcpsubnets,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

// +kubebuilder:rbac:groups=helm.cattle.io,resources=helmcharts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.cattle.io,resources=helmcharts/status,verbs=get

// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates/status,verbs=get

type FabricatorReconciler struct {
	kclient.Client
	status sync.Mutex
}

func SetupFabricatorReconcilerWith(mgr kctrl.Manager) error {
	r := &FabricatorReconciler{
		Client: mgr.GetClient(),
	}

	if err := kctrl.NewControllerManagedBy(mgr).
		Named("Fabricator").
		For(&fabapi.Fabricator{}).
		Complete(r); err != nil {
		return fmt.Errorf("setting up controller: %w", err)
	}

	if err := mgr.Add(r); err != nil {
		return fmt.Errorf("adding status watcher: %w", err)
	}

	return nil
}

func (r *FabricatorReconciler) Reconcile(ctx context.Context, req kctrl.Request) (kctrl.Result, error) {
	l := kctrllog.FromContext(ctx)

	if req.Name != comp.FabName && req.Namespace != comp.FabNamespace {
		l.Info("Ignoring incorrect Fabricator")

		return kctrl.Result{}, nil
	}

	f := &fabapi.Fabricator{}
	if err := r.Get(ctx, req.NamespacedName, f); err != nil {
		return kctrl.Result{}, fmt.Errorf("fetching fabricator: %w", err)
	}
	f.Default()

	l = l.WithValues("gen", f.Generation, "res", f.ResourceVersion)

	if f.Status.Conditions == nil {
		f.Status.Conditions = []kmetav1.Condition{}
	}

	f.Status.IsBootstrap = false
	f.Status.IsInstall = false

	outdated := f.Status.LastAppliedController != version.Version || f.Status.LastAppliedGen != f.Generation
	if outdated || !kmeta.IsStatusConditionTrue(f.Status.Conditions, fabapi.ConditionApplied) {
		l.Info("Reconciling Fabricator")

		// ensuring defaults for the fabricator and controls first

		controls := &fabapi.ControlNodeList{}
		if err := r.List(ctx, controls); err != nil {
			return kctrl.Result{}, fmt.Errorf("listing controls: %w", err)
		}
		if len(controls.Items) == 0 {
			return kctrl.Result{}, fmt.Errorf("no control nodes found") //nolint:goerr113
		}
		if len(controls.Items) > 1 {
			return kctrl.Result{}, fmt.Errorf("multiple control nodes found") //nolint:goerr113
		}
		control := controls.Items[0]
		control.Default()

		// That makes sure that we're updating Fab and ControlNodes with the new defaults
		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, f8r.InstallFabAndControl(control)); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing fabricator and control install defaults: %w", err)
		}

		// TODO do the same for the nodes

		regSecret := coreapi.Secret{}
		if err := r.Get(ctx, kclient.ObjectKey{
			Namespace: comp.FabNamespace,
			Name:      comp.RegistryUserReaderSecret,
		}, &regSecret); err != nil {
			return kctrl.Result{}, fmt.Errorf("getting registry reader user secret: %w", err)
		}

		regPassword, ok := regSecret.Data[comp.BasicAuthPasswordKey]
		if !ok || len(regPassword) == 0 {
			return kctrl.Result{}, errors.New("registry reader user secret missing password") //nolint:goerr113
		}

		// doing the actual reconciliation

		kmeta.SetStatusCondition(&f.Status.Conditions, kmetav1.Condition{
			Type:               fabapi.ConditionApplied,
			Status:             kmetav1.ConditionFalse,
			Reason:             "ApplyPending",
			ObservedGeneration: f.Generation,
			Message:            fmt.Sprintf("Config will be applied, gen=%d", f.Generation),
		})
		kmeta.SetStatusCondition(&f.Status.Conditions, kmetav1.Condition{
			Type:               fabapi.ConditionReady,
			Status:             kmetav1.ConditionFalse,
			Reason:             "ApplyPending",
			ObservedGeneration: f.Generation,
			Message:            "Config will be applied",
		})
		f.Status.LastAttemptGen = f.Generation
		f.Status.LastAttemptTime = kmetav1.Time{Time: time.Now()}

		if err := r.Status().Update(ctx, f); err != nil {
			return kctrl.Result{}, fmt.Errorf("updating attempt status: %w", err)
		}

		if err := f.CalculateVersions(fab.Versions); err != nil {
			return kctrl.Result{}, fmt.Errorf("calculating versions: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, reloader.Install); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing reloader install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, certmanager.Install); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing cert-manager install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, zot.Install); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing zot install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, fabric.Install(control)); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing fabric install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, fabric.InstallManagementDHCPSubnet); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing fabric management dhcp subnet install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, ntp.Install); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing ntp install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, controlproxy.Install); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing controlproxy install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f,
			k3s.InstallNodeRegistries(comp.RegistryUserReader, string(regPassword)),
		); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing k3s node registries install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, f8r.InstallNodeConfig); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing node config install: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, gateway.Install); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing gateway install: %w", err)
		}

		// Should be probably always updated last
		if err := comp.EnforceKubeInstall(ctx, r.Client, *f, f8r.Install); err != nil {
			return kctrl.Result{}, fmt.Errorf("enforcing fabricactor install: %w", err)
		}

		// TODO: reconcile all components and collect status

		kmeta.SetStatusCondition(&f.Status.Conditions, kmetav1.Condition{
			Type:               fabapi.ConditionApplied,
			Status:             kmetav1.ConditionTrue,
			Reason:             "ApplySucceeded",
			ObservedGeneration: f.Generation,
			Message:            fmt.Sprintf("Config applied, gen=%d", f.Generation),
		})
		f.Status.LastAppliedGen = f.Generation
		f.Status.LastAppliedTime = kmetav1.Time{Time: time.Now()}
		f.Status.LastAppliedController = version.Version

		if err := r.Status().Update(ctx, f); err != nil {
			return kctrl.Result{}, fmt.Errorf("updating applied status: %w", err)
		}

		l.Info("Reconciled Fabricator")
	}

	return kctrl.Result{}, r.statusCheck(ctx, l, f)
}

func (r *FabricatorReconciler) statusCheck(ctx context.Context, l logr.Logger, f *fabapi.Fabricator) error {
	if time.Since(f.Status.LastStatusCheck.Time) < 1*time.Minute && kmeta.IsStatusConditionTrue(f.Status.Conditions, fabapi.ConditionReady) {
		return nil
	}

	r.status.Lock()
	defer r.status.Unlock()

	l.Info("Checking for components status")

	var err error

	f.Status.Components.FabricatorAPI, err = f8r.StatusAPI(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting fabricator api status: %w", err)
	}

	f.Status.Components.FabricatorCtrl, err = f8r.StatusCtrl(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting fabricator ctrl status: %w", err)
	}

	f.Status.Components.FabricatorNodeConfig, err = f8r.StatusNodeConfig(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting fabricator node config status: %w", err)
	}

	f.Status.Components.CertManagerCtrl, err = certmanager.StatusCtrl(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting cert-manager ctrl status: %w", err)
	}

	f.Status.Components.CertManagerWebhook, err = certmanager.StatusWebhook(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting cert-manager webhook status: %w", err)
	}

	f.Status.Components.Reloader, err = reloader.Status(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting reloader status: %w", err)
	}

	f.Status.Components.Zot, err = zot.Status(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting zot status: %w", err)
	}

	f.Status.Components.NTP, err = ntp.Status(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting ntp status: %w", err)
	}

	f.Status.Components.FabricAPI, err = fabric.StatusAPI(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting fabric api status: %w", err)
	}

	f.Status.Components.FabricCtrl, err = fabric.StatusCtrl(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting fabric ctrl status: %w", err)
	}

	f.Status.Components.FabricBoot, err = fabric.StatusBoot(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting fabric boot status: %w", err)
	}

	f.Status.Components.FabricDHCP, err = fabric.StatusDHCP(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting fabric dhcp status: %w", err)
	}

	f.Status.Components.ControlProxy, err = controlproxy.Status(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting controlproxy status: %w", err)
	}

	f.Status.Components.GatewayAPI, err = gateway.StatusAPI(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting gateway api status: %w", err)
	}

	f.Status.Components.GatewayCtrl, err = gateway.StatusCtrl(ctx, r.Client, *f)
	if err != nil {
		return fmt.Errorf("getting gateway ctrl status: %w", err)
	}

	if f.Status.Components.IsReady(*f) {
		if !kmeta.IsStatusConditionTrue(f.Status.Conditions, fabapi.ConditionReady) {
			l.Info("All components are ready now")
		}

		kmeta.SetStatusCondition(&f.Status.Conditions, kmetav1.Condition{
			Type:               fabapi.ConditionReady,
			Status:             kmetav1.ConditionTrue,
			Reason:             "ComponentsReady",
			ObservedGeneration: f.Generation,
			Message:            "All components are ready",
		})

		f.Status.LastStatusCheck = kmetav1.Time{Time: time.Now()}

		if err := r.Status().Update(ctx, f); err != nil {
			return fmt.Errorf("updating ready status: %w", err)
		}

		return nil
	} else { //nolint:revive
		if kmeta.IsStatusConditionTrue(f.Status.Conditions, fabapi.ConditionReady) {
			l.Info("Some components are not ready now")
		}

		kmeta.SetStatusCondition(&f.Status.Conditions, kmetav1.Condition{
			Type:               fabapi.ConditionReady,
			Status:             kmetav1.ConditionFalse,
			Reason:             "ComponentsPending",
			ObservedGeneration: f.Generation,
			Message:            "Not all components are ready",
		})

		f.Status.LastStatusCheck = kmetav1.Time{Time: time.Now()}

		if err := r.Status().Update(ctx, f); err != nil {
			return fmt.Errorf("updating not ready status: %w", err)
		}

		return nil
	}
}

// Only one status watcher is needed
func (r *FabricatorReconciler) NeedLeaderElection() bool {
	return true
}

// Status watcher
func (r *FabricatorReconciler) Start(ctx context.Context) error {
	l := kctrllog.FromContext(ctx, "runner", "StatusWatcher")

	for {
		select {
		case <-ctx.Done():
			l.Info("Context done")

			return nil
		case <-time.After(10 * time.Second):
			f := &fabapi.Fabricator{}
			if err := r.Get(ctx, kclient.ObjectKey{Name: comp.FabName, Namespace: comp.FabNamespace}, f); err != nil {
				l.Error(err, "Fetching fabricator")

				continue
			}

			if err := r.statusCheck(ctx, l, f); err != nil {
				l.Error(err, "Checking status")

				continue
			}
		}
	}
}
