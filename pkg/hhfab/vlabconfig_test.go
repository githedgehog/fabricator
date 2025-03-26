// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetNICID(t *testing.T) {
	for _, tt := range []struct {
		nic  string
		want uint
		err  bool
	}{
		{
			nic: "eth0",
			err: true,
		},
		{
			nic: "eno0",
			err: true,
		},
		{
			nic: "enp1s1",
			err: true,
		},
		{
			nic: "x1",
			err: true,
		},
		{
			nic: "M2",
			err: true,
		},
		{
			nic: "E1",
			err: true,
		},
		{
			nic: "E1/",
			err: true,
		},
		{
			nic: "E1/1/",
			err: true,
		},
		{
			nic: "E1/1/1",
			err: true,
		},
		{
			nic: "E2",
			err: true,
		},
		{
			nic: "E2/",
			err: true,
		},
		{
			nic: "E2/1/",
			err: true,
		},
		{
			nic: "E2/1/1",
			err: true,
		},
		{
			nic: "Management0",
			err: true,
		},
		{
			nic: "Management1",
			err: true,
		},
		{
			nic:  "M1",
			want: 0,
		},
		{
			nic:  "E1/1",
			want: 1,
		},
		{
			nic:  "E1/2",
			want: 2,
		},
		{
			nic:  "E1/99",
			want: 99,
		},
		{
			nic:  "enp2s0",
			want: 0,
		},
		{
			nic:  "enp2s1",
			want: 1,
		},
		{
			nic:  "enp2s2",
			want: 2,
		},
		{
			nic:  "enp2s99",
			want: 99,
		},
		{
			nic:  "enp2s0np1",
			want: 0,
		},
		{
			nic:  "enp2s0np2",
			want: 0,
		},
		{
			nic:  "enp2s0np3",
			want: 0,
		},
		{
			nic:  "enp2s99np42",
			want: 99,
		},
		{
			nic: "enp2s99np42np1",
			err: true,
		},
	} {
		t.Run(tt.nic, func(t *testing.T) {
			got, err := getNICID(tt.nic)

			if tt.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
