// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/samber/lo"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	corev1 "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

func collectPodLogs(ctx context.Context, dump *Dump, kubeconfigPath string) error {
	logs := map[string]map[string]PodLogs{}

	clientset, err := kubeutil.NewClientset(ctx, kubeconfigPath)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	var pods *corev1.PodList
	if err := retry.OnError(longBackoff, func(err error) bool { return true }, func() error {
		pods, err = clientset.CoreV1().Pods("").List(ctx, kmetav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing pods: %w", err)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("retrying: %w", err)
	}

	for _, pod := range pods.Items {
		if os.Getenv("GITHUB_ACTIONS") != githubActionsValue {
			slog.Debug("Collecting Kube pod logs", "pod", pod.Name, "namespace", pod.Namespace)
		}

		for _, container := range lo.Map(slices.Concat(pod.Spec.Containers, pod.Spec.InitContainers),
			func(c corev1.Container, _ int) string { return c.Name }) {
			current, err := getPodContainerLogs(ctx, clientset, pod.Namespace, pod.Name, container, false)
			if err != nil {
				return fmt.Errorf("getting pod %s/%s container %s current logs: %w", pod.Namespace, pod.Name, container, err)
			}

			previous, err := getPodContainerLogs(ctx, clientset, pod.Namespace, pod.Name, container, true)
			if err != nil {
				return fmt.Errorf("getting pod %s/%s container %s previous logs: %w", pod.Namespace, pod.Name, container, err)
			}

			if len(current) == 0 && len(previous) == 0 {
				continue
			}

			if _, ok := logs[pod.Namespace]; !ok {
				logs[pod.Namespace] = map[string]PodLogs{}
			}
			if _, ok := logs[pod.Namespace][pod.Name]; !ok {
				logs[pod.Namespace][pod.Name] = PodLogs{}
			}

			logs[pod.Namespace][pod.Name][container] = ContainerLogs{
				Current:  string(current),
				Previous: string(previous),
			}
		}
	}

	dump.PodLogs = logs

	return nil
}

func getPodContainerLogs(ctx context.Context, clientset *kubernetes.Clientset, ns, pod, container string, previous bool) ([]byte, error) {
	res := &bytes.Buffer{}

	if err := retry.OnError(longBackoff, func(err error) bool { return true }, func() error {
		res.Reset()

		req := clientset.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
			Container: container,
			Previous:  previous,
		})
		logsStream, err := req.Stream(ctx)
		if err != nil {
			if kapierrors.IsNotFound(err) || strings.Contains(err.Error(), "proxy error") || strings.HasSuffix(err.Error(), "not found") {
				return nil
			}

			return fmt.Errorf("getting pod logs: %w", err)
		}
		defer logsStream.Close()

		if _, err := io.Copy(res, logsStream); err != nil {
			return fmt.Errorf("copying pod logs: %w", err)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("retrying: %w", err)
	}

	return res.Bytes(), nil
}
