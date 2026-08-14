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
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	corev1 "k8s.io/api/core/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gwPodNameLabel      = "app.kubernetes.io/name"
	gwPodRestartTimeout = 5 * time.Minute
	gwPodPollInterval   = 5 * time.Second
)

// restartGatewayPods deletes the pods of the given gateway component
// ("dataplane" or "frr") on every gateway and waits for the daemonset to bring
// up ready replacements. The gateway controller names the daemonsets
// gw--<gateway>--<component> and labels their pods with that same name.
func restartGatewayPods(ctx context.Context, kube kclient.Client, component string) error {
	gws := &gwapi.GatewayList{}
	if err := kube.List(ctx, gws); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}
	if len(gws.Items) == 0 {
		return fmt.Errorf("no gateways found") //nolint:goerr113
	}

	selectors := map[string]kclient.MatchingLabels{}
	killed := map[ktypes.UID]bool{}
	for _, gw := range gws.Items {
		sel := kclient.MatchingLabels{gwPodNameLabel: fmt.Sprintf("gw--%s--%s", gw.Name, component)}
		selectors[gw.Name] = sel

		pods := &corev1.PodList{}
		if err := kube.List(ctx, pods, kclient.InNamespace(comp.FabNamespace), sel); err != nil {
			return fmt.Errorf("listing %s pods of gateway %s: %w", component, gw.Name, err)
		}
		if len(pods.Items) == 0 {
			return fmt.Errorf("no %s pods found for gateway %s", component, gw.Name) //nolint:goerr113
		}

		for i := range pods.Items {
			pod := &pods.Items[i]
			killed[pod.UID] = true
			if err := kube.Delete(ctx, pod); err != nil {
				return fmt.Errorf("deleting pod %s: %w", pod.Name, err)
			}
			slog.Debug("Killed gateway pod", "component", component, "name", pod.Name, "UID", pod.UID)
		}
	}
	slog.Info("Killed gateway pods", "component", component, "gateways", len(selectors), "pods", len(killed))

	start := time.Now()
	for {
		notReady := []string{}
		for gwName, sel := range selectors {
			pods := &corev1.PodList{}
			if err := kube.List(ctx, pods, kclient.InNamespace(comp.FabNamespace), sel); err != nil {
				return fmt.Errorf("listing %s pods of gateway %s: %w", component, gwName, err)
			}

			pending := slices.ContainsFunc(pods.Items, func(pod corev1.Pod) bool {
				if killed[pod.UID] || pod.Status.Phase != corev1.PodRunning {
					return true
				}

				return !slices.ContainsFunc(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
					return cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue
				})
			})
			if len(pods.Items) == 0 || pending {
				notReady = append(notReady, gwName)
			}
		}

		if len(notReady) == 0 {
			slog.Info("Gateway pods back up", "component", component, "took", time.Since(start))

			return nil
		}

		if time.Since(start) > gwPodRestartTimeout {
			return fmt.Errorf("timed out waiting for %s pods to restart on gateways %v", component, notReady) //nolint:goerr113
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s pods to restart: %w", component, ctx.Err())
		case <-time.After(gwPodPollInterval):
		}
	}
}

// gatewayPodRestartTest peers two VPCs through the gateway and checks that
// connectivity survives killing the dataplane pods and then, separately, the
// FRR pods. The two share state over a unix socket and we have hit regressions
// where killing one or the other broke the gateway.
func gatewayPodRestartTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	skipped, reverts, err := testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway pod restart",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, nil)

			return specs, err
		},
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

	for _, component := range []string{"dataplane", "frr"} {
		if err := restartGatewayPods(ctx, testCtx.kube, component); err != nil {
			return false, nil, fmt.Errorf("restarting gateway %s: %w", component, err)
		}
		// Give the gateway time to react
		slog.Info("Waiting a few seconds for gateway to react...")
		time.Sleep(5 * time.Second)
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			return false, nil, fmt.Errorf("waiting for ready after %s restart: %w", component, err)
		}
		if err := DoVLABTestConnectivityWithMatrix(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, tcOpts, matrix); err != nil {
			return false, nil, fmt.Errorf("testing connectivity after %s restart: %w", component, err)
		}
	}

	return false, nil, nil
}

func getGatewayRestartTestCases() []JUnitTestCase {
	return []JUnitTestCase{
		{Name: "Gateway Dataplane and FRR Pod Restart", F: gatewayPodRestartTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
	}
}
