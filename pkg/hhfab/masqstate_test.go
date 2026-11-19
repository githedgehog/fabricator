// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func readMasqStateSample(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "masquerade-state", name))
	require.NoError(t, err)

	return string(data)
}

const masqStateICMPUDPOtherPair = `source pools (IPv4):
  ICMP | source VPC: VNI(300), dest VPC: VNI(400), for IPs: [ 10.10.1.0 .. 10.10.1.255 ]
    idle timeout: 1800s
    region [ 100.100.1.1 .. 100.100.1.1 ]:
      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            not a port list
  TCP | source VPC: VNI(300), dest VPC: VNI(400), for IPs: [ 10.10.1.0 .. 10.10.1.255 ]
    idle timeout: 1800s
    region [ 100.100.1.1 .. 100.100.1.1 ]:
      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            1024-1033
  UDP | source VPC: VNI(300), dest VPC: VNI(400), for IPs: [ 10.10.1.0 .. 10.10.1.255 ]
    idle timeout: 1800s
    region [ 100.100.1.1 .. 100.100.1.1 ]:
      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            [all ports allocated]
  TCP | source VPC: VNI(500), dest VPC: VNI(400), for IPs: [ 10.10.5.0 .. 10.10.5.255 ]
    idle timeout: 1800s
    region [ 100.100.5.1 .. 100.100.5.1 ]:
      IP ranges in pool:
      allocated IPs:
        100.100.5.1:
          allocated ports:
            [all ports allocated]
`

// one IP full and one partly used: 64,512 + 100 held ports
var masqStateMixed = masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            [all ports allocated]
        100.100.1.2:
          allocated ports:
            1024-1123`)

func masqStateTCP(body string) string {
	return `source pools (IPv4):
  TCP | source VPC: VNI(300), dest VPC: VNI(400), for IPs: [ 10.10.1.0 .. 10.10.1.255 ]
    idle timeout: 1800s
    region [ 100.100.1.1 .. 100.100.1.2 ]:
` + body + `
source pools (IPv6):
  (empty)
`
}

func TestParseMasqueradeState(t *testing.T) {
	for _, tt := range []struct {
		name    string
		out     string
		srcVNI  uint32
		dstVNI  uint32
		want    masqPoolState
		wantErr error
	}{
		{
			name: "full pool",
			out:  readMasqStateSample(t, "full-v0.38.2.txt"),
			want: masqPoolState{HeldPorts: 64512, Full: true},
		},
		{
			name: "partly used pool",
			out:  readMasqStateSample(t, "partly-used-v0.38.4.txt"),
			want: masqPoolState{HeldPorts: 22976, Ranges: 56},
		},
		{
			name: "idle pool with dropped weak references",
			out:  readMasqStateSample(t, "idle-v0.38.4.txt"),
			want: masqPoolState{},
		},
		{
			name: "idle pool with no allocated IPs",
			out: masqStateTCP(`      IP ranges in pool:
        [100.100.1.1 .. 100.100.1.2]
      allocated IPs:
        (empty)`),
			want: masqPoolState{},
		},
		{
			name: "single ports next to ranges, wrapped across lines",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            1024-1027, 2000,
            3000-3001,
            4096, 65535`),
			want: masqPoolState{HeldPorts: 9, Ranges: 5},
		},
		{
			name: "one IP full and one partly used is not a full pool",
			out:  masqStateMixed,
			want: masqPoolState{HeldPorts: 64612, Ranges: 1},
		},
		{
			name: "all IPs full but one still free is not a full pool",
			out: masqStateTCP(`      IP ranges in pool:
        [100.100.1.2 .. 100.100.1.2]
      allocated IPs:
        100.100.1.1:
          allocated ports:
            [all ports allocated]`),
			want: masqPoolState{HeldPorts: 64512},
		},
		{
			name: "dropped weak references before an allocated IP",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        <1 weak references dropped>
        100.100.1.1:
          allocated ports:
            1024-1033`),
			want: masqPoolState{HeldPorts: 10, Ranges: 1},
		},
		{
			name: "dropped weak references after an allocated IP with ports",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            1024-1033
        <2 weak references dropped>
        100.100.1.2:
          allocated ports:
            2000`),
			want: masqPoolState{HeldPorts: 11, Ranges: 2},
		},
		{
			name: "ICMP, UDP and other VPC pairs are ignored",
			out:  masqStateICMPUDPOtherPair,
			want: masqPoolState{HeldPorts: 10, Ranges: 1},
		},
		{
			name:    "no pool from another source VPC",
			out:     readMasqStateSample(t, "idle-v0.38.4.txt"),
			srcVNI:  301,
			wantErr: errMasqPoolNotFound,
		},
		{
			name:    "no pool towards another destination VPC",
			out:     readMasqStateSample(t, "idle-v0.38.4.txt"),
			dstVNI:  401,
			wantErr: errMasqPoolNotFound,
		},
		{
			name:    "empty output",
			out:     "",
			wantErr: errMasqPoolNotFound,
		},
		{
			name: "unknown line",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        something new`),
			wantErr: errMasqStateMalformed,
		},
		{
			name: "bad port",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            1024-1027, 7000x`),
			wantErr: errMasqStateMalformed,
		},
		{
			name: "port out of range",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            65530-65536`),
			wantErr: errMasqStateMalformed,
		},
		{
			name: "reversed port range",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:
            2000-1000`),
			wantErr: errMasqStateMalformed,
		},
		{
			name: "allocated IP without ports",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:`),
			wantErr: errMasqStateMalformed,
		},
		{
			name: "empty port list",
			out: masqStateTCP(`      IP ranges in pool:
      allocated IPs:
        100.100.1.1:
          allocated ports:`),
			wantErr: errMasqStateMalformed,
		},
		{
			name: "pool cut before its allocated IPs",
			out: `  TCP | source VPC: VNI(300), dest VPC: VNI(400), for IPs: [ 10.10.1.0 .. 10.10.1.255 ]
    idle timeout: 1800s`,
			wantErr: errMasqStateMalformed,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srcVNI, dstVNI := tt.srcVNI, tt.dstVNI
			if srcVNI == 0 {
				srcVNI = 300
			}
			if dstVNI == 0 {
				dstVNI = 400
			}

			got, err := parseMasqueradeState(tt.out, srcVNI, dstVNI)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, masqPoolState{}, got)

				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMasqPoolStateReleasedTo(t *testing.T) {
	full, err := parseMasqueradeState(readMasqStateSample(t, "full-v0.38.2.txt"), 300, 400)
	require.NoError(t, err)
	partlyUsed, err := parseMasqueradeState(readMasqStateSample(t, "partly-used-v0.38.4.txt"), 300, 400)
	require.NoError(t, err)
	idle, err := parseMasqueradeState(readMasqStateSample(t, "idle-v0.38.4.txt"), 300, 400)
	require.NoError(t, err)
	mixed, err := parseMasqueradeState(masqStateMixed, 300, 400)
	require.NoError(t, err)

	for _, tt := range []struct {
		name  string
		state masqPoolState
		level int
		want  bool
	}{
		{name: "full pool is never released, whatever the level", state: full, level: 65536, want: false},
		{name: "full pool is not released to 0", state: full, level: 0, want: false},
		{name: "partly used pool at the level", state: partlyUsed, level: 22976, want: true},
		{name: "partly used pool above the level", state: partlyUsed, level: 22975, want: false},
		{name: "idle pool released to 0", state: idle, level: 0, want: true},
		{name: "one IP full and one partly used at the level", state: mixed, level: 64612, want: true},
		{name: "one IP full and one partly used above the level", state: mixed, level: 64611, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.state.ReleasedTo(tt.level))
		})
	}
}
