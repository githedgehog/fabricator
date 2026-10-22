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

// Under sudo the command gets the cache dir this process resolved, whatever
// root's own default or an older HHFAB_CACHE_DIR would be, and keeps HOME and
// the other HHFAB_* settings.
func TestSudoCommandPassesCacheDir(t *testing.T) {
	t.Parallel()

	argv, env := sudoCommand("/home/u/hhfab", []string{"vlab", "bench", "dhcp", "--sudo", "--vpcs", "2"},
		[]string{"HOME=/home/u", "PATH=/usr/bin", "HHFAB_CACHE_DIR=/old", "HHFAB_PREVIEW=true"}, "/home/u/.hhfab-cache")

	require.Equal(t, []string{
		"sudo", "--preserve-env=HOME,HHFAB_PREVIEW,HHFAB_CACHE_DIR", "/home/u/hhfab", "vlab", "bench", "dhcp", "--vpcs", "2",
	}, argv)
	require.Equal(t, []string{"HOME=/home/u", "PATH=/usr/bin", "HHFAB_PREVIEW=true", "HHFAB_CACHE_DIR=/home/u/.hhfab-cache"}, env)
}
