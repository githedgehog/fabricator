// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfabctl

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"

	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
)

func ConfigExport(ctx context.Context) error {
	kube, err := kubeutil.NewClient(ctx, "", fabapi.SchemeBuilder)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	f, controls, nodes, err := fab.GetFabAndNodes(ctx, kube, false)
	if err != nil {
		return fmt.Errorf("getting fabricator and control nodes: %w", err)
	}

	slices.SortFunc(controls, func(a, b fabapi.ControlNode) int {
		return cmp.Compare(a.Name, b.Name)
	})

	slices.SortFunc(nodes, func(a, b fabapi.Node) int {
		return cmp.Compare(a.Name, b.Name)
	})

	out := os.Stdout

	if err := kubeutil.PrintObject(&f, out, false); err != nil {
		return fmt.Errorf("printing fabricator: %w", err)
	}

	for _, c := range controls {
		_, err := fmt.Fprintf(out, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := kubeutil.PrintObject(&c, out, false); err != nil {
			return fmt.Errorf("printing control node: %w", err)
		}
	}

	for _, n := range nodes {
		_, err := fmt.Fprintf(out, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := kubeutil.PrintObject(&n, out, false); err != nil {
			return fmt.Errorf("printing node: %w", err)
		}
	}

	return nil
}
