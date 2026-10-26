// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
)

const gatewayDataplaneContainer = "dataplane"

func (c collector) collectGatewayInsights(ctx context.Context, dump *Dump) error {
	insights := map[string]map[string]ExecOutputs{}

	cfg, err := kubeutil.NewClientConfig(ctx, c.kubeconfigPath)
	if err != nil {
		return fmt.Errorf("creating kube config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	var pods *corev1.PodList
	if err := retry.OnError(longBackoff, retriable(ctx), func() error {
		pods, err = clientset.CoreV1().Pods(comp.FabNamespace).List(ctx, kmetav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing pods: %w", err)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("retrying: %w", err)
	}

	for _, pod := range pods.Items {
		gwName, ok := strings.CutPrefix(pod.Labels["app.kubernetes.io/name"], gwapi.DaemonSetName(""))
		if !ok {
			continue
		}

		if !c.quiet {
			slog.Debug("Collecting gateway insights", "pod", pod.Name)
		}

		gw := map[string]ExecOutputs{}

		skip := gatewayExecSkipReason(pod)
		if skip != "" {
			slog.Warn("Skipping gateway insights", "pod", pod.Name, "reason", skip)
		}
		exec := func(command ...string) ExecOutputs {
			if skip != "" {
				return ExecOutputs{Error: "skipped: " + skip}
			}

			return execGatewayCommand(ctx, clientset, cfg, pod, c.quiet, command...)
		}

		// show tech includes everything the dataplane cli shows, e.g. transceivers, link and lldp neighbors
		for _, cmd := range []string{
			"show tech",
		} {
			gw["dataplane: "+cmd] = exec("/dataplane-cli", "-c", cmd)
		}

		for _, cmd := range []string{
			"show daemons",
			"show hedgehog rpc stats",
			"show zebra client summary",
			"show running-config",
			"show interface brief",
			"show bfd peers",
			"show bgp summary",
			"show ip route vrf all",
			"show ip fib vrf all",
		} {
			gw["frr: "+cmd] = exec("vtysh", "-c", cmd)
		}

		insights[gwName] = gw
	}

	dump.GatewayInsights = insights

	return nil
}

// gatewayExecSkipReason returns why execs into the dataplane container can't succeed within the retries, e.g. it's
// crashlooping or the pod isn't schedulable, or nothing so other failures are retried and recorded as usual
func gatewayExecSkipReason(pod corev1.Pod) string {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == corev1.PodReasonUnschedulable {
			return "pod unschedulable: " + cond.Message
		}
	}

	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != gatewayDataplaneContainer || status.State.Waiting == nil {
			continue
		}

		switch reason := status.State.Waiting.Reason; reason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "CreateContainerError", "InvalidImageName":
			return "dataplane container " + reason + ": " + status.State.Waiting.Message
		}
	}

	return ""
}

func execGatewayCommand(ctx context.Context, clientset *kubernetes.Clientset, cfg *rest.Config, pod corev1.Pod, quiet bool, command ...string) ExecOutputs {
	if !quiet {
		slog.Debug("Executing in gateway dataplane", "pod", pod.Name, "cmd", command)
	}

	stdout, stderr, err := execPodContainerCommand(ctx, clientset, cfg, pod.Namespace, pod.Name, gatewayDataplaneContainer, command)
	errStr := ""
	if err != nil {
		slog.Error("Failed to exec in gateway dataplane", "pod", pod.Name, "cmd", command, "err", err)
		errStr = err.Error()
	}

	return ExecOutputs{Stdout: stdout, Stderr: stderr, Error: errStr}
}

func execPodContainerCommand(ctx context.Context, clientset *kubernetes.Clientset, cfg *rest.Config, ns, pod, container string, command []string) (string, string, error) {
	var outBuf, errBuf bytes.Buffer

	if err := retry.OnError(longBackoff, retriable(ctx), func() error {
		outBuf.Reset()
		errBuf.Reset()

		req := clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Namespace(ns).
			Name(pod).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: container,
				Command:   command,
				Stdout:    true,
				Stderr:    true,
				TTY:       false,
			}, scheme.ParameterCodec)

		exec, err := remotecommand.NewWebSocketExecutor(cfg, http.MethodGet, req.URL().String())
		if err != nil {
			return fmt.Errorf("creating executor: %w", err)
		}

		if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: &outBuf,
			Stderr: &errBuf,
			Tty:    false,
		}); err != nil {
			return fmt.Errorf("executing %q command in pod: %w", strings.Join(command, " "), err)
		}

		return nil
	}); err != nil {
		return outBuf.String(), errBuf.String(), fmt.Errorf("retrying: %w", err)
	}

	return outBuf.String(), errBuf.String(), nil
}
