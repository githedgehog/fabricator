package fab

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"dario.cat/mergo"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetFabAndControls(ctx context.Context, kube client.Reader, allowNotHydrated bool) (fabapi.Fabricator, []fabapi.ControlNode, error) {
	f := &fabapi.Fabricator{}
	if err := kube.Get(ctx, client.ObjectKey{Name: comp.FabName, Namespace: comp.FabNamespace}, f); err != nil {
		return fabapi.Fabricator{}, nil, fmt.Errorf("getting fabricator: %w", err)
	}

	fabs := &fabapi.FabricatorList{}
	if err := kube.List(ctx, fabs); err != nil {
		return fabapi.Fabricator{}, nil, fmt.Errorf("listing fabricators: %w", err)
	}
	if len(fabs.Items) != 1 {
		return fabapi.Fabricator{}, nil, fmt.Errorf("exactly one fabricator is required") //nolint:goerr113
	}

	if err := mergo.Merge(&f.Spec.Config, *DefaultConfig.DeepCopy()); err != nil {
		return fabapi.Fabricator{}, nil, fmt.Errorf("merging fabricator defaults: %w", err)
	}

	controls := &fabapi.ControlNodeList{}
	if err := kube.List(ctx, controls); err != nil {
		return fabapi.Fabricator{}, nil, fmt.Errorf("listing control nodes: %w", err)
	}
	if len(controls.Items) == 0 {
		return fabapi.Fabricator{}, nil, fmt.Errorf("no control nodes found") //nolint:goerr113
	}
	if len(controls.Items) > 1 {
		return fabapi.Fabricator{}, nil, fmt.Errorf("only one control node is currently allowed") //nolint:goerr113
	}

	for _, control := range controls.Items {
		if err := control.Validate(ctx, &f.Spec.Config, allowNotHydrated); err != nil {
			return fabapi.Fabricator{}, nil, fmt.Errorf("validating control node %q: %w", control.GetName(), err)
		}
	}

	slices.SortFunc(controls.Items, func(a, b fabapi.ControlNode) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return *f, controls.Items, nil
}
