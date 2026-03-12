// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
)

func AirGenerate(ctx context.Context, workDir, cacheDir string, hMode HydrateMode) error {
	cfg, err := load(ctx, workDir, cacheDir, true, hMode, "")
	if err != nil {
		return err
	}

	return cfg.AirGenerate(ctx)
}
