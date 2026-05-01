// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfabctl

import (
	"context"
	"fmt"
	"log/slog"

	"go.githedgehog.com/fabricator/pkg/fab"
)

func ReleaseShow(_ context.Context) error {
	ch, err := fab.ReleaseChannel()
	if err != nil {
		return fmt.Errorf("getting release channel: %w", err)
	}

	slog.Info("Release", "version", fab.Release, "channel", ch)

	return nil
}
