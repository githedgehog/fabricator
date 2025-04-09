// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/hhfab/diagram"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func Diagram(ctx context.Context, workDir, cacheDir string, live bool, format diagram.Format, style diagram.StyleType) error {
	resultDir := filepath.Join(workDir, ResultDir)
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		return fmt.Errorf("creating result directory: %w", err)
	}

	var client kclient.Reader

	if !live {
		c, err := load(ctx, workDir, cacheDir, true, HydrateModeIfNotPresent, "")
		if err != nil {
			return err
		}

		client = c.Client
	} else {
		kubeconfig := filepath.Join(workDir, VLABDir, VLABKubeConfig)
		cacheCancel, kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
			wiringapi.SchemeBuilder,
			fabapi.SchemeBuilder,
		)
		if err != nil {
			return fmt.Errorf("creating kube client: %w", err)
		}
		defer cacheCancel()
		client = kube
	}

	if err := diagram.Generate(ctx, resultDir, client, format, style); err != nil {
		return fmt.Errorf("generating diagram: %w", err)
	}

	return nil
}
