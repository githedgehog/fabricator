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

// KernelArgKeys are the kernel args fully managed on the gateway nodes, any of them not in KernelArgs is dropped
var KernelArgKeys = []string{"iommu", "intel_iommu", "amd_iommu", "hugepagesz", "hugepages", "default_hugepagesz"}

// KernelArgs are set by ignition on install and by the installer on upgrade, ignition skips args that are already
// there, so they must be unique to keep the hugepages sizes and counts paired
func KernelArgs() []string {
	args := []string{"iommu=pt", "intel_iommu=on"}
	if HugePages1G > 0 {
		args = append(args, "hugepagesz=1G", fmt.Sprintf("hugepages=%d", HugePages1G))
	}
	if HugePages2M > 0 {
		args = append(args, "hugepagesz=2M", fmt.Sprintf("hugepages=%d", HugePages2M))
	}

	return args
}

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
