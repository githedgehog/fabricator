// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/logr"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/controller"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/version"
	// +kubebuilder:scaffold:imports
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiext.AddToScheme(scheme))

	utilruntime.Must(comp.HelmAPISchemeBuilder.AddToScheme(scheme))
	utilruntime.Must(comp.CMApiSchemeBuilder.AddToScheme(scheme))
	utilruntime.Must(comp.CMMetaSchemeBuilder.AddToScheme(scheme))

	utilruntime.Must(dhcpapi.AddToScheme(scheme))

	utilruntime.Must(fabapi.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	// TODO make it configurable
	logLevel := slog.LevelDebug

	logW := os.Stderr
	handler := tint.NewHandler(logW, &tint.Options{
		Level:      logLevel,
		TimeFormat: time.StampMilli,
		NoColor:    !isatty.IsTerminal(logW.Fd()),
	})

	logger := slog.New(handler)
	slog.SetDefault(logger)
	ctrl.SetLogger(logr.FromSlogHandler(handler))
	klog.SetSlogLogger(logger)

	if err := run(); err != nil {
		slog.Error("Failed to run", "error", err)
		os.Exit(1)
	}
}

func run() error {
	slog.Info("Starting fabricator-ctrl", "version", version.Version)

	// TODO: disable http2: https://github.com/advisories/GHSA-qppj-fm5r-hxr3 and https://github.com/advisories/GHSA-4374-p667-p6c8

	// TODO: enable secure metrics
	// FilterProvider is used to protect the metrics endpoint with authn/authz.
	// These configurations ensure that only authorized users and service accounts
	// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
	// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
	// metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: ":8080",
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port: 9443,
		}),
		HealthProbeBindAddress: ":8081",
		LeaderElection:         true,
		LeaderElectionID:       "fabricator.githedgehog.com",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	if err := controller.SetupFabricatorReconcilerWith(mgr); err != nil {
		return fmt.Errorf("setting up fabricator controller: %w", err)
	}
	if err := controller.SetupFabricatorWebhookWith(mgr); err != nil {
		return fmt.Errorf("setting up fabricator webhook: %w", err)
	}

	if err := controller.SetupControlNodeWebhookWith(mgr); err != nil {
		return fmt.Errorf("setting up controlnode webhook: %w", err)
	}

	if err := controller.SetupNodeWebhookWith(mgr); err != nil {
		return fmt.Errorf("setting up node webhook: %w", err)
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up ready check: %w", err)
	}

	slog.Info("Starting manager")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}

	return nil
}
