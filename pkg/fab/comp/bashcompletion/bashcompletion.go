// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bashcompletion

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/artificer"
)

//go:embed scripts/profile.sh
var scriptFS embed.FS

const (
	BashCompletionRef   = "fabricator/bash-completion"
	BashCompletionFile  = "bash_completion"
	BashCompletionDFile = "bash_completion.d"
	CompatFile          = "000_bash_completion_compat.bash"
	LicenseFile         = "COPYING"
	InstallDir          = "/opt/bash-completion"
	ProfileDir          = "/etc/profile.d"
	ProfileFilename     = "bash-completion.sh"
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
		filepath.Join(InstallDir, BashCompletionDFile),
		ProfileDir,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	bashCompletionSrc := filepath.Join(workDir, BashCompletionFile)
	bashCompletionDst := filepath.Join(InstallDir, BashCompletionFile)
	if err := artificer.CopyFile(bashCompletionSrc, bashCompletionDst); err != nil {
		return fmt.Errorf("copying bash_completion file: %w", err)
	}

	compatSrc := filepath.Join(workDir, CompatFile)
	compatDst := filepath.Join(InstallDir, BashCompletionDFile, CompatFile)
	if err := artificer.CopyFile(compatSrc, compatDst); err != nil {
		return fmt.Errorf("copying bash_completion.d compat file: %w", err)
	}

	licenseSrc := filepath.Join(workDir, LicenseFile)
	licenseDst := filepath.Join(InstallDir, LicenseFile)
	if err := artificer.CopyFile(licenseSrc, licenseDst); err != nil {
		return fmt.Errorf("copying license file: %w", err)
	}

	profileScript, err := scriptFS.ReadFile("scripts/profile.sh")
	if err != nil {
		return fmt.Errorf("reading profile script: %w", err)
	}

	profilePath := filepath.Join(ProfileDir, ProfileFilename)
	if err := os.WriteFile(profilePath, profileScript, 0644); err != nil { //nolint:gosec
		return fmt.Errorf("writing bash-completion profile script: %w", err)
	}

	slog.Info("Successfully installed bash-completion with license")

	return nil
}
