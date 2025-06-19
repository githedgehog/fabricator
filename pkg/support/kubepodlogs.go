// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"

	"github.com/samber/lo"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func collectPodLogs(ctx context.Context, dump *Dump) error {
	logs := map[string]map[string]PodLogs{}

	clientset, err := kubeutil.NewClientset(ctx, "")
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	pods, err := clientset.CoreV1().Pods("").List(ctx, kmetav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing pods: %w", err)
	}
	for _, pod := range pods.Items {
		slog.Debug("Collecting Kube pod logs", "pod", pod.Name, "namespace", pod.Namespace)

		for _, container := range lo.Map(slices.Concat(pod.Spec.Containers, pod.Spec.InitContainers),
			func(c corev1.Container, _ int) string { return c.Name }) {
			current, err := getPodContainerLogs(ctx, clientset, pod.Namespace, pod.Name, container)
			if err != nil {
				slog.Warn("Error getting current pod container logs, skipping", "pod", pod.Name, "container", container, "namespace", pod.Namespace, "err", err)

				continue
			}

			if _, ok := logs[pod.Namespace]; !ok {
				logs[pod.Namespace] = map[string]PodLogs{}
			}
			if _, ok := logs[pod.Namespace][pod.Name]; !ok {
				logs[pod.Namespace][pod.Name] = PodLogs{}
			}

			logs[pod.Namespace][pod.Name][container] = ContainerLogs{
				Current: string(current),
			}
		}
	}

	dump.PodLogs = logs

	return nil
}

func getPodContainerLogs(ctx context.Context, clientset *kubernetes.Clientset, ns, pod, container string) ([]byte, error) {
	req := clientset.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
	})
	logsStream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting pod logs: %w", err)
	}
	defer logsStream.Close()

	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, logsStream); err != nil {
		return nil, fmt.Errorf("copying pod logs: %w", err)
	}

	return buf.Bytes(), nil
}
