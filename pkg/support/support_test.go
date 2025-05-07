// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/support"
)

func TestCurrentVersion(t *testing.T) {
	supported := support.SupportedVersion.Check(support.CurrentVersion)
	require.True(t, supported, "current version should be supported")
}
