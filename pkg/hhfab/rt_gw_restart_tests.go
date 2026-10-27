// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gwPodNameLabel      = "app.kubernetes.io/name"
	gwPodRestartTimeout = 5 * time.Minute
	gwPodPollInterval   = 5 * time.Second
)

// restartGatewayPods deletes the pod of every gateway and waits for the daemonset to bring up ready replacements. The
// gateway controller labels the pods with the daemonset name.
func restartGatewayPods(ctx context.Context, kube kclient.Client) error {
	gws := &gwapi.GatewayList{}
	if err := kube.List(ctx, gws); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}
	if len(gws.Items) == 0 {
		return fmt.Errorf("no gateways found") //nolint:err113
	}

	gwNames := []string{}
	killed := map[ktypes.UID]bool{}
	for _, gw := range gws.Items {
		pods, err := listGatewayPods(ctx, kube, gw.Name)
		if err != nil {
			return err
		}
		if len(pods) == 0 {
			return fmt.Errorf("no pods found for gateway %s", gw.Name) //nolint:err113
		}
		gwNames = append(gwNames, gw.Name)

		for i := range pods {
			pod := &pods[i]
			killed[pod.UID] = true
			if err := kube.Delete(ctx, pod); err != nil {
				return fmt.Errorf("deleting pod %s: %w", pod.Name, err)
			}
			slog.Debug("Killed gateway pod", "name", pod.Name, "UID", pod.UID)
		}
	}
	slog.Info("Killed gateway pods", "gateways", len(gwNames), "pods", len(killed))

	start := time.Now()
	for {
		notReady := []string{}
		for _, gwName := range gwNames {
			pods, err := listGatewayPods(ctx, kube, gwName)
			if err != nil {
				return err
			}

			pending := slices.ContainsFunc(pods, func(pod corev1.Pod) bool {
				return killed[pod.UID] || !podReady(&pod)
			})
			if len(pods) == 0 || pending {
				notReady = append(notReady, gwName)
			}
		}

		if len(notReady) == 0 {
			slog.Info("Gateway pods back up", "took", time.Since(start))

			return nil
		}

		if time.Since(start) > gwPodRestartTimeout {
			return fmt.Errorf("timed out waiting for pods to restart on gateways %v", notReady) //nolint:err113
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for gateway pods to restart: %w", ctx.Err())
		case <-time.After(gwPodPollInterval):
		}
	}
}

// gatewayPodRestartTest peers two VPCs through the gateway and checks that
// connectivity survives killing the gateway pods, which run the dataplane and
// FRR together, as we have hit regressions where a restart broke the gateway.
func gatewayPodRestartTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	skipped, reverts, err := testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name:      "gateway pod restart",
		BuildSpec: gatewayPeeringSpec,
	})
	if skipped || err != nil {
		return skipped, reverts, err
	}

	vpc1, vpc2, err := firstTwoVPCs(ctx, testCtx.kube)
	if err != nil {
		return false, nil, fmt.Errorf("gateway pod restart: %w", err)
	}

	tcOpts := testCtx.tcOpts
	tcOpts.Sources = natTestProbeServers(matrix, vpc1.Name, vpc2.Name)
	tcOpts.Destinations = tcOpts.Sources

	if err := restartGatewayPods(ctx, testCtx.kube); err != nil {
		return false, nil, fmt.Errorf("restarting gateway pods: %w", err)
	}
	if err := testCtx.checkGatewayTraffic(ctx, matrix, tcOpts); err != nil {
		return false, nil, fmt.Errorf("after restart: %w", err)
	}

	return false, nil, nil
}

func getGatewayRestartTestCases() []JUnitTestCase {
	return []JUnitTestCase{
		{Name: "Gateway Pod Restart", F: gatewayPodRestartTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Driver Toggle", F: gatewayDriverToggleTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
	}
}
