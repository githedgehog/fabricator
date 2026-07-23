// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	coreapi "k8s.io/api/core/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type InspectPodsOpts struct {
	Namespace string
	Strict    bool
}

// helm-install-* are Jobs that track helm's own retry loop, not real restarts.
const helmInstallPrefix = "helm-install-"

type containerRestart struct {
	pod        string
	container  string
	restarts   int32
	lastReason string
}

func (c *Config) InspectPods(ctx context.Context, opts InspectPodsOpts) error {
	if opts.Namespace == "" {
		opts.Namespace = fabapi.FabNamespace
	}

	slog.Info("Inspecting pods for restarts", "namespace", opts.Namespace, "strict", opts.Strict)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	pods := &coreapi.PodList{}
	if err := kube.List(ctx, pods, kclient.InNamespace(opts.Namespace)); err != nil {
		return fmt.Errorf("listing pods in namespace %q: %w", opts.Namespace, err)
	}

	var restarts []containerRestart
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.Name, helmInstallPrefix) {
			continue
		}

		var statuses []coreapi.ContainerStatus
		statuses = append(statuses, pod.Status.InitContainerStatuses...)
		statuses = append(statuses, pod.Status.ContainerStatuses...)
		for _, cs := range statuses {
			if cs.RestartCount == 0 {
				continue
			}

			lastReason := ""
			if cs.LastTerminationState.Terminated != nil {
				lastReason = cs.LastTerminationState.Terminated.Reason
			}

			restarts = append(restarts, containerRestart{
				pod:        pod.Name,
				container:  cs.Name,
				restarts:   cs.RestartCount,
				lastReason: lastReason,
			})
		}
	}

	if len(restarts) == 0 {
		slog.Info("No container restarts detected", "namespace", opts.Namespace)

		return nil
	}

	slices.SortFunc(restarts, func(a, b containerRestart) int {
		return strings.Compare(a.pod+"/"+a.container, b.pod+"/"+b.container)
	})

	logf := slog.Warn
	if opts.Strict {
		logf = slog.Error
	}

	logf("Detected unexpected container restarts", "namespace", opts.Namespace, "containers", len(restarts))
	for _, r := range restarts {
		logf("Container restarted", "pod", r.pod, "container", r.container, "restarts", r.restarts, "lastReason", r.lastReason)
	}

	if opts.Strict {
		return fmt.Errorf("found %d container(s) with restarts in namespace %q", len(restarts), opts.Namespace) //nolint:err113
	}

	return nil
}
