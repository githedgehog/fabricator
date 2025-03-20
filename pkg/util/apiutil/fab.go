// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"fmt"
	"io"

	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

func PrintFab(f fabapi.Fabricator, controls []fabapi.ControlNode, nodes []fabapi.FabNode, w io.Writer) error {
	if err := kubeutil.PrintObject(&f, w, true); err != nil {
		return fmt.Errorf("printing fabricator: %w", err)
	}

	for _, control := range controls {
		_, err := fmt.Fprintf(w, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := kubeutil.PrintObject(&control, w, false); err != nil {
			return fmt.Errorf("printing control node %s: %w", control.Name, err)
		}
	}

	for _, node := range nodes {
		_, err := fmt.Fprintf(w, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := kubeutil.PrintObject(&node, w, false); err != nil {
			return fmt.Errorf("printing node %s: %w", node.Name, err)
		}
	}

	return nil
}
