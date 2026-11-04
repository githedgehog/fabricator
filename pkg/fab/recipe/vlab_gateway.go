// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	vlabcomp "go.githedgehog.com/fabricator/pkg/fab/comp/vlab"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// vlabGatewayMigrationBackoff makes up to 12 attempts, waiting 0.5s before the first retry and 1.5 times longer before
// each next one, ±10%, so ~1.5 minutes of waits in total with the last one ~30s. It ends well within the 5 minutes
// timeout of the whole migration, which is there for an attempt that hangs, e.g. waiting for the fabric controller.
var vlabGatewayMigrationBackoff = wait.Backoff{
	Steps:    12,
	Duration: 500 * time.Millisecond,
	Factor:   1.5,
	Jitter:   0.1,
}

// setVLABHugePages sets the hugepages of the gateway VMs on their FabNodes and Gateways unless already set, the same as
// hhfab sets them for the installers. The VLAB marker of the control VM tells it's a VLAB and the hw annotation of the
// FabNodes tells the hardware gateways of a hybrid VLAB apart. It runs before the fabricator is upgraded, as the new
// fabric controller would request the default hugepages for the gateways without them.
//
// TODO remove once upgrades are only supported from the releases setting the hugepages
func setVLABHugePages(ctx context.Context, kube kclient.Client) error {
	if !isVLAB() {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// any error, e.g. a conflict or the webhooks not being ready after the k8s upgrade, is retried with a fresh list
	attempt := 0
	if err := retry.OnError(vlabGatewayMigrationBackoff, func(error) bool {
		return ctx.Err() == nil
	}, func() error {
		attempt++

		err := setVLABHugePagesOnce(ctx, kube)
		if err != nil {
			slog.Warn("Failed to set VLAB hugepages", "attempt", attempt, "err", err)
		}

		return err
	}); err != nil {
		return fmt.Errorf("setting VLAB hugepages after %d attempts: %w", attempt, err)
	}

	return nil
}

func setVLABHugePagesOnce(ctx context.Context, kube kclient.Client) error {
	nodes := &fabapi.FabNodeList{}
	if err := kube.List(ctx, nodes); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}
	gwList := &gwapi.GatewayList{}
	if err := kube.List(ctx, gwList); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}
	gws := map[string]*gwapi.Gateway{}
	for idx := range gwList.Items {
		gws[gwList.Items[idx].Name] = &gwList.Items[idx]
	}

	for idx := range nodes.Items {
		node := &nodes.Items[idx]
		if !vlabcomp.IsGatewayVM(*node) {
			continue
		}

		gw := gws[node.Name]
		nodeChanged, gwChanged := vlabcomp.SetGatewayHugePages(node, gw)
		if nodeChanged {
			slog.Info("Setting VLAB hugepages", "node", node.Name)

			if err := kube.Update(ctx, node); err != nil {
				return fmt.Errorf("updating node %s: %w", node.Name, err)
			}
		}
		if gwChanged {
			slog.Info("Setting VLAB hugepages", "gateway", gw.Name)

			if err := kube.Update(ctx, gw); err != nil {
				return fmt.Errorf("updating gateway %s: %w", gw.Name, err)
			}
		}
	}

	return nil
}
