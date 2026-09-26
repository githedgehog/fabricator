// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.githedgehog.com/fabricator/pkg/util/sshutil"
)

// containerLogFollowTargets are the container name filters continuously
// followed for the duration of a diagnostics-enabled run, on every gateway
// VM. Scoped to dataplane+FRR: these are the containers whose kubelet-
// managed logs get evicted by rotation long before a post-failure show-tech
// collection ever runs on a busy multi-suite job (fabricator#1482, #2043).
var containerLogFollowTargets = []struct {
	ContainerName string
	FileSuffix    string
}{
	{"dataplane", "dataplane"},
	{"frr", "frr"},
}

// startContainerLogFollowers launches a background, auto-reconnecting
// `crictl logs -f` follower per (gateway VM, container) target, writing
// continuously to <outDir>/live-logs/<vm>-<suffix>.log. Unlike the
// post-failure show-tech collector, this never depends on kubelet's rotated
// files still being on disk at collection time - it is a live stream that
// keeps going regardless of what happens to older log files, mirroring the
// standalone tail-crictl.sh tool already used for manual live debugging.
// Returns a stop function; safe to call even if some followers failed to
// start, and safe to call more than once.
func (c *Config) startContainerLogFollowers(ctx context.Context, vlab *VLAB, outDir string) (func(), error) {
	logDir := filepath.Join(outDir, "live-logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return func() {}, fmt.Errorf("creating live-logs directory: %w", err)
	}

	followCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup

	for _, vm := range vlab.VMs {
		if vm.Type != VMTypeGateway {
			continue
		}

		for _, target := range containerLogFollowTargets {
			ssh, err := c.SSH(followCtx, vlab, vm.Name)
			if err != nil {
				slog.Warn("Failed to set up log follower ssh; skipping", "vm", vm.Name, "container", target.ContainerName, "err", err)

				continue
			}

			localPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", vm.Name, target.FileSuffix))

			wg.Add(1)
			go func(nodeName, containerName, path string) {
				defer wg.Done()
				followContainerLog(followCtx, ssh, nodeName, containerName, path)
			}(vm.Name, target.ContainerName, localPath)
		}
	}

	stop := func() {
		cancel()
		wg.Wait()
	}

	return stop, nil
}

// followContainerLog runs the resolve-then-stream-then-reconnect loop for a
// single container until ctx is cancelled. The container ID is re-resolved
// on every (re)connect, not cached once, in case whatever caused the
// disconnect (a pod restart, in particular) minted a new container ID.
func followContainerLog(ctx context.Context, ssh *sshutil.Config, nodeName, containerName, localPath string) {
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Warn("Failed to open log follower output", "path", localPath, "err", err)

		return
	}
	defer f.Close()

	logLine := func(format string, args ...any) {
		fmt.Fprintf(f, "[log-follower] %s %s\n", time.Now().UTC().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))
	}

	psCmd := fmt.Sprintf("sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps -q --name %q", containerName)

	for ctx.Err() == nil {
		out, _, err := ssh.Run(ctx, psCmd)
		cid := strings.TrimSpace(out)
		if idx := strings.LastIndexByte(cid, '\n'); idx >= 0 {
			cid = cid[idx+1:] // rare: more than one match, keep the most recent
		}
		if err != nil || cid == "" {
			logLine("could not resolve container id for %s/%s, retrying in 3s", nodeName, containerName)
			if !sleepOrDone(ctx, 3*time.Second) {
				return
			}

			continue
		}

		logLine("(re)connecting to %s/%s (cid %s)", nodeName, containerName, cid)
		logCmd := fmt.Sprintf("sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock logs -f --timestamps %s", cid)
		streamErr := ssh.StreamLog(ctx, logCmd, "", func(msg string, _ ...any) {
			fmt.Fprintln(f, strings.TrimPrefix(msg, ": "))
		})
		if ctx.Err() != nil {
			return
		}

		logLine("disconnected from %s/%s (cid %s): %v, retrying in 3s", nodeName, containerName, cid, streamErr)
		if !sleepOrDone(ctx, 3*time.Second) {
			return
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
