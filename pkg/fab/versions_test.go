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

func TestFabricatorCtrlUpgradeConstraint(t *testing.T) {
	fabCtrlConstr, err := semver.NewConstraint(FabricatorCtrlConstraint)
	require.NoError(t, err, "Error parsing fabricator ctrl constraint")
	require.NotNil(t, fabCtrlConstr, "Fabricator ctrl constraint should not be nil")

	for _, test := range []struct {
		name     string
		version  string
		expected bool
	}{
		{"current", version.Version, true},
		{"dev", "v0.42.0", true},
		{"25.04", "v0.41.3", true},
		{"25.04.0", "v0.41.3", true},
		{"25.03", "v0.40.0", false},
		{"25.03.0", "v0.40.0", false},
		{"25.02", "v0.38.1", false},
		{"25.02.0", "v0.38.1", false},
		{"25.01", "v0.36.1", false},
		{"25.01.0", "v0.36.1", false},
		{"24.09", "v0.32.1", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, err := semver.NewVersion(test.version)
			require.NoError(t, err, "Error parsing test version")
			require.NotNil(t, v, "Test version should not be nil")

			assert.Equal(t, test.expected, fabCtrlConstr.Check(v),
				"Fabricator ctrl upgrade from version %q should be %v", test.version, test.expected)
		})
	}
}

func TestFabricAgentUpgradeConstraint(t *testing.T) {
	fabAgentConstr, err := semver.NewConstraint(FabricAgentConstraint)
	require.NoError(t, err, "Error parsing fabric agent constraint")
	require.NotNil(t, fabAgentConstr, "Fabric agent constraint should not be nil")

	for _, test := range []struct {
		name     string
		version  string
		expected bool
	}{
		{"current", string(FabricVersion), true},
		{"dev", "v0.89.0", true},
		{"25.04", "v0.87.4", true},
		{"25.04.0", "v0.87.4", true},
		{"25.03", "v0.81.1", false},
		{"25.03.0", "v0.81.1", false},
		{"25.02", "v0.75.3", false},
		{"25.02.0", "v0.75.3", false},
		{"25.01", "v0.71.6", false},
		{"25.01.0", "v0.71.6", false},
		{"24.09", "v0.58.0", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, err := semver.NewVersion(test.version)
			require.NoError(t, err, "Error parsing test version")
			require.NotNil(t, v, "Test version should not be nil")

			assert.Equal(t, test.expected, fabAgentConstr.Check(v),
				"Fabric agent upgrade from version %q should be %v", test.version, test.expected)
		})
	}
}

func TestFabricNOSUpgradeConstraint(t *testing.T) {
	fabNOSConstr, err := semver.NewConstraint(FabricNOSConstraint)
	require.NoError(t, err, "Error parsing fabric NOS constraint")
	require.NotNil(t, fabNOSConstr, "Fabric NOS constraint should not be nil")

	for _, test := range []struct {
		name     string
		version  string
		expected bool
	}{
		{"current", string(BCMSONiCVersion), true},
		{"dev", "v4.5.0", true},
		{"25.03", "v4.5.0", true},
		{"25.03.0", "v4.5.0", true},
		{"25.02", "v4.4.2", false},
		{"25.02.0", "v4.4.2", false},
		{"25.01", "v4.4.2", false},
		{"25.01.0", "v4.4.2", false},
		{"24.09", "v4.4.0", false},
		{"dev-agent", CleanupFabricNOSVersion("4.5.0-Enterprise_Base"), true},
		{"25.03-agent", CleanupFabricNOSVersion("4.5.0-Enterprise_Base"), true},
		{"25.03.0-agent", CleanupFabricNOSVersion("4.5.0-Enterprise_Base"), true},
		{"25.02-agent", CleanupFabricNOSVersion("4.4.2-Enterprise_Base"), false},
		{"25.02.0-agent", CleanupFabricNOSVersion("4.4.2-Enterprise_Base"), false},
		{"25.01-agent", CleanupFabricNOSVersion("4.4.2-Enterprise_Base"), false},
		{"25.01.0-agent", CleanupFabricNOSVersion("4.4.2-Enterprise_Base"), false},
		{"24.09-agent", CleanupFabricNOSVersion("4.4.0-Enterprise_Base"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, err := semver.NewVersion(test.version)
			require.NoError(t, err, "Error parsing test version")
			require.NotNil(t, v, "Test version should not be nil")

			assert.Equal(t, test.expected, fabNOSConstr.Check(v),
				"Fabric NOS upgrade from version %q should be %v", test.version, test.expected)
		})
	}
}
