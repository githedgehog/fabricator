// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfabctl

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"go.githedgehog.com/fabricator/pkg/support"
)

type SupportDumpOpts struct {
	WorkDir string
	Name    string
	Force   bool
}

func SupportDump(ctx context.Context, opts SupportDumpOpts) error {
	slog.Info("Creating support dump", "workdir", opts.WorkDir, "name", opts.Name)

	if opts.WorkDir == "" {
		return fmt.Errorf("empty path") //nolint:goerr113
	}
	if opts.Name == "" {
		return fmt.Errorf("empty name") //nolint:goerr113
	}

	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return fmt.Errorf("mkdir path: %w", err)
	}

	if stat, err := os.Stat(opts.WorkDir); err != nil {
		return fmt.Errorf("stat path: %w", err)
	} else if !stat.IsDir() {
		return fmt.Errorf("path is not a directory") //nolint:goerr113
	}

	fullPath := filepath.Join(opts.WorkDir, opts.Name+support.FileExt)

	if stat, err := os.Stat(fullPath); err == nil {
		if !opts.Force {
			return fmt.Errorf("dump file already exists") //nolint:goerr113
		}

		if !stat.Mode().IsRegular() {
			return fmt.Errorf("existing dump file is not a regular file") //nolint:goerr113
		}

		if err := os.RemoveAll(fullPath); err != nil {
			return fmt.Errorf("remove dump file: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat dump file: %w", err)
	}

	dump, err := support.Collect(ctx, opts.Name, "")
	if err != nil {
		return fmt.Errorf("collecting support dump: %w", err)
	}

	data, err := support.Marshal(dump)
	if err != nil {
		return fmt.Errorf("marshaling dump: %w", err)
	}

	if err := os.WriteFile(fullPath, data, 0o600); err != nil {
		return fmt.Errorf("writing dump file: %w", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current working directory: %w", err)
	}

	rel, err := filepath.Rel(wd, fullPath)
	if err != nil {
		return fmt.Errorf("getting relative path: %w", err)
	}

	slog.Info("Support dump created", "path", rel)

	return nil
}
