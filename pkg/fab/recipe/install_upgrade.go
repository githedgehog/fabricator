// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"bufio"
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
	"strings"
	"time"

	"github.com/beevik/ntp"
	"github.com/mattn/go-isatty"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

const (
	InstallLog            = "/var/log/install.log"
	HedgehogDir           = "/opt/hedgehog"
	InstallMarkerFile     = HedgehogDir + "/.install"
	InstallMarkerComplete = "complete"
)

func DoInstall(ctx context.Context, workDir string, yes bool) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()

	rawMarker, err := os.ReadFile(InstallMarkerFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading install marker: %w", err)
	}
	if err == nil {
		marker := strings.TrimSpace(string(rawMarker))
		if marker == InstallMarkerComplete {
			slog.Info("Node seems to be already installed", "status", marker, "marker", InstallMarkerFile)

			return nil
		}

		slog.Info("Node seems to be partially installed, cleanup and re-run", "status", marker, "marker", InstallMarkerFile)

		return fmt.Errorf("partially installed: %s", marker) //nolint:goerr113
	}

	cfg, err := LoadConfig(workDir)
	if err != nil {
		return fmt.Errorf("loading recipe config: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("getting hostname: %w", err)
	}

	if cfg.Name != hostname {
		return fmt.Errorf("hostname mismatch: running on %q while installer expects %q", hostname, cfg.Name) //nolint:goerr113
	}

	if err := os.MkdirAll(HedgehogDir, 0o755); err != nil {
		return fmt.Errorf("creating hedgehog dir %q: %w", HedgehogDir, err)
	}

	if cfg.Type == TypeControl {
		l := apiutil.NewFabLoader()
		fabCfg, err := os.ReadFile(filepath.Join(workDir, FabName))
		if err != nil {
			return fmt.Errorf("reading fab config: %w", err)
		}

		if err := l.LoadAdd(ctx, fabCfg); err != nil {
			return fmt.Errorf("loading fab config: %w", err)
		}

		f, controls, nodes, err := fab.GetFabAndNodes(ctx, l.GetClient())
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

		regUsers, err := zot.NewUsers()
		if err != nil {
			return fmt.Errorf("generating zot users: %w", err)
		}

		if err := (&ControlInstall{
			ControlUpgrade: &ControlUpgrade{
				WorkDir: workDir,
				Yes:     yes,
				Fab:     f,
				Control: controls[0],
				Nodes:   nodes,
			},
			WorkDir:  workDir,
			Fab:      f,
			Control:  controls[0],
			Wiring:   wL,
			RegUsers: regUsers,
		}).Run(ctx); err != nil {
			return fmt.Errorf("running control install: %w", err)
		}
	} else if cfg.Type == TypeNode {
		l := apiutil.NewFabLoader()
		fabCfg, err := os.ReadFile(filepath.Join(workDir, FabName))
		if err != nil {
			return fmt.Errorf("reading fab config: %w", err)
		}

		if err := l.LoadAdd(ctx, fabCfg); err != nil {
			return fmt.Errorf("loading fab config: %w", err)
		}

		f, _, nodes, err := fab.GetFabAndNodes(ctx, l.GetClient(), fab.GetFabAndNodesOpts{AllowNoControls: true})
		if err != nil {
			return fmt.Errorf("getting fabricator and nodes: %w", err)
		}

		if len(nodes) != 1 {
			return fmt.Errorf("expected exactly 1 node, got %d", len(nodes)) //nolint:goerr113
		}

		if err := (&NodeInstall{
			WorkDir: workDir,
			Fab:     f,
			Node:    nodes[0],
		}).Run(ctx); err != nil {
			return fmt.Errorf("running node install: %w", err)
		}
	} else {
		return fmt.Errorf("unknown installer type %q", cfg.Type) //nolint:goerr113
	}

	if err := os.WriteFile(InstallMarkerFile, []byte(InstallMarkerComplete), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing install marker: %w", err)
	}

	return nil
}

func DoUpgrade(ctx context.Context, workDir string, yes bool) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()

	rawMarker, err := os.ReadFile(InstallMarkerFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading install marker: %w", err)
	}
	if err == nil {
		marker := strings.TrimSpace(string(rawMarker))
		if marker != InstallMarkerComplete {
			slog.Info("Node seems to be not installed successfully", "status", marker, "marker", InstallMarkerFile)

			return nil
		}
	} else {
		slog.Info("Node seems to be not installed", "marker", InstallMarkerFile)

		return fmt.Errorf("install marker file not found: %w", err)
	}

	cfg, err := LoadConfig(workDir)
	if err != nil {
		return fmt.Errorf("loading recipe config: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("getting hostname: %w", err)
	}

	if cfg.Name != hostname {
		return fmt.Errorf("hostname mismatch: running on %q while upgrader expects %q", hostname, cfg.Name) //nolint:goerr113
	}

	if cfg.Type == TypeControl {
		if err := (&ControlUpgrade{
			WorkDir: workDir,
			Yes:     yes,
		}).Run(ctx); err != nil {
			return fmt.Errorf("running control upgrade: %w", err)
		}
	} else if cfg.Type == TypeNode {
		slog.Warn("Node upgrade is not implemented yet")
		// TODO implement node upgrade
	} else {
		return fmt.Errorf("unknown upgrader type %q", cfg.Type) //nolint:goerr113
	}

	if err := os.WriteFile(InstallMarkerFile, []byte(InstallMarkerComplete), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing install marker: %w", err)
	}

	return nil
}

func waitNTP(ctx context.Context, controlVIP string) error {
	slog.Info("Waiting for NTP server on control node(s)")

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for NTP %s: %w", controlVIP, ctx.Err())
		case <-time.After(15 * time.Second):
			if _, err := ntp.Time(controlVIP); err != nil {
				slog.Debug("Waiting for NTP (not an error)", "reason", err)

				continue
			}

			return nil
		}
	}
}

func setupTimesync(ctx context.Context, controlVIP string) error {
	if err := waitNTP(ctx, controlVIP); err != nil {
		return fmt.Errorf("waiting for NTP: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	slog.Info("Setting up timesync")

	// TODO remove if it'll be managed by control agent?

	cfg := []byte(fmt.Sprintf("[Time]\nNTP=%s\n", controlVIP))
	if err := os.WriteFile("/etc/systemd/timesyncd.conf", cfg, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing timesyncd.conf: %w", err)
	}

	cmd := exec.CommandContext(ctx, "systemctl", "restart", "systemd-timesyncd")
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "systemctl: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restarting systemd-timesyncd: %w", err)
	}

	// TODO check `timedatectl timesync-status` output

	return nil
}

const (
	FlatcarVersionPrefix = "VERSION="
)

func upgradeFlatcar(ctx context.Context, targetVersion string, yes bool) error {
	slog.Info("Upgrading Flatcar")
	const filename = "/etc/os-release"

	content, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("could not read /etc/os-release : %w", err)
	}

	version := ""
	for line := range strings.Lines(string(content)) {
		if strings.HasPrefix(line, FlatcarVersionPrefix) {
			version = strings.TrimSpace(strings.TrimPrefix(line, FlatcarVersionPrefix))
		}
	}
	if version == "" {
		return fmt.Errorf("could not find flatcar version in /etc/os-release") //nolint:goerr113
	}

	if version == strings.TrimPrefix(targetVersion, "v") {
		slog.Info("System already running desired Flatcar", "version", targetVersion)

		return nil
	}

	slog.Info("Upgrading Flatcar to", "version", targetVersion)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			slog.Debug("Retrying upgrading Flatcar", "attempt", attempt)
		}

		cmd := exec.CommandContext(ctx, "flatcar-update", "--to-version", targetVersion, "--to-payload", flatcar.UpdateBinName) //nolint:gosec
		cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "flatcar-update: ")
		cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "flatcar-update: ")

		if err := cmd.Run(); err != nil {
			lastErr = fmt.Errorf("running flatcar-update: %w", err)

			continue
		}

		lastErr = nil
		slog.Info("Flatcar upgrade completed")

		break
	}
	if lastErr != nil {
		cmd := exec.CommandContext(ctx, "journalctl", "-t", "update_engine", "-n", "100")
		cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "update_engine: ")
		cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "update_engine: ")

		if err := cmd.Run(); err != nil {
			slog.Warn("Failed to print update_engine logs", "err", err)
		}

		return fmt.Errorf("retrying upgrading Flatcar: %w", lastErr)
	}

	reboot := yes
	if !reboot && isatty.IsTerminal(os.Stdout.Fd()) {
		ok, err := askForConfirmation("Do you really want to reboot your system?")
		if err != nil {
			slog.Warn("Failed asking for confirmation, assuming 'no'", "err", err)
		}
		if ok {
			reboot = true
		}
	}

	if reboot {
		slog.Info("Rebooting Control Node")

		cmd := exec.CommandContext(ctx, "reboot")
		cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "reboot: ")
		cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "reboot: ")

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("rebooting: %w", err)
		}

		return nil
	}

	slog.Warn("A reboot is necessary for the changes to take effect")

	return nil
}

func askForConfirmation(s string) (bool, error) {
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Printf("%s [y/n]: ", s)

		response, err := reader.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("reading response: %w", err)
		}

		response = strings.ToLower(strings.TrimSpace(response))

		if response == "y" || response == "yes" {
			return true, nil
		} else if response == "n" || response == "no" {
			return false, nil
		}
	}
}

func waitURL(ctx context.Context, url string, ca string) error {
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	if ca != "" {
		rootCAs := x509.NewCertPool()
		if !rootCAs.AppendCertsFromPEM([]byte(ca)) {
			return errors.New("failed to append CA cert to rootCAs") //nolint:goerr113
		}

		baseTransport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: false,
			RootCAs:            rootCAs,
		}
	} else {
		baseTransport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec
		}
	}

	client := &http.Client{
		Transport: baseTransport,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for URL %s: %w", url, ctx.Err())
		case <-time.After(15 * time.Second):
			resp, err := client.Do(req)
			if err != nil {
				slog.Debug("Waiting for URL (not an error)", "reason", err)

				continue
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
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
