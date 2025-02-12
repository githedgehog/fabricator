// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k9s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	InstallLog            = "/var/log/install.log"
	HedgehogDir           = "/opt/hedgehog"
	InstallMarkerFile     = HedgehogDir + "/.install"
	InstallMarkerComplete = "complete"
)

func DoControlInstall(ctx context.Context, workDir string) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()

	rawMarker, err := os.ReadFile(InstallMarkerFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading install marker: %w", err)
	}
	if err == nil {
		marker := strings.TrimSpace(string(rawMarker))
		if marker == InstallMarkerComplete {
			slog.Info("Control node seems to be already installed", "status", marker, "marker", InstallMarkerFile)

			return nil
		}

		slog.Info("Control node seems to be partially installed, cleanup and re-run", "status", marker, "marker", InstallMarkerFile)

		return fmt.Errorf("partially installed: %s", marker) //nolint:goerr113
	}

	l := apiutil.NewFabLoader()
	fabCfg, err := os.ReadFile(filepath.Join(workDir, FabName))
	if err != nil {
		return fmt.Errorf("reading fab config: %w", err)
	}

	if err := l.LoadAdd(ctx, fabCfg); err != nil {
		return fmt.Errorf("loading fab config: %w", err)
	}

	f, controls, nodes, err := fab.GetFabAndNodes(ctx, l.GetClient(), false)
	if err != nil {
		return fmt.Errorf("getting fabricator and controls nodes: %w", err)
	}

	if len(controls) != 1 {
		return fmt.Errorf("expected exactly 1 control node, got %d", len(controls)) //nolint:goerr113
	}

	wL := apiutil.NewWiringLoader()
	wiringCfg, err := os.ReadFile(filepath.Join(workDir, WiringName))
	if err != nil {
		return fmt.Errorf("reading wiring config: %w", err)
	}

	if err := wL.LoadAdd(ctx, wiringCfg); err != nil {
		return fmt.Errorf("loading wiring config: %w", err)
	}

	if err := os.MkdirAll(HedgehogDir, 0o755); err != nil {
		return fmt.Errorf("creating hedgehog dir %q: %w", HedgehogDir, err)
	}

	regUsers, err := zot.NewUsers()
	if err != nil {
		return fmt.Errorf("generating zot users: %w", err)
	}

	return (&ControlInstall{
		ControlUpgrade: &ControlUpgrade{
			WorkDir: workDir,
			Fab:     f,
			Control: controls[0],
			Nodes:   nodes,
		},
		WorkDir:  workDir,
		Fab:      f,
		Control:  controls[0],
		Wiring:   wL,
		RegUsers: regUsers,
	}).Run(ctx)
}

type ControlInstall struct {
	*ControlUpgrade
	WorkDir  string
	Fab      fabapi.Fabricator
	Control  fabapi.ControlNode
	Wiring   *apiutil.Loader
	RegUsers map[string]string
}

func (c *ControlInstall) Run(ctx context.Context) error {
	slog.Info("Running control node installation")

	kube, err := c.installK8s(ctx)
	if err != nil {
		return fmt.Errorf("installing k3s: %w", err)
	}

	c.Fab.Status.IsBootstrap = true
	c.Fab.Status.IsInstall = true

	if err := kube.Create(ctx, comp.NewNamespace(comp.FabNamespace)); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %q: %w", comp.FabNamespace, err)
	}

	if err := c.installCertManager(ctx, kube); err != nil {
		return fmt.Errorf("installing cert-manager: %w", err)
	}

	ca, err := c.installFabCA(ctx, kube)
	if err != nil {
		return fmt.Errorf("installing fab-ca: %w", err)
	}

	if err := c.installZot(ctx, kube, ca); err != nil {
		return fmt.Errorf("installing zot: %w", err)
	}

	// we should use in-cluster registry from now on
	c.Fab.Status.IsBootstrap = false

	if c.Fab.Spec.Config.Registry.IsAirgap() {
		if err := c.uploadAirgap(ctx, comp.RegistryUserWriter, c.RegUsers[comp.RegistryUserWriter]); err != nil {
			return fmt.Errorf("uploading airgap artifacts: %w", err)
		}
	}

	if err := c.preCacheZot(ctx); err != nil {
		return fmt.Errorf("pre-caching zot: %w", err)
	}

	if err := c.installFabricator(ctx, kube, true); err != nil {
		return fmt.Errorf("installing fabricator and config: %w", err)
	}

	if err := c.waitReloader(ctx, kube); err != nil {
		return fmt.Errorf("installing reloader: %w", err)
	}

	if err := c.waitNTP(ctx, kube); err != nil {
		return fmt.Errorf("installing ntp: %w", err)
	}

	if err := c.setupTimesync(ctx); err != nil {
		return fmt.Errorf("setting up timesync: %w", err)
	}

	if err := c.installWaitFabric(ctx, kube); err != nil {
		return fmt.Errorf("installing fabric: %w", err)
	}

	if err := c.installWiring(ctx, kube); err != nil {
		return fmt.Errorf("installing included wiring: %w", err)
	}

	if err := os.WriteFile(InstallMarkerFile, []byte(InstallMarkerComplete), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing install marker: %w", err)
	}

	slog.Info("Control node installation complete")

	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	srcF, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %q: %w", src, err)
	}
	defer srcF.Close()

	dstF, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %q: %w", dst, err)
	}
	defer dstF.Close()

	if _, err := io.Copy(dstF, srcF); err != nil {
		return fmt.Errorf("copying file %q to %q: %w", src, dst, err)
	}

	if mode != 0 {
		if err := os.Chmod(dst, mode); err != nil {
			return fmt.Errorf("chmod %q: %w", dst, err)
		}
	}

	return nil
}

func (c *ControlInstall) installK8s(ctx context.Context) (client.Client, error) {
	slog.Info("Installing k3s")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := copyFile(k3s.BinName, filepath.Join(k3s.BinDir, k3s.BinName), 0o755); err != nil {
		return nil, fmt.Errorf("copying k3s bin: %w", err)
	}

	if err := os.MkdirAll(k3s.ImagesDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating k3s images dir %q: %w", k3s.ImagesDir, err)
	}

	if err := os.MkdirAll(k3s.ChartsDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating k3s static dir %q: %w", k3s.ChartsDir, err)
	}

	if err := copyFile(k3s.AirgapName, filepath.Join(k3s.ImagesDir, k3s.AirgapName), 0o644); err != nil {
		return nil, fmt.Errorf("copying k3s airgap: %w", err)
	}

	if err := copyFile(certmanager.AirgapImageName, filepath.Join(k3s.ImagesDir, certmanager.AirgapImageName), 0o644); err != nil {
		return nil, fmt.Errorf("copying cert-manager airgap image: %w", err)
	}

	if err := copyFile(certmanager.AirgapChartName, filepath.Join(k3s.ChartsDir, certmanager.AirgapChartName), 0o644); err != nil {
		return nil, fmt.Errorf("copying cert-manager airgap chart: %w", err)
	}

	if err := copyFile(zot.AirgapImageName, filepath.Join(k3s.ImagesDir, zot.AirgapImageName), 0o644); err != nil {
		return nil, fmt.Errorf("copying zot airgap image: %w", err)
	}

	if err := copyFile(zot.AirgapChartName, filepath.Join(k3s.ChartsDir, zot.AirgapChartName), 0o644); err != nil {
		return nil, fmt.Errorf("copying zot airgap chart: %w", err)
	}

	if err := os.MkdirAll(k3s.ConfigDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating k3s config dir %q: %w", k3s.ConfigPath, err)
	}

	k3sCfg, err := k3s.Config(c.Fab, c.Control)
	if err != nil {
		return nil, fmt.Errorf("k3s config: %w", err)
	}
	if err := os.WriteFile(k3s.ConfigPath, []byte(k3sCfg), 0o644); err != nil { //nolint:gosec
		return nil, fmt.Errorf("writing file %q: %w", k3s.ConfigPath, err)
	}

	regCfg, err := k3s.Registries(c.Fab, comp.RegistryUserReader, c.RegUsers[comp.RegistryUserReader])
	if err != nil {
		return nil, fmt.Errorf("k3s registries: %w", err)
	}
	if err := os.WriteFile(k3s.KubeRegistriesPath, []byte(regCfg), 0o600); err != nil {
		return nil, fmt.Errorf("writing file %q: %w", k3s.KubeRegistriesPath, err)
	}

	k3sInstall := "./" + k3s.InstallName
	if err := os.Chmod(k3sInstall, 0o755); err != nil {
		return nil, fmt.Errorf("chmod k3s install: %w", err)
	}

	slog.Debug("Running k3s install")
	cmd := exec.CommandContext(ctx, k3sInstall, "--disable=servicelb,traefik")
	cmd.Env = append(os.Environ(),
		"INSTALL_K3S_SKIP_DOWNLOAD=true",
		"INSTALL_K3S_BIN_DIR=/opt/bin",
	)
	cmd.Dir = c.WorkDir
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "k3s: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "k3s: ")

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running k3s install: %w", err)
	}

	if err := copyFile(k9s.BinName, filepath.Join(k3s.BinDir, k9s.BinName), 0o755); err != nil {
		return nil, fmt.Errorf("copying k9s bin: %w", err)
	}

	slog.Debug("Waiting for k8s node ready")

	kube, err := kubeutil.NewClient(ctx, k3s.KubeConfigPath,
		comp.CoreAPISchemeBuilder, comp.AppsAPISchemeBuilder,
		comp.HelmAPISchemeBuilder, comp.CMApiSchemeBuilder, comp.CMMetaSchemeBuilder,
		wiringapi.SchemeBuilder, vpcapi.SchemeBuilder, fabapi.SchemeBuilder,
	)
	if err != nil {
		return nil, fmt.Errorf("creating kube client: %w", err)
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
		return nil, fmt.Errorf("waiting for k8s node ready: %w", err)
	}

	return kube, nil
}

func (c *ControlInstall) installCertManager(ctx context.Context, kube client.Client) error {
	slog.Info("Installing cert-manager")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, certmanager.Install); err != nil {
		return fmt.Errorf("enforcing cert-manager install: %w", err)
	}

	slog.Debug("Waiting for cert-manager ready")

	if err := waitKube(ctx, kube, "cert-manager-webhook", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for cert-manager ready: %w", err)
	}

	return nil
}

func (c *ControlInstall) installFabCA(ctx context.Context, kube client.Client) (string, error) {
	slog.Info("Installing fab-ca")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	ca, err := certmanager.NewFabCA()
	if err != nil {
		return "", fmt.Errorf("creating fab-ca: %w", err)
	}

	if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, certmanager.InstallFabCA(ca)); err != nil {
		return "", fmt.Errorf("enforcing fab-ca install: %w", err)
	}

	if err := os.WriteFile("/etc/ssl/certs/hh-fab-ca.crt", []byte(ca.Crt), 0o644); err != nil { //nolint:gosec
		return "", fmt.Errorf("writing fab-ca cert: %w", err)
	}

	cmd := exec.CommandContext(ctx, "update-ca-certificates")
	cmd.Dir = c.WorkDir
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "update-ca: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "update-ca: ")

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running update-ca-certificates: %w", err)
	}

	slog.Debug("Waiting for fab-ca ready")

	if err := waitKube(ctx, kube, comp.FabCAIssuer, comp.FabNamespace,
		&comp.Issuer{}, func(obj *comp.Issuer) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.IssuerConditionReady && cond.Status == comp.CMConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return "", fmt.Errorf("waiting for fab-ca issuer ready: %w", err)
	}

	return ca.Crt, nil
}

func (c *ControlInstall) installZot(ctx context.Context, kube client.Client, ca string) error {
	slog.Info("Installing zot")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, zot.InstallUsers(c.RegUsers)); err != nil {
		return fmt.Errorf("enforcing zot users install: %w", err)
	}

	if err := comp.EnforceKubeInstall(ctx, kube, c.Fab, zot.Install); err != nil {
		return fmt.Errorf("enforcing zot install: %w", err)
	}

	slog.Debug("Waiting for zot ready")

	if err := waitKube(ctx, kube, "zot", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for zot ready: %w", err)
	}

	regURL, err := comp.RegistryURL(c.Fab)
	if err != nil {
		return fmt.Errorf("getting registry URL: %w", err)
	}

	if err := waitURL(ctx, "https://"+regURL+"/v2/_catalog", ca); err != nil {
		return fmt.Errorf("waiting for zot endpoint: %w", err)
	}

	return nil
}

func (c *ControlInstall) waitReloader(ctx context.Context, kube client.Client) error {
	slog.Info("Waiting for reloader")

	if err := waitKube(ctx, kube, "reloader-reloader", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for reloader ready: %w", err)
	}

	return nil
}

func (c *ControlInstall) installWaitFabric(ctx context.Context, kube client.Client) error {
	if err := c.installFabricCtl(ctx); err != nil {
		return fmt.Errorf("installing kubectl-fabric: %w", err)
	}

	slog.Info("Waiting for fabric services")

	if err := waitKube(ctx, kube, "fabric-ctrl", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for fabric-ctrl ready: %w", err)
	}

	if err := waitKube(ctx, kube, "fabric-boot", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for fabric-boot ready: %w", err)
	}

	if err := waitKube(ctx, kube, "fabric-dhcpd", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for fabric-dhcpd ready: %w", err)
	}

	return nil
}

func (c *ControlInstall) installWiring(ctx context.Context, kube client.Client) error {
	slog.Info("Waiting for all used switch profiles ready")

	switches := &wiringapi.SwitchList{}
	if err := c.Wiring.List(ctx, switches); err != nil {
		return fmt.Errorf("listing included switches: %w", err)
	}

	checkedProfiles := map[string]bool{}
	for _, sw := range switches.Items {
		if checkedProfiles[sw.Spec.Profile] {
			continue
		}

		slog.Debug("Waiting for switch profile ready", "name", sw.Spec.Profile)

		if err := waitKube(ctx, kube, sw.Spec.Profile, metav1.NamespaceDefault,
			&wiringapi.SwitchProfile{}, func(obj *wiringapi.SwitchProfile) (bool, error) {
				return obj.GetName() == sw.Spec.Profile, nil
			}); err != nil {
			return fmt.Errorf("waiting for switch profiles ready: %w", err)
		}

		checkedProfiles[sw.Spec.Profile] = true
	}

	slog.Info("Installing included wiring")

	for _, objList := range []meta.ObjectList{
		&wiringapi.VLANNamespaceList{},
		&vpcapi.IPv4NamespaceList{},
		&wiringapi.SwitchGroupList{},
		&wiringapi.SwitchList{},
		&wiringapi.ServerList{},
		&vpcapi.VPCList{},
		&wiringapi.ConnectionList{}, // can be within VPC
		&vpcapi.VPCAttachmentList{},
		&vpcapi.VPCPeeringList{},
		&vpcapi.ExternalList{},
		&vpcapi.ExternalAttachmentList{},
		&vpcapi.ExternalPeeringList{},
		// switch/server profiles are intentionally skipped
	} {
		if err := c.Wiring.List(ctx, objList); err != nil {
			return fmt.Errorf("listing %T: %w", objList, err)
		}

		for _, obj := range objList.GetItems() {
			kind := obj.GetObjectKind().GroupVersionKind().Kind
			name := obj.GetName()

			obj.SetGeneration(0)
			obj.SetResourceVersion("")

			attempt := 0

			if err := retry.OnError(wait.Backoff{
				Steps:    17,
				Duration: 500 * time.Millisecond,
				Factor:   1.5,
				Jitter:   0.1,
			}, func(err error) bool {
				return !apierrors.IsConflict(err)
			}, func() error {
				if attempt > 0 {
					slog.Debug("Retrying installing wiring", "kind", kind, "name", name, "attempt", attempt)
				}

				attempt++

				if err := kube.Create(ctx, obj); err != nil {
					return fmt.Errorf("creating %s/%s: %w", kind, name, err)
				}

				return nil
			}); err != nil {
				return fmt.Errorf("retrying creating %s/%s: %w", kind, name, err)
			}

			slog.Debug("Installed included wiring", "kind", kind, "name", name)
		}
	}

	return nil
}

func (c *ControlInstall) waitNTP(ctx context.Context, kube client.Client) error {
	slog.Info("Waiting for NTP server")

	if err := waitKube(ctx, kube, "ntp", comp.FabNamespace,
		&comp.Deployment{}, func(obj *comp.Deployment) (bool, error) {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == comp.DeploymentAvailable && cond.Status == comp.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("waiting for ntp ready: %w", err)
	}

	return nil
}

func waitKube[T client.Object](ctx context.Context, kube client.Client, name, ns string, obj T, check func(obj T) (bool, error)) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	t := reflect.TypeOf(obj).Elem().Name()

	slog.Debug("Waiting for ready", "kind", t, "name", name)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for ready: %w", ctx.Err())
		case <-ticker.C:
			if err := kube.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, obj); err != nil {
				slog.Debug("Waiting for ready", "kind", t, "name", name, "err", err)

				continue
			}

			ready, err := check(obj)
			if err != nil {
				slog.Debug("Checking ready", "kind", t, "name", name, "err", err)

				continue
			}
			if ready {
				return nil
			}
		}
	}
}

func waitURL(ctx context.Context, url string, ca string) error {
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM([]byte(ca)) {
		return errors.New("failed to append CA cert to rootCAs") //nolint:goerr113
	}

	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTransport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
		RootCAs:            rootCAs,
	}

	client := &http.Client{
		Transport: baseTransport,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for URL: %w", ctx.Err())
		case <-ticker.C:
			resp, err := client.Do(req)
			if err != nil {
				slog.Debug("Waiting for URL", "url", url, "err", err)

				continue
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
}
