// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKernelArgs(t *testing.T) {
	args := KernelArgs()

	seen := map[string]bool{}
	for _, arg := range args {
		require.False(t, seen[arg], "duplicate kernel arg %q", arg)
		seen[arg] = true

		key, _, _ := strings.Cut(arg, "=")
		require.True(t, slices.Contains(KernelArgKeys, key), "kernel arg %q isn't managed", arg)
	}
}
