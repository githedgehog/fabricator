// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
)

// LLDPInspectCaptureDir is the subdirectory under show-tech-output where
// per-attempt LLDP captures land. Lives alongside show-tech logs so the same
// debug bundle picks them up.
const LLDPInspectCaptureDir = "lldp-inspect-captures"

const lldpCapturePerTargetTimeout = 20 * time.Second

// captureLLDPAtFailure snapshots detailed LLDP state on each switch (and any
// server on the far end of a missing neighbor) when an inspect attempt fails.
// Best-effort: a per-target failure is logged but never propagates. Output
// lands in <WorkDir>/show-tech-output/lldp-inspect-captures/attempt-N/<host>.log
// so the standard debug bundle contains it.
func (c *Config) captureLLDPAtFailure(ctx context.Context, vlab *VLAB, attempt int, lldpOut inspect.Out[inspect.LLDPIn]) {
	out, ok := lldpOut.(*inspect.LLDPOut)
	if !ok || out == nil {
		return
	}

	switches, servers := affectedLLDPTargets(out)
	if len(switches) == 0 && len(servers) == 0 {
		return
	}

	outDir := filepath.Join(c.WorkDir, ShowTechOutputDir, LLDPInspectCaptureDir, fmt.Sprintf("attempt-%d", attempt))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		slog.Warn("Failed to create LLDP capture directory; skipping", "dir", outDir, "err", err)

		return
	}

	slog.Info("Capturing LLDP state at inspect failure",
		"attempt", attempt, "switches", switches, "servers", servers, "dir", outDir)

	var wg sync.WaitGroup
	for _, sw := range switches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.captureSwitchLLDP(ctx, vlab, sw, outDir)
		}()
	}
	for _, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.captureServerLLDP(ctx, vlab, srv, outDir)
		}()
	}
	wg.Wait()
}

// affectedLLDPTargets returns the switches and servers that have a missing
// non-external expected neighbor. Output is sorted and deduplicated.
func affectedLLDPTargets(out *inspect.LLDPOut) ([]string, []string) {
	swSet := map[string]struct{}{}
	srvSet := map[string]struct{}{}

	for swName, ports := range out.Neighbors {
		for _, status := range ports {
			if status.Type == apiutil.LLDPNeighborTypeExternal {
				continue
			}
			if neighborPresent(status) {
				continue
			}

			swSet[swName] = struct{}{}
			if status.Type == apiutil.LLDPNeighborTypeServer && status.Expected.Name != "" {
				srvSet[status.Expected.Name] = struct{}{}
			}
		}
	}

	switches := make([]string, 0, len(swSet))
	for name := range swSet {
		switches = append(switches, name)
	}
	servers := make([]string, 0, len(srvSet))
	for name := range srvSet {
		servers = append(servers, name)
	}
	sort.Strings(switches)
	sort.Strings(servers)

	return switches, servers
}

func neighborPresent(status apiutil.LLDPNeighborStatus) bool {
	for _, actual := range status.Actual {
		if actual.Name == status.Expected.Name {
			return true
		}
	}

	return false
}

func (c *Config) captureSwitchLLDP(ctx context.Context, vlab *VLAB, name, outDir string) {
	captureCtx, cancel := context.WithTimeout(ctx, lldpCapturePerTargetTimeout)
	defer cancel()

	ssh, err := c.SSH(captureCtx, vlab, name)
	if err != nil {
		slog.Warn("LLDP capture: ssh config unavailable", "switch", name, "err", err)

		return
	}

	out := &strings.Builder{}
	fmt.Fprintf(out, "=== LLDP inspect failure capture: switch %s at %s ===\n",
		name, time.Now().UTC().Format(time.RFC3339Nano))

	runOnTarget(captureCtx, ssh, out, "sonic-cli -c 'show lldp neighbor | no-more'")
	runOnTarget(captureCtx, ssh, out, "docker exec lldp lldpcli show neighbors detail")
	runOnTarget(captureCtx, ssh, out, "docker exec lldp lldpcli show statistics")
	runOnTarget(captureCtx, ssh, out, "docker exec lldp lldpcli show chassis")

	writeCapture(outDir, name, out.String())
}

func (c *Config) captureServerLLDP(ctx context.Context, vlab *VLAB, name, outDir string) {
	captureCtx, cancel := context.WithTimeout(ctx, lldpCapturePerTargetTimeout)
	defer cancel()

	ssh, err := c.SSH(captureCtx, vlab, name)
	if err != nil {
		slog.Warn("LLDP capture: ssh config unavailable", "server", name, "err", err)

		return
	}

	out := &strings.Builder{}
	fmt.Fprintf(out, "=== LLDP inspect failure capture: server %s at %s ===\n",
		name, time.Now().UTC().Format(time.RFC3339Nano))

	runOnTarget(captureCtx, ssh, out, "networkctl lldp")
	runOnTarget(captureCtx, ssh, out, "networkctl status")
	runOnTarget(captureCtx, ssh, out, "ip -d link show")
	runOnTarget(captureCtx, ssh, out, "sudo journalctl -u systemd-networkd -n 300 --no-pager")

	writeCapture(outDir, name, out.String())
}

func runOnTarget(ctx context.Context, ssh *sshutil.Config, out *strings.Builder, cmd string) {
	fmt.Fprintf(out, "\n=== Executing: %s ===\n", cmd)
	stdout, stderr, err := ssh.Run(ctx, cmd)
	out.WriteString(stdout)
	if stderr != "" {
		fmt.Fprintf(out, "\n--- stderr ---\n%s", stderr)
	}
	if err != nil {
		fmt.Fprintf(out, "\n--- error: %v ---\n", err)
	}
}

func writeCapture(outDir, name, content string) {
	path := filepath.Join(outDir, name+".log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		slog.Warn("LLDP capture: failed to write file", "path", path, "err", err)
	}
}
