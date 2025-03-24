// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.githedgehog.com/fabric/pkg/util/logutil"
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
)

const (
	ConfigDir          = "/opt/hedgehog/node"
	CAFileName         = "ca.pem"
	RegistriesFileName = "registries.yaml"
)

func DoConfig(ctx context.Context, workDir, nodeName string) error {
	if workDir == "" {
		return fmt.Errorf("workdir is empty") //nolint:goerr113
	}

	if nodeName == "" {
		return fmt.Errorf("nodename is empty") //nolint:goerr113
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("getting hostname: %w", err)
	}

	if nodeName != hostname {
		return fmt.Errorf("hostname mismatch: running on %q while installer expects %q", hostname, nodeName) //nolint:goerr113
	}

	slog.Info("Configuring node", "workdir", workDir, "nodename", nodeName)

	if err := enforceK3sConfigs(ctx, workDir); err != nil {
		return fmt.Errorf("enforcing k3s configs: %w", err)
	}

	return nil
}

func enforceK3sConfigs(ctx context.Context, workDir string) error {
	slog.Info("Enforcing k3s configs")

	ca, err := os.ReadFile(filepath.Join(workDir, CAFileName))
	if err != nil {
		return fmt.Errorf("reading CA: %w", err)
	}

	registries, err := os.ReadFile(filepath.Join(workDir, RegistriesFileName))
	if err != nil {
		return fmt.Errorf("reading registries.yaml: %w", err)
	}

	changed, err := enforceFile(certmanager.FabCAPath, ca, 0o644)
	if err != nil {
		return fmt.Errorf("enforcing CA: %w", err)
	}

	if changed {
		slog.Info("FabCA updated", "path", certmanager.FabCAPath)
	} else {
		slog.Info("FabCA is up to date", "path", certmanager.FabCAPath)
	}

	changed, err = enforceFile(k3s.KubeRegistriesPath, registries, 0o644)
	if err != nil {
		return fmt.Errorf("enforcing registries.yaml: %w", err)
	}

	if changed {
		slog.Info("K3s registries.yaml updated", "path", k3s.KubeRegistriesPath)
	} else {
		slog.Info("K3s registries.yaml is up to date", "path", k3s.KubeRegistriesPath)
	}

	if changed {
		slog.Info("Configs affecting k3s service were updated, restarting k3s service")

		if isServer, err := isK3sServer(); err != nil {
			return fmt.Errorf("checking if k3s server: %w", err)
		} else if isServer {
			if err := systemctlRestart(ctx, k3s.ServerServiceName); err != nil {
				return fmt.Errorf("restarting k3s server: %w", err)
			}
		} else if isAgent, err := isK3sAgent(); err != nil {
			return fmt.Errorf("checking if k3s agent: %w", err)
		} else if isAgent {
			if err := systemctlRestart(ctx, k3s.AgentServiceName); err != nil {
				return fmt.Errorf("restarting k3s agent: %w", err)
			}
		} else {
			return fmt.Errorf("not a k3s server or agent") //nolint:goerr113
		}
	}

	return nil
}

func enforceFile(path string, expectedContent []byte, mode os.FileMode) (bool, error) {
	actualContent, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading file %q: %w", path, err) //nolint:goerr113
	}

	if string(actualContent) != string(expectedContent) {
		if err := os.WriteFile(path, expectedContent, mode); err != nil {
			return false, fmt.Errorf("writing file %q: %w", path, err)
		}

		return true, nil
	}

	if stat, err := os.Stat(path); err != nil {
		return false, fmt.Errorf("stat %q: %w", path, err)
	} else if stat.Mode() != mode {
		if err := os.Chmod(path, mode); err != nil {
			return false, fmt.Errorf("chmod %q: %w", path, err)
		}

		return true, nil
	}

	return false, nil
}

func isK3sServer() (bool, error) {
	if stat, err := os.Stat(k3s.ServerDir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("stat %q: %w", k3s.ConfigPath, err)
	} else if !stat.IsDir() {
		return false, fmt.Errorf("expected %q to be a directory", k3s.ServerDir) //nolint:goerr113
	}

	return true, nil
}

func isK3sAgent() (bool, error) {
	if stat, err := os.Stat(k3s.KubeConfigPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("stat %q: %w", k3s.KubeConfigPath, err)
	} else if stat.IsDir() {
		return false, fmt.Errorf("expected %q to be a file", k3s.KubeConfigPath) //nolint:goerr113
	}

	return true, nil
}

func systemctlRestart(ctx context.Context, serviceName string) error {
	slog.Info("Restarting service", "service", serviceName)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	{
		cmd := exec.CommandContext(ctx, "journalctl", "-fu", serviceName)
		cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "svc-log: ")
		cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "svc-log: ")

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("starting journalctl: %w", err)
		}

		// we're intentionally not waiting for the command to finish
		defer cmd.Process.Kill() //nolint:errcheck
	}

	cmd := exec.CommandContext(ctx, "systemctl", "restart", serviceName)
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "svc-restart: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "svc-restart: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restarting %q: %w", serviceName, err)
	}

	return nil
}
