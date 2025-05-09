// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	"os"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/version"
	"golang.org/x/mod/modfile"
)

func TestFlatcarVersion(t *testing.T) {
	assert.True(t,
		strings.HasPrefix(
			string(Versions.Fabricator.ControlUSBRoot),
			string(Versions.Fabricator.Flatcar)+"-"),
		"ControlUSBRoot version should be based on the Fabricator Flatcar version")

	assert.Equal(t,
		string(Versions.Fabricator.Flatcar),
		string(Versions.VLAB.Flatcar),
		"VLAB Flatcar version should match Fabricator Flatcar version")
}

func TestDependenciesMatchVersions(t *testing.T) {
	content, err := os.ReadFile("../../go.mod")
	require.NoError(t, err, "Error reading go.mod")

	modfile, err := modfile.Parse("go.mod", content, nil)
	require.NoError(t, err, "Error parsing go.mod")

	assert.Equal(t, "go.githedgehog.com/fabricator", modfile.Module.Mod.Path, "Module path should be go.githedgehog.com/fabricator")
	assert.True(t, len(modfile.Require) > 0, "No dependencies found in go.mod")

	checkVersion(t, modfile, "go.githedgehog.com/fabric", string(FabricVersion))
	checkVersion(t, modfile, "go.githedgehog.com/gateway", string(GatewayVersion))
	checkVersion(t, modfile, "github.com/cert-manager/cert-manager", string(Versions.Platform.CertManager))
}

func checkVersion(t *testing.T, modfile *modfile.File, path, version string) {
	t.Helper()

	found := false
	for _, require := range modfile.Require {
		if require.Mod.Path != path {
			continue
		}

		found = true
		assert.Equalf(t, version, require.Mod.Version, "Require path %s should match version %s", path, version)
	}

	assert.Truef(t, found, "Require path %s not found in go.mod", path)
}

func TestUpgradeConstraint(t *testing.T) {
	c, err := semver.NewConstraint(string(UpgradeConstraint))
	require.NoError(t, err, "Error parsing upgrade constraint")

	for _, test := range []struct {
		name     string
		version  string
		expected bool
	}{
		{"current", version.Version, true},
		{"25.01", "v0.36.1", true},
		{"24.09", "v0.32.1", true},
		{"beta-1", "v0.30.3", false},
	} {
		t.Run(string(test.version), func(t *testing.T) {
			v, err := semver.NewVersion(string(test.version))
			require.NoError(t, err, "Error parsing version %q", test.version)

			require.Equal(t, test.expected, c.Check(v), "Upgrade from version %q should be %v", test.version, test.expected)
		})
	}
}
