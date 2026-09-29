// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/hhfab/bench"
)

func TestParseSize(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]int{
		"":         0,
		"0":        0,
		"512":      512,
		"100KB":    100000,
		"100kb":    100000,
		"100KiB":   102400,
		"1MB":      1000000,
		"1MiB":     1048576,
		"2K":       2048,
		"4096B":    4096,
		" 100KB  ": 100000,
	} {
		got, err := bench.ParseSize(value)
		require.NoError(t, err, "parsing %q", value)
		require.Equal(t, want, got, "parsing %q", value)
	}
}

func TestParseSizeRejects(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"abc", "100GG", "1.5MB", "KB"} {
		_, err := bench.ParseSize(value)
		require.Error(t, err, "should reject %q", value)
	}
}
