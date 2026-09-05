// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"fmt"
	"slices"

	// just to keep the import
	_ "go.githedgehog.com/fabric/api/gateway/v1alpha1"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DataplaneRef          = "dataplane"
	DataplaneValidatorRef = "dataplane/validator"
	FRRRef                = "dataplane/frr"
	DataplaneMetricsPort  = 9442
	FRRMetricsPort        = 9342
)

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		DataplaneRef:          cfg.Status.Versions.Gateway.Dataplane,
		DataplaneValidatorRef: cfg.Status.Versions.Gateway.Dataplane,
		FRRRef:                cfg.Status.Versions.Gateway.FRR,
	}, nil
}

func StatusDataplane(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator, nodes []fabapi.FabNode) (map[string]fabapi.ComponentStatus, error) {
	res := map[string]fabapi.ComponentStatus{}
	if !cfg.Spec.Config.Gateway.Enable {
		return res, nil
	}

	ref, err := comp.ImageURL(cfg, DataplaneRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", DataplaneRef, err)
	}
	image := ref + ":" + string(cfg.Status.Versions.Gateway.Dataplane)

	for _, node := range nodes {
		if !slices.Contains(node.Spec.Roles, fabapi.NodeRoleGateway) {
			continue
		}

		// TODO make name builder reusable in the gayeway-ctrl
		res[node.Name], err = comp.GetDaemonSetStatus(fmt.Sprintf("gw--%s--dataplane", node.Name), "dataplane", image)(ctx, kube, cfg)
		if err != nil {
			return nil, fmt.Errorf("getting status for dataplane on node %q: %w", node.Name, err)
		}
	}

	return res, nil
}

// StatusFRR reports nothing, because FRR is no longer a component of its own.
//
// It used to look up a `gw--<node>--frr` DaemonSet. FRR now runs under `dataplane-init` inside the
// dataplane pod -- one image, one process tree, shared fate -- so that DaemonSet does not exist and
// the gateway controller deletes it on sight. StatusDataplane covers what is left.
//
// Returning an empty map rather than deleting the function keeps `ComponentsStatus.GatewayFRR` in
// the API, so no CRD changes with it. Both loops that read it become vacuous, which matters:
// IsGatewayReady requires every entry to be Ready, and a DaemonSet that is *never* going to exist
// reports NotFound forever. That does not fail anything -- it hangs `hhfab vlab up --ready` until
// the timeout, with IsReady still true because it only asks for "not Unknown".
func StatusFRR(_ context.Context, _ kclient.Reader, _ fabapi.Fabricator, _ []fabapi.FabNode) (map[string]fabapi.ComponentStatus, error) {
	return map[string]fabapi.ComponentStatus{}, nil
}
