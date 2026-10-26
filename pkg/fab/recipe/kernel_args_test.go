// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var testKernelArgKeys = []string{"iommu", "hugepagesz", "hugepages"}

func TestSetKernelArgs(t *testing.T) {
	args := []string{"iommu=pt", "hugepagesz=1G", "hugepages=4"}

	for _, tt := range []struct {
		name    string
		cfg     string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "no-file",
			args: args,
			want: "set linux_append=\"$linux_append iommu=pt hugepagesz=1G hugepages=4\"\n",
		},
		{
			name: "no-args",
			cfg:  "set oem_id=\"qemu\"\n",
			want: "set oem_id=\"qemu\"\n",
		},
		{
			name: "append",
			cfg:  "# Flatcar GRUB settings\n\nset oem_id=\"qemu\"\n",
			args: args,
			want: "# Flatcar GRUB settings\n\nset oem_id=\"qemu\"\nset linux_append=\"$linux_append iommu=pt hugepagesz=1G hugepages=4\"\n",
		},
		{
			name: "same-as-ignition",
			cfg:  "# Flatcar GRUB settings\n\nset oem_id=\"qemu\"\n\nset linux_append=\"$linux_append flatcar.autologin iommu=pt hugepagesz=1G hugepages=4\"\n",
			args: args,
			want: "# Flatcar GRUB settings\n\nset oem_id=\"qemu\"\n\nset linux_append=\"$linux_append flatcar.autologin iommu=pt hugepagesz=1G hugepages=4\"\n",
		},
		{
			name: "replace-and-merge",
			cfg:  "set oem_id=\"qemu\"\nset linux_append=\"$linux_append flatcar.autologin hugepagesz=1G\"\nset linux_append=\"$linux_append hugepages=2 console=ttyS0\"",
			args: args,
			want: "set oem_id=\"qemu\"\nset linux_append=\"$linux_append flatcar.autologin console=ttyS0 iommu=pt hugepagesz=1G hugepages=4\"\n",
		},
		{
			name: "drop-managed",
			cfg:  "set linux_append=\"$linux_append flatcar.autologin hugepagesz=1G hugepages=4\"\n",
			want: "set linux_append=\"$linux_append flatcar.autologin\"\n",
		},
		{
			name: "drop-line",
			cfg:  "set oem_id=\"qemu\"\nset linux_append=\"$linux_append iommu=pt\"\n",
			want: "set oem_id=\"qemu\"\n",
		},
		{
			name: "conditional-untouched",
			cfg:  "if [ \"$x\" ]; then\n  set linux_append=\"$linux_append hugepages=2\"\nfi\n",
			args: args,
			want: "if [ \"$x\" ]; then\n  set linux_append=\"$linux_append hugepages=2\"\nfi\nset linux_append=\"$linux_append iommu=pt hugepagesz=1G hugepages=4\"\n",
		},
		{
			name:    "escaped-quotes",
			cfg:     "set linux_append=\"$linux_append foo=\\\"a b\\\" hugepages=2\"\n",
			args:    args,
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setKernelArgs([]byte(tt.cfg), testKernelArgKeys, tt.args)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))

			again, err := setKernelArgs(got, testKernelArgKeys, tt.args)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(again), "not idempotent")
		})
	}
}

func TestKernelArgsApplied(t *testing.T) {
	args := []string{"iommu=pt", "hugepagesz=1G", "hugepages=4"}

	for _, tt := range []struct {
		name    string
		cmdline string
		want    bool
	}{
		{
			name:    "applied",
			cmdline: "BOOT_IMAGE=/flatcar/vmlinuz-a mount.usr=PARTUUID=x flatcar.autologin iommu=pt hugepagesz=1G hugepages=4\n",
			want:    true,
		},
		{
			name:    "missing",
			cmdline: "BOOT_IMAGE=/flatcar/vmlinuz-a flatcar.autologin\n",
		},
		{
			name:    "other-count",
			cmdline: "flatcar.autologin iommu=pt hugepagesz=1G hugepages=2\n",
		},
		{
			name:    "extra",
			cmdline: "iommu=pt hugepagesz=1G hugepages=4 hugepagesz=2M hugepages=4\n",
		},
		{
			name:    "other-order",
			cmdline: "hugepagesz=1G hugepages=4 iommu=pt\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, kernelArgsApplied(tt.cmdline, testKernelArgKeys, args))
		})
	}
}
