// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/f8r"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func DoControlUpgrade(ctx context.Context, workDir string) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()

	rawMarker, err := os.ReadFile(InstallMarkerFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading install marker: %w", err)
	}
	if err == nil {
		marker := strings.TrimSpace(string(rawMarker))
		if marker != InstallMarkerComplete {
			slog.Info("Control node seems to be not installed successfully", "status", marker, "marker", InstallMarkerFile)

			return nil
		}
	} else {
		return fmt.Errorf("install marker file not found: %w", err)
	}

	return (&ControlUpgrade{
		WorkDir: workDir,
	}).Run(ctx)
}

type ControlUpgrade struct {
	WorkDir string
	Fab     fabapi.Fabricator
	Control fabapi.ControlNode
}

func (c *ControlUpgrade) Run(ctx context.Context) error {
	slog.Info("Running control node upgrade")

	kube, err := kubeutil.NewClient(ctx, k3s.KubeConfigPath,
		comp.CoreAPISchemeBuilder, comp.AppsAPISchemeBuilder,
		comp.HelmAPISchemeBuilder, comp.CMApiSchemeBuilder, comp.CMMetaSchemeBuilder,
		wiringapi.SchemeBuilder, vpcapi.SchemeBuilder, fabapi.SchemeBuilder,
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}

	fab, control, err := fab.GetFabAndControls(ctx, kube, false)
	if err != nil {
		return fmt.Errorf("getting fabricator and control nodes: %w", err)
	}

	if len(control) != 1 {
		return fmt.Errorf("expected 1 control node, got %d", len(control)) //nolint:goerr113
	}

	c.Fab = fab
	c.Control = control[0]

	if err := waitKube(ctx, kube, c.Control.Name, "",
		&comp.Node{}, func(obj *comp.Node) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.NodeReady && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for k8s node ready: %w", err)
	}

	c.Fab.Status.IsBootstrap = false
	c.Fab.Status.IsInstall = true

	// TODO read reg user
	regPassword := "password"

	if c.Fab.Spec.Config.Registry.IsAirgap() {
		if err := c.uploadAirgap(ctx, comp.RegistryUserWriter, regPassword); err != nil {
			return fmt.Errorf("uploading airgap artifacts: %w", err)
		}
	}

	if err := c.preCacheZot(ctx); err != nil {
		return fmt.Errorf("pre-caching zot: %w", err)
	}

	if err := c.installFabricator(ctx, kube, false); err != nil {
		return fmt.Errorf("installing fabricator and config: %w", err)
	}

	if err := c.installFabricCtl(ctx); err != nil {
		return fmt.Errorf("installing kubectl-fabric: %w", err)
	}

	slog.Info("Control node upgrade complete")

	return nil
}

func (c *ControlUpgrade) uploadAirgap(ctx context.Context, username, password string) error {
	slog.Info("Uploading airgap artifacts")

	regURL, err := comp.RegistryURL(c.Fab)
	if err != nil {
		return fmt.Errorf("getting registry URL: %w", err)
	}

	airgapArts, err := comp.CollectArtifacts(c.Fab, AirgapArtifactLists...)
	if err != nil {
		return fmt.Errorf("collecting airgap artifacts: %w", err)
	}
	for ref, version := range airgapArts {
		slog.Debug("Uploading airgap artifact", "ref", ref, "version", version)

		if err := artificer.UploadOCIArchive(ctx, c.WorkDir, ref, version, regURL, comp.RegPrefix, username, password); err != nil {
			return fmt.Errorf("uploading airgap artifact %q: %w", ref, err)
		}
	}

	return nil
}

func (c *ControlUpgrade) preCacheZot(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	slog.Info("Pre-caching Zot image")

	repo, err := zot.ImageURL(c.Fab)
	if err != nil {
		return fmt.Errorf("getting zot image URL: %w", err)
	}
	img := repo + ":" + string(zot.Version(c.Fab))

	slog.Debug("Pre-caching", "image", img)

	cmd := exec.CommandContext(ctx, "k3s", "crictl", "pull", img)
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "crictl: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "crictl: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running crictl pull: %w", err)
	}

	return nil
}

func (c *ControlUpgrade) installFabricator(ctx context.Context, kube client.Client, installConfig bool) error {
	slog.Info("Installing fabricator")

	if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, f8r.Install); err != nil {
		return fmt.Errorf("enforcing fabricactor install: %w", err)
	}

	if err := waitKube(ctx, kube, "fabricator-ctrl", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for fabricator-ctrl ready: %w", err)
	}

	if installConfig {
		// TODO only install control node if it's not the first one and we're joining the cluster
		if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, f8r.InstallFabAndControl(c.Control)); err != nil {
			return fmt.Errorf("installing fabricator config and control nodes: %w", err)
		}
	}

	slog.Info("Waiting for fabricator applied")

	if err := waitKube(ctx, kube, comp.FabName, comp.FabNamespace,
		&fabapi.Fabricator{}, func(obj *fabapi.Fabricator) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == fabapi.ConditionApplied && cond.Status == metav1.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for fabricator applied: %w", err)
	}

	slog.Info("Waiting for fabricator ready, may take 2-5 minutes")

	if err := waitKube(ctx, kube, comp.FabName, comp.FabNamespace,
		&fabapi.Fabricator{}, func(obj *fabapi.Fabricator) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == fabapi.ConditionReady && cond.Status == metav1.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for fabricator ready: %w", err)
	}

	return nil
}

func (c *ControlUpgrade) installFabricCtl(_ context.Context) error {
	slog.Info("Installing kubectl-fabric")

	// TODO remove if it'll be managed by control agent?
	if err := copyFile(fabric.CtlBinName, filepath.Join(fabric.BinDir, fabric.CtlDestBinName), 0o755); err != nil {
		return fmt.Errorf("copying fabricctl bin: %w", err)
	}

	return nil
}