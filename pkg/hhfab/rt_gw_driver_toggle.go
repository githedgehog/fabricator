// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	corev1 "k8s.io/api/core/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gwDataplaneContainer = "dataplane"
	// the dataplane logs this line with both drivers, with the kernel one it initializes the EAL for the packet
	// classifier only, and without PCI devices
	gwEALLogMarker = "Initializing DPDK EAL with:"
	gwEALNoPCIArg  = "--no-pci"
	// a new pod has to be ready and not restart for this long, a crash looping dataplane is ready for a moment
	gwToggleStableFor = 20 * time.Second
	gwEALLogTimeout   = time.Minute
	// the longest a gateway can take to switch: the pods replaced, stable and the log checked
	gwToggleTimeout = gwPodRestartTimeout + gwToggleStableFor + gwEALLogTimeout
	// the traffic check after a toggle only has to show that traffic flows through the gateway, not how fast
	gwToggleIPerfSeconds  = 5
	gwToggleIPerfMinSpeed = 100
)

// dataplaneDriver returns the value of the --driver argument of the dataplane container, or an empty string.
func dataplaneDriver(pod *corev1.Pod) string {
	for _, container := range pod.Spec.Containers {
		if container.Name != gwDataplaneContainer {
			continue
		}

		for idx, arg := range container.Args {
			if val, ok := strings.CutPrefix(arg, "--driver="); ok {
				return val
			}
			if arg == "--driver" && idx+1 < len(container.Args) {
				return container.Args[idx+1]
			}
		}
	}

	return ""
}

// gatewayPodsOnDriver checks that the gateway runs only new, ready pods whose dataplane was started with the driver. It
// returns what is still pending otherwise. Old pods must be gone, new pods that are being deleted are ignored.
func gatewayPodsOnDriver(pods []corev1.Pod, old map[ktypes.UID]bool, driver gwapi.GatewayDriver) (bool, string) {
	live := 0
	for idx := range pods {
		pod := &pods[idx]
		if old[pod.UID] && pod.DeletionTimestamp != nil {
			return false, fmt.Sprintf("old pod %s is still terminating", pod.Name)
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		live++

		switch {
		case old[pod.UID]:
			return false, fmt.Sprintf("pod %s is not replaced yet", pod.Name)
		case !podReady(pod):
			return false, fmt.Sprintf("pod %s is not ready", pod.Name)
		}
		if got := dataplaneDriver(pod); got != string(driver) {
			return false, fmt.Sprintf("pod %s runs the dataplane with driver %q", pod.Name, got)
		}
	}

	if live == 0 {
		return false, "no pods"
	}

	return true, ""
}

// gatewayPodCrash returns an error if a container of the new, live pods restarted, which means it crashed. The show-tech
// of the failed test has its logs.
func gatewayPodCrash(pods []corev1.Pod, old map[ktypes.UID]bool) error {
	for idx := range pods {
		pod := &pods[idx]
		if pod.DeletionTimestamp != nil || old[pod.UID] {
			continue
		}

		for _, status := range pod.Status.ContainerStatuses {
			if status.RestartCount == 0 {
				continue
			}

			term := status.LastTerminationState.Terminated
			if term == nil {
				term = &corev1.ContainerStateTerminated{}
			}

			return fmt.Errorf("container %s of pod %s restarted %d times, last exit %s code %d", //nolint:err113
				status.Name, pod.Name, status.RestartCount, term.Reason, term.ExitCode)
		}
	}

	return nil
}

// logDriver returns the driver the dataplane log says was started, or an empty string if it has no EAL line yet.
func logDriver(logs string) gwapi.GatewayDriver {
	for line := range strings.SplitSeq(logs, "\n") {
		if !strings.Contains(line, gwEALLogMarker) {
			continue
		}
		if strings.Contains(line, gwEALNoPCIArg) {
			return gwapi.GatewayDriverKernel
		}

		return gwapi.GatewayDriverDPDK
	}

	return ""
}

// checkGatewayLogs waits for the EAL line in the dataplane log of every live pod and checks that it is the one of the
// driver. The line is an info message, so it is not checked if the gateway doesn't log those.
func checkGatewayLogs(ctx context.Context, clientset *kubernetes.Clientset, gw *gwapi.Gateway, pods []corev1.Pod, driver gwapi.GatewayDriver) error {
	if slices.Contains([]gwapi.GatewayLogLevel{gwapi.GatewayLogLevelOff, gwapi.GatewayLogLevelError, gwapi.GatewayLogLevelWarning}, gw.Spec.Logs.Default) {
		slog.Warn("Skipping the dataplane log check, the gateway doesn't log info messages", "gateway", gw.Name, "level", gw.Spec.Logs.Default)

		return nil
	}

	for idx := range pods {
		pod := &pods[idx]
		if pod.DeletionTimestamp != nil {
			continue
		}

		deadline := time.Now().Add(gwEALLogTimeout)
		for {
			stream, err := clientset.CoreV1().Pods(comp.FabNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: gwDataplaneContainer}).Stream(ctx)
			if err != nil {
				return fmt.Errorf("getting dataplane logs of pod %s: %w", pod.Name, err)
			}
			logs, err := io.ReadAll(stream)
			stream.Close()
			if err != nil {
				return fmt.Errorf("reading dataplane logs of pod %s: %w", pod.Name, err)
			}

			if got := logDriver(string(logs)); got != "" {
				if got != driver {
					return fmt.Errorf("pod %s: dataplane log says the %s driver was started, not the %s one", pod.Name, got, driver) //nolint:err113
				}

				break
			}

			if time.Now().After(deadline) {
				return fmt.Errorf("pod %s: dataplane log has no %q line after %s", pod.Name, gwEALLogMarker, gwEALLogTimeout) //nolint:err113
			}

			select {
			case <-ctx.Done():
				return fmt.Errorf("waiting for the dataplane log of pod %s: %w", pod.Name, ctx.Err())
			case <-time.After(gwPodPollInterval):
			}
		}
	}

	return nil
}

// changeGatewayDriver sets the driver of the gateway, waits for the daemonset to replace its pods and checks that the new
// ones run the driver: they are ready and don't restart for a while, their dataplane is started with it and its log
// says so.
func changeGatewayDriver(ctx context.Context, kube kclient.Client, clientset *kubernetes.Clientset, gw *gwapi.Gateway, driver gwapi.GatewayDriver) error {
	pods, err := listGatewayPods(ctx, kube, gw.Name)
	if err != nil {
		return err
	}
	old := map[ktypes.UID]bool{}
	for idx := range pods {
		old[pods[idx].UID] = true
	}

	slog.Info("Switching gateway driver", "gateway", gw.Name, "driver", driver)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &gwapi.Gateway{}
		if err := kube.Get(ctx, kclient.ObjectKeyFromObject(gw), cur); err != nil {
			return fmt.Errorf("getting: %w", err)
		}

		cur.Spec.Driver = driver

		return kube.Update(ctx, cur)
	}); err != nil {
		return fmt.Errorf("setting driver of gateway %s to %s: %w", gw.Name, driver, err)
	}

	start := time.Now()
	var stableSince time.Time
	for {
		pods, err = listGatewayPods(ctx, kube, gw.Name)
		if err != nil {
			return err
		}

		if err := gatewayPodCrash(pods, old); err != nil {
			return fmt.Errorf("gateway %s with the %s driver: %w", gw.Name, driver, err)
		}

		done, pending := gatewayPodsOnDriver(pods, old, driver)
		switch {
		case done && stableSince.IsZero():
			stableSince = time.Now()
		case done && time.Since(stableSince) >= gwToggleStableFor:
			slog.Info("Gateway runs the driver", "gateway", gw.Name, "driver", driver, "took", time.Since(start))

			if err := checkGatewayLogs(ctx, clientset, gw, pods, driver); err != nil {
				return fmt.Errorf("gateway %s with the %s driver: %w", gw.Name, driver, err)
			}

			return nil
		case !done:
			stableSince = time.Time{}
			if time.Since(start) > gwPodRestartTimeout {
				return fmt.Errorf("timed out waiting for gateway %s to run the %s driver: %s", gw.Name, driver, pending) //nolint:err113
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for gateway %s to run the %s driver: %w", gw.Name, driver, ctx.Err())
		case <-time.After(gwPodPollInterval):
		}
	}
}

// gatewayDriverToggleTest switches the driver of the gateways one at a time, like an operator would to keep the gateway
// group up, to the other driver and back. After every switch it checks the driver the gateway runs and that traffic
// flows through the gateway peering.
func gatewayDriverToggleTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	gws := &gwapi.GatewayList{}
	if err := testCtx.kube.List(ctx, gws); err != nil {
		return false, nil, fmt.Errorf("listing gateways: %w", err)
	}
	if len(gws.Items) == 0 {
		return true, nil, fmt.Errorf("no gateways found") //nolint:err113
	}
	slices.SortFunc(gws.Items, func(a, b gwapi.Gateway) int { return strings.Compare(a.Name, b.Name) })

	initial := map[string]gwapi.GatewayDriver{}
	target := map[string]gwapi.GatewayDriver{}
	for idx := range gws.Items {
		gw := &gws.Items[idx]

		other := gwapi.GatewayDriverDPDK
		switch gw.Spec.Driver {
		case gwapi.GatewayDriverKernel:
		case gwapi.GatewayDriverDPDK:
			other = gwapi.GatewayDriverKernel
		default:
			return false, nil, fmt.Errorf("gateway %s: unknown driver %q", gw.Name, gw.Spec.Driver) //nolint:err113
		}

		// the webhook only requires the PCI addresses of the interfaces for the dpdk driver, the kernel one falls back
		// to the interface names
		if other == gwapi.GatewayDriverDPDK {
			for _, name := range slices.Sorted(maps.Keys(gw.Spec.Interfaces)) {
				if gw.Spec.Interfaces[name].PCI == "" {
					return true, nil, fmt.Errorf("gateway %s can't switch to the dpdk driver: interface %s has no PCI address", gw.Name, name) //nolint:err113
				}
			}
		}

		initial[gw.Name] = gw.Spec.Driver
		target[gw.Name] = other
	}

	clientset, err := kubeutil.NewClientset(ctx, filepath.Join(testCtx.vlabCfg.WorkDir, VLABDir, VLABKubeConfig))
	if err != nil {
		return false, nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	skipped, reverts, err := testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name:      "gateway driver toggle",
		BuildSpec: gatewayPeeringSpec,
	})
	if skipped || err != nil {
		return skipped, reverts, err
	}

	vpc1, vpc2, err := firstTwoVPCs(ctx, testCtx.kube)
	if err != nil {
		return false, nil, fmt.Errorf("gateway driver toggle: %w", err)
	}

	tcOpts := testCtx.tcOpts
	tcOpts.Sources = natTestProbeServers(matrix, vpc1.Name, vpc2.Name)
	tcOpts.Destinations = tcOpts.Sources
	tcOpts.IPerfsSeconds = min(tcOpts.IPerfsSeconds, gwToggleIPerfSeconds)
	tcOpts.IPerfsMinSpeed = min(tcOpts.IPerfsMinSpeed, gwToggleIPerfMinSpeed)

	// puts the gateways back on their drivers if the test fails half way, even if the run was cancelled
	reverts = append(reverts, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(len(gws.Items))*gwToggleTimeout)
		defer cancel()

		for idx := range gws.Items {
			gw := &gws.Items[idx]

			current := &gwapi.Gateway{}
			if err := testCtx.kube.Get(ctx, kclient.ObjectKeyFromObject(gw), current); err != nil {
				return fmt.Errorf("getting gateway %s: %w", gw.Name, err)
			}
			if current.Spec.Driver == initial[gw.Name] {
				continue
			}

			if err := changeGatewayDriver(ctx, testCtx.kube, clientset, current, initial[gw.Name]); err != nil {
				return fmt.Errorf("restoring the driver of gateway %s: %w", gw.Name, err)
			}
		}

		return nil
	})

	for _, drivers := range []map[string]gwapi.GatewayDriver{target, initial} {
		for idx := range gws.Items {
			gw := &gws.Items[idx]

			current := &gwapi.Gateway{}
			if err := testCtx.kube.Get(ctx, kclient.ObjectKeyFromObject(gw), current); err != nil {
				return false, reverts, fmt.Errorf("getting gateway %s: %w", gw.Name, err)
			}

			if err := changeGatewayDriver(ctx, testCtx.kube, clientset, current, drivers[gw.Name]); err != nil {
				return false, reverts, fmt.Errorf("switching gateway %s to the %s driver: %w", gw.Name, drivers[gw.Name], err)
			}
			if err := testCtx.checkGatewayTraffic(ctx, matrix, tcOpts); err != nil {
				return false, reverts, fmt.Errorf("after switching gateway %s to the %s driver: %w", gw.Name, drivers[gw.Name], err)
			}
		}
	}

	return false, reverts, nil
}
