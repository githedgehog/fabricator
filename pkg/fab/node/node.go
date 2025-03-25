// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func DoConfig(ctx context.Context) error {
	nodeName := os.Getenv("FAB_NODE_NAME")
	if nodeName == "" {
		return fmt.Errorf("FAB_NODE_NAME not set") //nolint:goerr113
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("getting hostname: %w", err)
	}

	if nodeName != hostname {
		return fmt.Errorf("hostname mismatch: running on %q while installer expects %q", hostname, nodeName) //nolint:goerr113
	}

	slog.Info("Configuring node", "nodename", nodeName)

	if err := enforceK3sConfigs(ctx); err != nil {
		return fmt.Errorf("enforcing k3s configs: %w", err)
	}

	return nil
}

func enforceK3sConfigs(ctx context.Context) error {
	slog.Info("Enforcing k3s configs")

	ca := os.Getenv("FAB_CA")
	if ca == "" {
		return fmt.Errorf("FAB_CA not set") //nolint:goerr113
	}

	registries := os.Getenv("FAB_REGISTRIES")
	if registries == "" {
		return fmt.Errorf("FAB_REGISTRIES not set") //nolint:goerr113
	}

	restart := false

	changed, err := enforceFile(certmanager.FabCAPath, []byte(ca), 0o644)
	if err != nil {
		return fmt.Errorf("enforcing CA: %w", err)
	}

	if changed {
		slog.Info("FabCA updated", "path", certmanager.FabCAPath)

		if err := updateCACertificates(ctx); err != nil {
			return fmt.Errorf("updating CA certificates: %w", err)
		}

		// no restart required
		// TODO validate that in case of changing CA in-place k3s would still not require restart
	} else {
		slog.Info("FabCA is up to date", "path", certmanager.FabCAPath)
	}

	changed, err = enforceFile(k3s.KubeRegistriesPath, []byte(registries), 0o600)
	if err != nil {
		return fmt.Errorf("enforcing registries.yaml: %w", err)
	}

	if changed {
		slog.Info("K3s registries.yaml updated", "path", k3s.KubeRegistriesPath)

		restart = true
	} else {
		slog.Info("K3s registries.yaml is up to date", "path", k3s.KubeRegistriesPath)
	}

	if restart {
		slog.Info("Configs affecting k3s service were updated, k3s service restart is required")
	} else {
		slog.Info("No configs requiring k3s service restart were updated")

		restart, err = isK3sNeedsRestart(ctx)
		if err != nil {
			return fmt.Errorf("checking if k3s needs restart: %w", err)
		}
	}

	if restart {
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

		return false, fmt.Errorf("stat %q: %w", k3s.ServerDir, err)
	} else if !stat.IsDir() {
		return false, fmt.Errorf("expected %q to be a directory", k3s.ServerDir) //nolint:goerr113
	}

	return true, nil
}

func isK3sAgent() (bool, error) {
	if stat, err := os.Stat(k3s.AgentDir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("stat %q: %w", k3s.AgentDir, err)
	} else if !stat.IsDir() {
		return false, fmt.Errorf("expected %q to be a directory", k3s.AgentDir) //nolint:goerr113
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

func isK3sNeedsRestart(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	imageURL := os.Getenv("FAB_IMAGE")
	if imageURL == "" {
		return false, fmt.Errorf("FAB_IMAGE not set") //nolint:goerr113
	}

	slog.Info("Checking if k3s needs restart by pulling test image")

	// TODO prevent k3s restart loop from happening

	stdOut, stdErr := &bytes.Buffer{}, &bytes.Buffer{}

	cmd := exec.CommandContext(ctx, filepath.Join(k3s.BinDir, k3s.BinName), "crictl", "pull", imageURL) //nolint:gosec
	cmd.Stdout = io.MultiWriter(stdOut, logutil.NewSink(ctx, slog.Debug, "crictl-pull: "))
	cmd.Stderr = io.MultiWriter(stdErr, logutil.NewSink(ctx, slog.Debug, "crictl-pull: "))

	if err := cmd.Run(); err != nil {
		stdErrStr := stdErr.String()

		if strings.Contains(stdErrStr, "401 Unauthorized") {
			slog.Info("Test image pull failed due to unauthorized error, assuming k3s service restart is required")

			return true, nil
		} else if strings.Contains(stdErrStr, "x509: certificate signed by unknown authority") {
			slog.Info("Test image pull failed due to unknown authority error, assuming k3s service restart is required")

			return true, nil
		} else if strings.Contains(stdErrStr, "authorization failed: no basic auth credentials") {
			slog.Info("Test image pull failed due to no basic auth credentials error, assuming k3s service restart is required")

			return true, nil
		}

		return false, fmt.Errorf("pulling image %q: %w", imageURL, err)
	}

	stdOutStr := stdOut.String()
	if strings.Contains(stdOutStr, "Image is up to date for") {
		slog.Info("Test image is up to date, no k3s service restart is required")

		return false, nil
	}

	return false, fmt.Errorf("unexpected output from crictl: %q", stdOutStr) //nolint:goerr113
}

func updateCACertificates(ctx context.Context) error {
	slog.Info("Updating CA certificates")

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "update-ca-certificates")
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "update-ca: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "update-ca: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running update-ca-certificates: %w", err)
	}

	return nil
}
