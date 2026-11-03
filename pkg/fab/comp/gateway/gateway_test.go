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
	for _, tt := range []struct {
		name    string
		pages1G int
		pages2M int
		want    []string
	}{
		{
			name:    "1g",
			pages1G: 16,
			want:    []string{"iommu=pt", "intel_iommu=on", "hugepagesz=1G", "hugepages=16"},
		},
		{
			name:    "both",
			pages1G: 4,
			pages2M: 512,
			want:    []string{"iommu=pt", "intel_iommu=on", "hugepagesz=1G", "hugepages=4", "hugepagesz=2M", "hugepages=512"},
		},
		{
			name:    "2m",
			pages2M: 1024,
			want:    []string{"iommu=pt", "intel_iommu=on", "hugepagesz=2M", "hugepages=1024"},
		},
		{
			name: "none",
			want: []string{"iommu=pt", "intel_iommu=on"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := KernelArgs(tt.pages1G, tt.pages2M)
			require.Equal(t, tt.want, args)

			seen := map[string]bool{}
			for _, arg := range args {
				require.False(t, seen[arg], "duplicate kernel arg %q", arg)
				seen[arg] = true

				key, _, _ := strings.Cut(arg, "=")
				require.True(t, slices.Contains(KernelArgKeys, key), "kernel arg %q isn't managed", arg)
			}
		})
	}
}
