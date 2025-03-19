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
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k9s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	coreapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ControlUpgrade struct {
	WorkDir string
	Yes     bool
	Fab     fabapi.Fabricator
	Control fabapi.ControlNode
	Nodes   []fabapi.Node
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

	backoff := wait.Backoff{
		Steps:    17,
		Duration: 500 * time.Millisecond,
		Factor:   1.5,
		Jitter:   0.1,
	}

	if err := retry.OnError(backoff, func(error) bool {
		return true
	}, func() error {
		f, control, _, err := fab.GetFabAndNodes(ctx, kube)
		if err != nil {
			return fmt.Errorf("getting fabricator and control nodes: %w", err)
		}

		if len(control) != 1 {
			return fmt.Errorf("expected 1 control node, got %d", len(control)) //nolint:goerr113
		}

		c.Fab = f
		c.Control = control[0]

		return nil
	}); err != nil {
		return fmt.Errorf("retrying getting fabricator and control nodes: %w", err)
	}

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

	if err := setupTimesync(ctx, c.Fab.Spec.Config.Control.VIP); err != nil {
		return fmt.Errorf("setting up timesync: %w", err)
	}

	if err := c.waitRegistry(ctx, kube); err != nil {
		return fmt.Errorf("waiting for registry: %w", err)
	}

	regSecret := coreapi.Secret{}
	if err := kube.Get(ctx, client.ObjectKey{
		Namespace: comp.FabNamespace,
		Name:      comp.RegistryUserSecretPrefix + comp.RegistryUserWriter,
	}, &regSecret); err != nil {
		return fmt.Errorf("getting registry user secret: %w", err)
	}

	regPassword, ok := regSecret.Data[comp.BasicAuthPasswordKey]
	if !ok {
		return errors.New("registry user secret missing password") //nolint:goerr113
	}

	if c.Fab.Spec.Config.Registry.IsAirgap() {
		if err := c.uploadAirgap(ctx, comp.RegistryUserWriter, string(regPassword)); err != nil {
			return fmt.Errorf("uploading airgap artifacts: %w", err)
		}
	}

	if err := c.preCacheZot(ctx); err != nil {
		return fmt.Errorf("pre-caching zot: %w", err)
	}

	if err := c.upgradeK8s(ctx, kube); err != nil {
		return fmt.Errorf("upgrading K8s: %w", err)
	}

	if err := c.installFabricator(ctx, kube, false); err != nil {
		return fmt.Errorf("installing fabricator and config: %w", err)
	}

	if err := c.installFabricCtl(ctx); err != nil {
		return fmt.Errorf("installing kubectl-fabric: %w", err)
	}

	if err := copyFile(k9s.BinName, filepath.Join(k3s.BinDir, k9s.BinName), 0o755); err != nil {
		return fmt.Errorf("copying k9s bin: %w", err)
	}

	if err := c.upgradeK8s(ctx, kube); err != nil {
		return fmt.Errorf("upgrading K8s: %w", err)
	}

	if err := upgradeFlatcar(ctx, string(flatcar.Version(c.Fab)), c.Yes); err != nil {
		return fmt.Errorf("upgrading Flatcar: %w", err)
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

	backoff := wait.Backoff{
		Steps:    17,
		Duration: 500 * time.Millisecond,
		Factor:   1.5,
		Jitter:   0.1,
	}

	for ref, version := range airgapArts {
		slog.Debug("Uploading airgap artifact", "ref", ref, "version", version)

		attempt := 0
		if err := retry.OnError(backoff, func(error) bool {
			return true
		}, func() error {
			if attempt > 0 {
				slog.Debug("Retrying uploading airgap artifact", "name", ref, "version", version, "attempt", attempt)
			}

			attempt++

			if err := artificer.UploadOCIArchive(ctx, c.WorkDir, ref, version, regURL, comp.RegPrefix, username, password); err != nil {
				return fmt.Errorf("uploading airgap artifact %q: %w", ref, err)
			}

			return nil
		}); err != nil {
			return fmt.Errorf("retrying uploading airgap artifact %q: %w", ref, err)
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

	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("waiting to retry running crictl: %w", ctx.Err())
			case <-time.After(15 * time.Second):
			}
		}

		cmd := exec.CommandContext(ctx, "k3s", "crictl", "pull", img)
		cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "crictl: ")
		cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "crictl: ")

		if err := cmd.Run(); err != nil {
			slog.Debug("Failed to pre-cache Zot image", "image", img, "attempt", attempt, "err", err)

			if attempt >= 10 {
				return fmt.Errorf("running crictl pull: %w", err)
			}

			continue
		}

		break
	}

	return nil
}

func (c *ControlUpgrade) installFabricator(ctx context.Context, kube client.Client, installConfig bool) error {
	slog.Info("Installing fabricator")

	if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, f8r.Install); err != nil {
		return fmt.Errorf("enforcing fabricactor install: %w", err)
	}

	repo, err := comp.ImageURL(c.Fab, f8r.CtrlRef)
	if err != nil {
		return fmt.Errorf("getting image URL for %q: %w", f8r.CtrlRef, err)
	}
	image := repo + ":" + string(c.Fab.Status.Versions.Fabricator.Controller)

	slog.Debug("Expected fabricator-ctrl", "image", image)

	if err := waitKube(ctx, kube, "fabricator-ctrl", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, c := range obj.Spec.Template.Spec.Containers {
				if c.Image != image {
					return false, nil
				}
			}

			if obj.Status.UpdatedReplicas == 0 {
				return false, nil
			}

			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for fabricator-ctrl ready: %w", err)
	}

	// TODO remove if it'll be managed by control agent?
	if err := copyFile(f8r.CtlBinName, filepath.Join(f8r.BinDir, f8r.CtlDestBinName), 0o755); err != nil {
		return fmt.Errorf("copying hhfabctl bin: %w", err)
	}

	if installConfig {
		// TODO only install control node if it's not the first one and we're joining the cluster
		if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, f8r.InstallFabAndControl(c.Control)); err != nil {
			return fmt.Errorf("installing fabricator config and control nodes: %w", err)
		}

		if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, f8r.InstallNodes(c.Nodes)); err != nil {
			return fmt.Errorf("installing fabricator nodes: %w", err)
		}
	}

	slog.Info("Waiting for fabricator applied")

	version := string(c.Fab.Status.Versions.Fabricator.Controller)
	if err := waitKube(ctx, kube, comp.FabName, comp.FabNamespace,
		&fabapi.Fabricator{}, func(obj *fabapi.Fabricator) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if obj.Status.LastAppliedController != version {
					return false, nil
				}
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
				if obj.Status.LastAppliedController != version {
					return false, nil
				}
				if time.Since(obj.Status.LastStatusCheck.Time) > 2*time.Minute {
					return false, nil
				}
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

func (c *ControlUpgrade) upgradeK8s(ctx context.Context, kube client.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()

	node := &comp.Node{}
	if err := kube.Get(ctx, client.ObjectKey{
		Name: c.Control.Name,
	}, node); err != nil {
		return fmt.Errorf("getting control node: %w", err)
	}

	actual := node.Status.NodeInfo.KubeletVersion
	desired := k3s.KubeVersion(c.Fab)
	if actual == desired {
		slog.Info("System already running desired K8s version", "version", desired)

		return nil
	}

	slog.Info("Upgrading K8s", "from", actual, "to", desired)

	if err := copyFile(k3s.BinName, filepath.Join(k3s.BinDir, k3s.BinName), 0o755); err != nil {
		return fmt.Errorf("copying k3s bin: %w", err)
	}

	if err := os.MkdirAll(k3s.ImagesDir, 0o755); err != nil {
		return fmt.Errorf("creating k3s images dir %q: %w", k3s.ImagesDir, err)
	}

	if err := copyFile(k3s.AirgapName, filepath.Join(k3s.ImagesDir, k3s.AirgapName), 0o644); err != nil {
		return fmt.Errorf("copying k3s airgap: %w", err)
	}

	slog.Debug("Restarting K3s")

	cmd := exec.CommandContext(ctx, "systemctl", "restart", k3s.ServiceName) //nolint:gosec
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "systemctl: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restarting k3s: %w", err)
	}

	slog.Info("Waiting for K8s node ready with new version", "version", desired)

	if err := waitKube(ctx, kube, c.Control.Name, "",
		&comp.Node{}, func(node *comp.Node) (bool, error) {
			if node.Status.NodeInfo.KubeletVersion != desired {
				return false, nil
			}

			for _, cond := range node.Status.Conditions {
				if cond.Type == comp.NodeReady && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for k8s node ready: %w", err)
	}

	slog.Debug("K8s node ready with new version", "version", desired)

	slog.Debug("Waiting for registry after K8s upgrade")

	// make sure registry is ready after K8s upgrade and up for at least a minute
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting before registry check after upgrade: %w", ctx.Err())
		case <-time.After(30 * time.Second):
		}

		if err := c.waitRegistry(ctx, kube); err != nil {
			return fmt.Errorf("waiting for registry after k8s upgrade: %w", err)
		}
	}

	slog.Debug("Registry ready after K8s upgrade")

	return nil
}

func (c *ControlUpgrade) waitRegistry(ctx context.Context, kube client.Reader) error {
	regURL, err := comp.RegistryURL(c.Fab)
	if err != nil {
		return fmt.Errorf("getting registry URL: %w", err)
	}

	caCM := coreapi.ConfigMap{}
	attempt := 0
	if err := retry.OnError(wait.Backoff{
		Steps:    17,
		Duration: 500 * time.Millisecond,
		Factor:   1.5,
		Jitter:   0.1,
	}, func(_ error) bool { return true }, func() error {
		if attempt > 0 {
			slog.Debug("Retrying getting CA", "attempt", attempt)
		}

		attempt++

		if err := kube.Get(ctx, client.ObjectKey{
			Namespace: comp.FabNamespace,
			Name:      comp.FabCAConfigMap,
		}, &caCM); err != nil {
			return fmt.Errorf("getting CA config map: %w", err)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("retrying getting ca: %w", err)
	}

	if caCM.Data == nil || caCM.Data[comp.FabCAConfigMapKey] == "" {
		return errors.New("CA config map missing data") //nolint:goerr113
	}

	if err := waitURL(ctx, "https://"+regURL+"/v2/_catalog", caCM.Data[comp.FabCAConfigMapKey]); err != nil {
		return fmt.Errorf("waiting for zot endpoint: %w", err)
	}

	return nil
}
