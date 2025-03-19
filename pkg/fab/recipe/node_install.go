// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	coreapi "k8s.io/api/core/v1"
)

type NodeInstall struct {
	WorkDir string
	Fab     fabapi.Fabricator
	Node    fabapi.Node
}

func (c *NodeInstall) Run(ctx context.Context) error {
	slog.Info("Running node installation", "name", c.Node.Name, "roles", c.Node.Spec.Roles)

	// TODO dedup
	slog.Info("Wait for registry on control node(s)")

	regURL, err := comp.RegistryURL(c.Fab)
	if err != nil {
		return fmt.Errorf("getting registry URL: %w", err)
	}

	if err := waitURL(ctx, "https://"+regURL+"/v2/_catalog", ""); err != nil {
		return fmt.Errorf("waiting for zot endpoint: %w", err)
	}

	controlVIP, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}

	if err := setupTimesync(ctx, controlVIP.Addr().String()); err != nil {
		return fmt.Errorf("setting up timesync: %w", err)
	}

	if err := c.joinK8s(ctx); err != nil {
		return fmt.Errorf("joining k8s cluster: %w", err)
	}

	return nil
}

// TODO dedup with contol node's k3s install
func (c *NodeInstall) joinK8s(ctx context.Context) error {
	slog.Info("Joining k8s cluster")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := copyFile(k3s.BinName, filepath.Join(k3s.BinDir, k3s.BinName), 0o755); err != nil {
		return fmt.Errorf("copying k3s bin: %w", err)
	}

	if err := os.MkdirAll(k3s.ImagesDir, 0o755); err != nil {
		return fmt.Errorf("creating k3s images dir %q: %w", k3s.ImagesDir, err)
	}

	if err := copyFile(k3s.AirgapName, filepath.Join(k3s.ImagesDir, k3s.AirgapName), 0o644); err != nil {
		return fmt.Errorf("copying k3s airgap: %w", err)
	}

	if err := os.MkdirAll(k3s.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("creating k3s config dir %q: %w", k3s.ConfigPath, err)
	}

	k3sCfg, err := k3s.AgentConfig(c.Fab, c.Node)
	if err != nil {
		return fmt.Errorf("k3s config: %w", err)
	}
	if err := os.WriteFile(k3s.ConfigPath, []byte(k3sCfg), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing file %q: %w", k3s.ConfigPath, err)
	}

	k3sInstall := "./" + k3s.InstallName
	if err := os.Chmod(k3sInstall, 0o755); err != nil {
		return fmt.Errorf("chmod k3s install: %w", err)
	}

	controlVIP, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}

	args := []string{}
	for _, role := range c.Node.Spec.Roles {
		args = append(args,
			"--node-role", fabapi.RoleLabelKey(role)+"="+fabapi.RoleLabelValue,
			"--node-taint", fabapi.RoleTaintKey(role)+"="+fabapi.RoleTaintValue+":"+string(coreapi.TaintEffectNoExecute),
		)
	}

	slog.Debug("Running k3s install")
	cmd := exec.CommandContext(ctx, k3sInstall, args...)
	cmd.Env = append(os.Environ(),
		"INSTALL_K3S_SKIP_DOWNLOAD=true",
		"INSTALL_K3S_BIN_DIR=/opt/bin",
		fmt.Sprintf("K3S_URL=https://%s:%d", controlVIP.Addr().String(), k3s.APIPort),
		"K3S_TOKEN=temp-testing-only", // TODO change with actually generated/confiuigured token
	)
	cmd.Dir = c.WorkDir
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "k3s: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "k3s: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running k3s install: %w", err)
	}

	return nil
}
