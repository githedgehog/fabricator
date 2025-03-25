// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bashcompletion

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
)

//go:embed scripts/profile.sh
var scriptFS embed.FS

const (
	BashCompletionRef  = "bash-completion"
	BashCompletionFile = "bash_completion"
	InstallDir         = "/opt/bash-completion"
	ProfileDir         = "/etc/profile.d"
	ProfileFilename    = "bash-completion.sh"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.BashCompletion
}

func Install(_ context.Context, workDir string, fab fabapi.Fabricator) error {
	slog.Info("Installing bash-completion")

	versionStr := strings.TrimPrefix(string(Version(fab)), "v")
	slog.Info("Using bash-completion version", "version", versionStr)

	dirs := []string{
		InstallDir,
		ProfileDir,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	srcPath := filepath.Join(workDir, BashCompletionFile)
	dstPath := filepath.Join(InstallDir, BashCompletionFile)

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying file content: %w", err)
	}

	profileScript, err := scriptFS.ReadFile("scripts/profile.sh")
	if err != nil {
		return fmt.Errorf("reading profile script: %w", err)
	}

	profilePath := filepath.Join(ProfileDir, ProfileFilename)
	if err := os.WriteFile(profilePath, profileScript, 0644); err != nil { //nolint:gosec
		return fmt.Errorf("writing bash-completion profile script: %w", err)
	}

	return nil
}
