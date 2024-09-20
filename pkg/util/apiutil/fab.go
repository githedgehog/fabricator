package apiutil

import (
	"context"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ValidateFabricator(ctx context.Context, l *Loader) error {
	if l == nil {
		return fmt.Errorf("loader is nil") //nolint:goerr113
	}

	fabs := &fabapi.FabricatorList{}
	if err := l.kube.List(ctx, fabs); err != nil {
		return fmt.Errorf("listing fabricators: %w", err)
	}
	if len(fabs.Items) != 1 {
		return fmt.Errorf("exactly one fabricator is required") //nolint:goerr113
	}

	fab := fabs.Items[0]
	if fab.Namespace != comp.FabNamespace {
		return fmt.Errorf("fabricator should be in %q namespace", comp.FabNamespace) //nolint:goerr113
	}
	if fab.Name != comp.FabName {
		return fmt.Errorf("fabricator should be named %q", comp.FabName) //nolint:goerr113
	}

	if err := fab.Validate(); err != nil {
		return fmt.Errorf("validating fabricator: %w", err)
	}

	controls := &fabapi.ControlNodeList{}
	if err := l.kube.List(ctx, controls); err != nil {
		return fmt.Errorf("listing control nodes: %w", err)
	}
	if len(controls.Items) == 0 {
		return fmt.Errorf("no control nodes found") //nolint:goerr113
	}
	if len(controls.Items) > 1 {
		return fmt.Errorf("only one control node is currently allowed") //nolint:goerr113
	}

	for _, control := range controls.Items {
		if err := control.Validate(&fab.Spec.Config); err != nil {
			return fmt.Errorf("validating control node %q: %w", control.GetName(), err)
		}
	}

	return nil
}

func GetFabAndControls(ctx context.Context, l *Loader) (fabapi.Fabricator, []fabapi.ControlNode, error) {
	if err := ValidateFabricator(ctx, l); err != nil {
		return fabapi.Fabricator{}, nil, err
	}

	fab := &fabapi.Fabricator{}
	if err := l.kube.Get(ctx, client.ObjectKey{Name: comp.FabName, Namespace: comp.FabNamespace}, fab); err != nil {
		return fabapi.Fabricator{}, nil, fmt.Errorf("getting fabricator: %w", err)
	}

	controls := &fabapi.ControlNodeList{}
	if err := l.kube.List(ctx, controls); err != nil {
		return fabapi.Fabricator{}, nil, fmt.Errorf("listing control nodes: %w", err)
	}

	return *fab, controls.Items, nil
}
