// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The k3s config is rewritten on upgrade only when it differs, since a write
// is what makes the upgrade restart k3s.
func TestUpdateConfigFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")

	changed, err := updateConfigFile(path, "a: 1\n")
	require.NoError(t, err)
	require.True(t, changed, "a missing file is a change")

	changed, err = updateConfigFile(path, "a: 1\n")
	require.NoError(t, err)
	require.False(t, changed, "the same content must not count as a change")

	changed, err = updateConfigFile(path, "a: 2\n")
	require.NoError(t, err)
	require.True(t, changed)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "a: 2\n", string(got))
}
