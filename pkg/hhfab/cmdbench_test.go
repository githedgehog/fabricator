// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Re-running under sudo drops --sudo in every spelling the CLI accepts, so the
// command under sudo doesn't try to re-run itself, and keeps everything else.
func TestWithoutSudoFlag(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{"vlab", "bench", "dhcp", "--vpcs", "2", "--relay", "sudo", "--pxe"},
		withoutSudoFlag([]string{"vlab", "bench", "dhcp", "--sudo", "--vpcs", "2", "-sudo", "--relay", "sudo", "--sudo=true", "--pxe", "-sudo=false"}),
	)
}
