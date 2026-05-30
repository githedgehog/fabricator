// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

// EnvCollectSonicTechsupport opts the failure-path debug bundle into
// collecting native SONiC tech-support tarballs. Unset or "false" means the
// expensive collection is skipped even on failure; the manual CLI
// `hhfab vlab switch show-tech` is unaffected.
const EnvCollectSonicTechsupport = "HHFAB_VLAB_COLLECT_SONIC_TECHSUPPORT"

const (
	sonicTechsupportPerSwitchTimeout = 45 * time.Minute
	sonicTechsupportPollInterval     = 15 * time.Second
	sonicTechsupportStartCmd         = `sonic-cli -c "show tech-support"`
	sonicTechsupportStatusCmd        = `sonic-cli -c "show tech-support status"`
	sonicTechsupportStartedMarker    = "tech-support process started"
	sonicTechsupportInProgressMarker = "in progress"
)

func envCollectSonicTechsupport() bool {
	v, _ := strconv.ParseBool(os.Getenv(EnvCollectSonicTechsupport))

	return v
}

// Matches the tarball path SONiC emits in the status output, for example
// /var/dump/sonic_dump_ds5000-01_20260530_120000.tar.gz.
var sonicTechsupportDumpPathRE = regexp.MustCompile(`/var/dump/sonic_dump_[^\s"']+\.tar\.gz`)

// CollectSonicTechsupport runs the native SONiC `show tech-support` on each
// target switch, polls until completion, and downloads the resulting tarball
// to outDir. VM switches accept the same sonic-cli command, but Broadcom
// vendor support will only accept dumps captured from hardware switches.
// Per-switch failures are logged as warnings and do not fail the call;
// only outDir creation or kube listing errors are propagated.
func (c *Config) CollectSonicTechsupport(
	ctx context.Context,
	vlab *VLAB,
	switchNames []string,
	outDir string,
) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating sonic tech-support output directory: %w", err)
	}

	targets := switchNames
	if len(targets) == 0 {
		switches := wiringapi.SwitchList{}
		if err := c.Client.List(ctx, &switches); err != nil {
			return fmt.Errorf("listing switches: %w", err)
		}
		targets = make([]string, 0, len(switches.Items))
		for _, sw := range switches.Items {
			targets = append(targets, sw.Name)
		}
	}

	if len(targets) == 0 {
		slog.Info("No switches to collect SONiC tech-support from")

		return nil
	}

	slog.Info("Collecting SONiC tech-support", "switches", targets, "outDir", outDir)

	var wg sync.WaitGroup
	for _, name := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.collectSonicTechsupportOne(ctx, vlab, name, outDir)
		}()
	}
	wg.Wait()

	return nil
}

func (c *Config) collectSonicTechsupportOne(ctx context.Context, vlab *VLAB, name, outDir string) {
	swCtx, cancel := context.WithTimeout(ctx, sonicTechsupportPerSwitchTimeout)
	defer cancel()

	ssh, err := c.SSH(swCtx, vlab, name)
	if err != nil {
		slog.Warn("SONiC tech-support: ssh config unavailable", "switch", name, "err", err)

		return
	}

	stdout, stderr, err := ssh.Run(swCtx, sonicTechsupportStartCmd)
	if err != nil {
		slog.Warn("SONiC tech-support: start command failed", "switch", name, "err", err, "stderr", stderr)

		return
	}
	if !strings.Contains(strings.ToLower(stdout), sonicTechsupportStartedMarker) {
		slog.Warn("SONiC tech-support: unexpected start response", "switch", name, "stdout", stdout)

		return
	}

	remotePath, err := pollSonicTechsupportStatus(swCtx, ssh, name)
	if err != nil {
		slog.Warn("SONiC tech-support: polling status failed", "switch", name, "err", err)

		return
	}

	localPath := filepath.Join(outDir, name+"-sonic-techsupport.tar.gz")
	if err := ssh.DownloadPath(remotePath, localPath); err != nil {
		slog.Warn("SONiC tech-support: download failed", "switch", name, "remote", remotePath, "err", err)

		return
	}

	slog.Info("SONiC tech-support collected", "switch", name, "path", localPath)
}

// pollSonicTechsupportStatus polls until the dump path appears in the status
// output. It returns the remote tarball path or an error if the context
// expires, ssh fails, or the status output never advertises the path.
func pollSonicTechsupportStatus(ctx context.Context, ssh interface {
	Run(ctx context.Context, cmd string) (string, string, error)
}, name string,
) (string, error) {
	ticker := time.NewTicker(sonicTechsupportPollInterval)
	defer ticker.Stop()

	for {
		stdout, stderr, err := ssh.Run(ctx, sonicTechsupportStatusCmd)
		if err != nil {
			return "", fmt.Errorf("status command: %w (stderr=%q)", err, stderr)
		}

		lower := strings.ToLower(stdout)
		match := sonicTechsupportDumpPathRE.FindString(stdout)
		if !strings.Contains(lower, sonicTechsupportInProgressMarker) && match != "" {
			return match, nil
		}

		slog.Debug("SONiC tech-support still running", "switch", name, "status", strings.TrimSpace(stdout))

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("context done while waiting for tech-support: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
