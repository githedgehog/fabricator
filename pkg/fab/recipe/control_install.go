package recipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

// "install" k3s binary, its config and airgap images
// generate fabca (root CA) and install it into the system
// run k3s-install.sh script
// wait for k8s ready
// file structure for kubeconfig
// install cert-manager component
// wait for cert-manager ready
// install fabca - just secret + issuer
// install zot component
// wait for zot ready
// upload images into zot if airgap
// install fabricator component
// wait for fabricator ready
// install fab.yaml
// wait for fabric ready
// install pre-packaged wiring
// wait for control agents ready

// control agent: ???
// /etc/hosts with switches
// install k9s with config and plugins

const (
	InstallLog            = "/var/log/install.log"
	HedgehogDir           = "/opt/hedgehog"
	InstallMarkerFile     = HedgehogDir + "/.install"
	InstallMarkerStarted  = "started"
	InstallMarkerComplete = "complete"
)

func DoControlInstall(ctx context.Context, workDir string) error {
	rawMarker, err := os.ReadFile(InstallMarkerFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading install marker: %w", err)
	}
	if err == nil {
		marker := strings.TrimSpace(string(rawMarker))
		if marker == InstallMarkerComplete {
			slog.Info("Control node seems to be already installed", "marker", InstallMarkerFile)

			return nil
		}

		slog.Info("Control node seems to be partially installed, cleanup and re-run", "marker", InstallMarkerFile, "status", marker)

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

	f, controls, err := fab.GetFabAndControls(ctx, l.GetClient(), true)
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

	return (&ControlInstall{
		WorkDir: workDir,
		Fab:     f,
		Control: controls[0],
		Wiring:  wL,
	}).Run(ctx)
}

type ControlInstall struct {
	WorkDir string
	Fab     fabapi.Fabricator
	Control fabapi.ControlNode
	Wiring  *apiutil.Loader
}

func (c *ControlInstall) Run(ctx context.Context) error {
	if err := os.WriteFile(InstallMarkerFile, []byte(InstallMarkerStarted), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing install marker: %w", err)
	}

	if err := c.copyFile(k3s.BinName, filepath.Join(k3s.BinDir, k3s.BinName), 0o755); err != nil {
		return fmt.Errorf("copying k3s bin: %w", err)
	}

	if err := os.MkdirAll(k3s.ImagesDir, 0o755); err != nil {
		return fmt.Errorf("creating k3s images dir %q: %w", k3s.ImagesDir, err)
	}

	if err := c.copyFile(k3s.AirgapName, filepath.Join(k3s.ImagesDir, k3s.AirgapName), 0o644); err != nil {
		return fmt.Errorf("copying k3s airgap: %w", err)
	}

	k3sCfg, err := k3s.Config(c.Fab, c.Control)
	if err != nil {
		return fmt.Errorf("k3s config: %w", err)
	}

	if err := os.MkdirAll(k3s.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("creating k3s config dir %q: %w", k3s.ConfigPath, err)
	}

	if err := os.WriteFile(k3s.ConfigPath, []byte(k3sCfg), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing file %q: %w", k3s.ConfigPath, err)
	}

	if err := c.k3sInstall(ctx); err != nil {
		return fmt.Errorf("installing k3s: %w", err)
	}

	if err := os.WriteFile(InstallMarkerFile, []byte(InstallMarkerComplete), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing install marker: %w", err)
	}

	return nil
}

func (c *ControlInstall) copyFile(src, dst string, mode os.FileMode) error {
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

func (c *ControlInstall) k3sInstall(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, "./"+k3s.InstallName, "--disable=servicelb,traefik") //nolint:gosec
	cmd.Env = append(os.Environ(),
		"INSTALL_K3S_SKIP_DOWNLOAD=true",
		"INSTALL_K3S_BIN_DIR=/opt/bin",
	)
	cmd.Dir = c.WorkDir
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "k3s: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "k3s: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running k3s install: %w", err)
	}

	return nil
}
