// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"fmt"
	"slices"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DataplaneRef          = "dataplane"
	DataplaneValidatorRef = "dataplane/validator"
	DataplaneMetricsPort  = 9442
	FRRMetricsPort        = 9342

	// Hugepages reserved on the gateway nodes and requested by the dataplane, it currently only uses 1G pages
	HugePages1G = 4
	HugePages2M = 0
)

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		DataplaneRef:          cfg.Status.Versions.Gateway.Dataplane,
		DataplaneValidatorRef: cfg.Status.Versions.Gateway.Dataplane,
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

		res[node.Name], err = comp.GetDaemonSetStatus(gwapi.DaemonSetName(node.Name), "dataplane", image)(ctx, kube, cfg)
		if err != nil {
			return nil, fmt.Errorf("getting status for dataplane on node %q: %w", node.Name, err)
		}
	}

	return res, nil
}
