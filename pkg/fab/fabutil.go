// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"dario.cat/mergo"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetFabAndNodes(ctx context.Context, kube client.Reader, allowNotHydrated bool) (fabapi.Fabricator, []fabapi.ControlNode, []fabapi.Node, error) {
	f := &fabapi.Fabricator{}
	if err := kube.Get(ctx, client.ObjectKey{Name: comp.FabName, Namespace: comp.FabNamespace}, f); err != nil {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("getting fabricator: %w", err)
	}

	fabs := &fabapi.FabricatorList{}
	if err := kube.List(ctx, fabs); err != nil {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("listing fabricators: %w", err)
	}
	if len(fabs.Items) != 1 {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("exactly one fabricator is required") //nolint:goerr113
	}

	if err := mergo.Merge(&f.Spec.Config, *DefaultConfig.DeepCopy()); err != nil {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("merging fabricator defaults: %w", err)
	}

	if err := f.Validate(ctx); err != nil {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("validating fabricator: %w", err)
	}

	if err := f.CalculateVersions(Versions); err != nil {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("calculating versions: %w", err)
	}

	controls := &fabapi.ControlNodeList{}
	if err := kube.List(ctx, controls); err != nil {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("listing control nodes: %w", err)
	}
	if len(controls.Items) == 0 {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("no control nodes found") //nolint:goerr113
	}
	if len(controls.Items) > 1 {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("only one control node is currently allowed") //nolint:goerr113
	}

	for _, control := range controls.Items {
		if err := control.Validate(ctx, &f.Spec.Config, allowNotHydrated); err != nil {
			return fabapi.Fabricator{}, nil, nil, fmt.Errorf("validating control node %q: %w", control.GetName(), err)
		}
	}

	slices.SortFunc(controls.Items, func(a, b fabapi.ControlNode) int {
		return cmp.Compare(a.Name, b.Name)
	})

	nodes := &fabapi.NodeList{}
	// It's okay if node resources are not found, as we may be upgrading from the older versions
	// TODO make it strict after we completely migrate to Node objects for everything
	if err := kube.List(ctx, nodes); err != nil && !apimeta.IsNoMatchError(err) {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("listing nodes: %w", err)
	}
	if len(nodes.Items) > 1 {
		return fabapi.Fabricator{}, nil, nil, fmt.Errorf("only one node is currently allowed") //nolint:goerr113
	}

	for _, node := range nodes.Items {
		if err := node.Validate(ctx, &f.Spec.Config, allowNotHydrated); err != nil {
			return fabapi.Fabricator{}, nil, nil, fmt.Errorf("validating node %q: %w", node.GetName(), err)
		}
	}

	slices.SortFunc(nodes.Items, func(a, b fabapi.Node) int {
		return cmp.Compare(a.Name, b.Name)
	})

	f.APIVersion = fabapi.GroupVersion.String()
	f.Kind = fabapi.KindFabricator

	return *f, controls.Items, nodes.Items, nil
}
