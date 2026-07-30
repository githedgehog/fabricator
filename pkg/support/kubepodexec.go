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
	if err := retry.OnError(longBackoff, func(err error) bool { return true }, func() error {
		pods, err = clientset.CoreV1().Pods(comp.FabNamespace).List(ctx, kmetav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing pods: %w", err)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("retrying: %w", err)
	}

	for _, pod := range pods.Items {
		// TODO: better ways to detect correct pods

		if !strings.HasPrefix(pod.Name, "gw--") {
			continue
		}

		gwName, ok := gatewayName(pod.Name)
		if !ok {
			slog.Warn("Failed to extract gateway name, skipping", "pod", pod.Name)

			continue
		}

		gw, ok := insights[gwName]
		if !ok {
			gw = map[string]ExecOutputs{}
		}

		switch {
		// Collect dataplane insights
		case strings.Contains(pod.Name, "--dataplane-"):
			if !c.quiet {
				slog.Debug("Collecting dataplane insights", "pod", pod.Name)
			}

			for _, cmd := range []string{
				"show tech",
			} {
				if !c.quiet {
					slog.Debug("Executing dataplane/cli", "pod", pod.Name, "cmd", cmd)
				}

				stdout, stderr, err := execPodContainerCommand(ctx, clientset, cfg,
					pod.Namespace, pod.Name, "dataplane",
					[]string{"/dataplane-cli", "-c", cmd})
				errStr := ""
				if err != nil {
					slog.Error("Failed to exec dataplane/cli", "pod", pod.Name, "cmd", cmd, "err", err)
					errStr = err.Error()
				}
				gw["dataplane: "+cmd] = ExecOutputs{Stdout: stdout, Stderr: stderr, Error: errStr}
			}

		// Collect FRR insights
		case strings.Contains(pod.Name, "--frr-"):
			if !c.quiet {
				slog.Debug("Collecting frr insights", "pod", pod.Name)
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
				if !c.quiet {
					slog.Debug("Executing frr/vtysh", "pod", pod.Name, "cmd", cmd)
				}
				stdout, stderr, err := execPodContainerCommand(ctx, clientset, cfg,
					pod.Namespace, pod.Name, "frr",
					[]string{"vtysh", "-X", "/lib/libvtysh_hedgehog.so", "-c", cmd})
				errStr := ""
				if err != nil {
					slog.Error("Failed to exec frr/vtysh", "pod", pod.Name, "cmd", cmd, "err", err)
					errStr = err.Error()
				}
				gw["frr: "+cmd] = ExecOutputs{Stdout: stdout, Stderr: stderr, Error: errStr}
			}
		}

		insights[gwName] = gw
	}

	dump.GatewayInsights = insights

	return nil
}

// gatewayName extracts the gateway name — everything between the first "--" and the last "--" in a pod name like
// "gw--he-f2-gw-1--dataplane-wcbjv"
func gatewayName(s string) (string, bool) {
	const sep = "--"
	first := strings.Index(s, sep)
	last := strings.LastIndex(s, sep)
	if first < 0 || last <= first {
		return "", false
	}

	return s[first+len(sep) : last], true
}

func execPodContainerCommand(ctx context.Context, clientset *kubernetes.Clientset, cfg *rest.Config, ns, pod, container string, command []string) (string, string, error) {
	var outBuf, errBuf bytes.Buffer

	if err := retry.OnError(longBackoff, func(err error) bool { return true }, func() error {
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
