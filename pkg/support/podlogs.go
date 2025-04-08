// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/samber/lo"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func collectPodLogs(ctx context.Context) (map[string]map[string]PodLogs, error) {
	logs := map[string]map[string]PodLogs{}

	clientset, err := kubeutil.NewClientset(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	pods, err := clientset.CoreV1().Pods("").List(ctx, kmetav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	for _, pod := range pods.Items {
		for _, containerName := range lo.Map(slices.Concat(pod.Spec.Containers, pod.Spec.InitContainers),
			func(c corev1.Container, _ int) string { return c.Name }) {
			req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				// Previous: true, // TODO collect previous logs as well
				Container: containerName,
			})
			logsStream, err := req.Stream(ctx)
			if err != nil {
				return nil, fmt.Errorf("getting pod logs: %w", err)
				// slog.Warn("getting pod logs", "pod", pod.Name, "err", err)
				// continue
			}
			defer logsStream.Close()

			buf := &bytes.Buffer{}
			if _, err := io.Copy(buf, logsStream); err != nil {
				return nil, fmt.Errorf("copying pod logs: %w", err)
			}

			// fmt.Printf("pod %s/%s/%s logs:\n%s\n ", pod.Namespace, pod.Name, containerName, buf.String())

			if _, ok := logs[pod.Namespace]; !ok {
				logs[pod.Namespace] = map[string]PodLogs{}
			}
			if _, ok := logs[pod.Namespace][pod.Name]; !ok {
				logs[pod.Namespace][pod.Name] = PodLogs{}
			}

			logs[pod.Namespace][pod.Name][containerName] = ContainerLogs{
				Current: buf.Bytes(),
			}
		}
	}

	return logs, nil
}
