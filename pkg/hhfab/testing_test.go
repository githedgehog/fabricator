// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
)

func TestVLANsFrom(t *testing.T) {
	for _, test := range []struct {
		name     string
		ranges   []meta.VLANRange
		expected []uint16
	}{
		{
			name: "empty",
		},
		{
			name: "one range",
			ranges: []meta.VLANRange{
				{From: 100, To: 105},
			},
			expected: []uint16{100, 101, 102, 103, 104, 105},
		},
		{
			name: "multiple ranges",
			ranges: []meta.VLANRange{
				{From: 100, To: 105},
				{From: 200, To: 202},
			},
			expected: []uint16{100, 101, 102, 103, 104, 105, 200, 201, 202},
		},
		{
			name: "invalid range",
			ranges: []meta.VLANRange{
				{From: 100, To: 99},
			},
		},
		{
			name: "single elem range",
			ranges: []meta.VLANRange{
				{From: 100, To: 100},
			},
			expected: []uint16{100},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := slices.Collect(VLANsFrom(test.ranges...))

			require.Equal(t, test.expected, got)
		})
	}
}

func TestAddrsFrom(t *testing.T) {
	for _, test := range []struct {
		name     string
		prefixes []netip.Prefix
		expected []netip.Prefix
	}{
		{
			name: "empty",
		},
		{
			name: "one addr prefix",
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/32"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/32"),
			},
		},
		{
			name: "one addr multi prefix",
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/32"),
				netip.MustParsePrefix("10.0.1.0/32"),
				netip.MustParsePrefix("10.0.1.2/32"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/32"),
				netip.MustParsePrefix("10.0.1.0/32"),
				netip.MustParsePrefix("10.0.1.2/32"),
			},
		},
		{
			name: "multi prefix",
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/30"),
				netip.MustParsePrefix("10.0.2.5/32"),
				netip.MustParsePrefix("10.0.1.100/31"),
				netip.MustParsePrefix("10.0.1.199/31"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/30"),
				netip.MustParsePrefix("10.0.0.1/30"),
				netip.MustParsePrefix("10.0.0.2/30"),
				netip.MustParsePrefix("10.0.0.3/30"),
				netip.MustParsePrefix("10.0.2.5/32"),
				netip.MustParsePrefix("10.0.1.100/31"),
				netip.MustParsePrefix("10.0.1.101/31"),
				netip.MustParsePrefix("10.0.1.198/31"),
				netip.MustParsePrefix("10.0.1.199/31"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := slices.Collect(AddrsFrom(test.prefixes...))

			gotStr := mapSlice(prefixToString, got)
			expectedStr := mapSlice(prefixToString, test.expected)

			require.Equal(t, expectedStr, gotStr)
		})
	}
}

func TestSubPrefixesFrom(t *testing.T) {
	for _, test := range []struct {
		name     string
		bits     int
		prefixes []netip.Prefix
		expected []netip.Prefix
	}{
		{
			name: "no prefixes",
			bits: 24,
		},
		{
			name: "prefixes smaller than bits",
			bits: 24,
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/25"),
				netip.MustParsePrefix("10.0.1.0/25"),
				netip.MustParsePrefix("10.0.2.0/25"),
			},
		},
		{
			name: "one prefix same len",
			bits: 24,
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/24"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/24"),
			},
		},
		{
			name: "one prefix bigger len",
			bits: 24,
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/21"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/24"),
				netip.MustParsePrefix("10.0.1.0/24"),
				netip.MustParsePrefix("10.0.2.0/24"),
				netip.MustParsePrefix("10.0.3.0/24"),
				netip.MustParsePrefix("10.0.4.0/24"),
				netip.MustParsePrefix("10.0.5.0/24"),
				netip.MustParsePrefix("10.0.6.0/24"),
				netip.MustParsePrefix("10.0.7.0/24"),
			},
		},
		{
			name: "one unmasked prefix bigger len",
			bits: 24,
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.1.42/21"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.0/24"),
				netip.MustParsePrefix("10.0.1.0/24"),
				netip.MustParsePrefix("10.0.2.0/24"),
				netip.MustParsePrefix("10.0.3.0/24"),
				netip.MustParsePrefix("10.0.4.0/24"),
				netip.MustParsePrefix("10.0.5.0/24"),
				netip.MustParsePrefix("10.0.6.0/24"),
				netip.MustParsePrefix("10.0.7.0/24"),
			},
		},
		{
			name: "one prefix smaller bits",
			bits: 31,
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.96/29"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.96/31"),
				netip.MustParsePrefix("10.0.0.98/31"),
				netip.MustParsePrefix("10.0.0.100/31"),
				netip.MustParsePrefix("10.0.0.102/31"),
			},
		},
		{
			name: "one prefix min bits",
			bits: 32,
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.96/31"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.96/32"),
				netip.MustParsePrefix("10.0.0.97/32"),
			},
		},
		{
			name: "multiple prefix smaller bits",
			bits: 31,
			prefixes: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.96/29"),
				netip.MustParsePrefix("10.0.0.205/29"),
			},
			expected: []netip.Prefix{
				netip.MustParsePrefix("10.0.0.96/31"),
				netip.MustParsePrefix("10.0.0.98/31"),
				netip.MustParsePrefix("10.0.0.100/31"),
				netip.MustParsePrefix("10.0.0.102/31"),
				netip.MustParsePrefix("10.0.0.200/31"),
				netip.MustParsePrefix("10.0.0.202/31"),
				netip.MustParsePrefix("10.0.0.204/31"),
				netip.MustParsePrefix("10.0.0.206/31"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := slices.Collect(SubPrefixesFrom(test.bits, test.prefixes...))

			gotStr := mapSlice(prefixToString, got)
			expectedStr := mapSlice(prefixToString, test.expected)

			require.Equal(t, expectedStr, gotStr)
		})
	}
}

func TestCollectN(t *testing.T) {
	for _, test := range []struct {
		name     string
		n        int
		seq      []int
		expected []int
	}{
		{
			name: "empty",
			n:    3,
		},
		{
			name:     "less than n",
			n:        5,
			seq:      []int{1, 2, 3},
			expected: []int{1, 2, 3},
		},
		{
			name:     "equal to n",
			n:        3,
			seq:      []int{1, 2, 3},
			expected: []int{1, 2, 3},
		},
		{
			name:     "more than n",
			n:        2,
			seq:      []int{1, 2, 3},
			expected: []int{1, 2},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := CollectN(test.n, slices.Values(test.seq))

			require.Equal(t, test.expected, got)
		})
	}
}

func TestParsePingLostSeqs(t *testing.T) {
	// Fixtures are real `ping -i 0.5 -c N -W 1` stdout captured from CI job
	// https://github.com/githedgehog/fabricator/actions/runs/28009366936/job/82899549177
	// (h-gw-iso-l2vni-rt), pasted verbatim from the "Ping result" debug lines.

	// 5/5 clean.
	const allReceived = `PING 10.20.1.4 (10.20.1.4) 56(84) bytes of data.
64 bytes from 10.20.1.4: icmp_seq=1 ttl=62 time=0.253 ms
64 bytes from 10.20.1.4: icmp_seq=2 ttl=62 time=0.400 ms
64 bytes from 10.20.1.4: icmp_seq=3 ttl=62 time=0.487 ms
64 bytes from 10.20.1.4: icmp_seq=4 ttl=62 time=0.425 ms
64 bytes from 10.20.1.4: icmp_seq=5 ttl=62 time=0.478 ms
--- 10.20.1.4 ping statistics ---
5 packets transmitted, 5 received, 0% packet loss, time 2016ms
rtt min/avg/max/mdev = 0.253/0.408/0.487/0.084 ms
`

	// The actual flake: sent 5, rcvd 4, first reply is icmp_seq=2 (seq 1 dropped
	// during next-hop resolution / convergence tail).
	const firstLost = `PING 10.20.4.2 (10.20.4.2) 56(84) bytes of data.
64 bytes from 10.20.4.2: icmp_seq=2 ttl=61 time=0.611 ms
64 bytes from 10.20.4.2: icmp_seq=3 ttl=61 time=0.844 ms
64 bytes from 10.20.4.2: icmp_seq=4 ttl=61 time=0.885 ms
64 bytes from 10.20.4.2: icmp_seq=5 ttl=61 time=1.31 ms
--- 10.20.4.2 ping statistics ---
5 packets transmitted, 4 received, 20% packet loss, time 2010ms
rtt min/avg/max/mdev = 0.611/0.912/1.308/0.251 ms
`

	// 100% loss (no reply lines at all).
	const allLost = `PING 10.20.2.3 (10.20.2.3) 56(84) bytes of data.
--- 10.20.2.3 ping statistics ---
5 packets transmitted, 0 received, 100% packet loss, time 2014ms
`

	// Derived from the real reply format above, dropping a middle / last reply.
	const middleLost = `PING 10.20.1.4 (10.20.1.4) 56(84) bytes of data.
64 bytes from 10.20.1.4: icmp_seq=1 ttl=62 time=0.253 ms
64 bytes from 10.20.1.4: icmp_seq=2 ttl=62 time=0.400 ms
64 bytes from 10.20.1.4: icmp_seq=4 ttl=62 time=0.425 ms
64 bytes from 10.20.1.4: icmp_seq=5 ttl=62 time=0.478 ms
--- 10.20.1.4 ping statistics ---
5 packets transmitted, 4 received, 20% packet loss, time 2016ms
`

	const lastLost = `PING 10.20.1.4 (10.20.1.4) 56(84) bytes of data.
64 bytes from 10.20.1.4: icmp_seq=1 ttl=62 time=0.253 ms
64 bytes from 10.20.1.4: icmp_seq=2 ttl=62 time=0.400 ms
64 bytes from 10.20.1.4: icmp_seq=3 ttl=62 time=0.487 ms
64 bytes from 10.20.1.4: icmp_seq=4 ttl=62 time=0.425 ms
--- 10.20.1.4 ping statistics ---
5 packets transmitted, 4 received, 20% packet loss, time 2016ms
`

	// Format guard for the "bytes from" check: an ICMP error line carries
	// icmp_seq= but is not an echo reply, so that seq must still count as lost.
	// iputils format; not captured in the run above (no unreachables occurred).
	const icmpError = `PING 10.20.4.2 (10.20.4.2) 56(84) bytes of data.
64 bytes from 10.20.4.2: icmp_seq=1 ttl=61 time=0.5 ms
From 10.20.4.1 icmp_seq=2 Destination Host Unreachable
64 bytes from 10.20.4.2: icmp_seq=3 ttl=61 time=0.5 ms
--- 10.20.4.2 ping statistics ---
3 packets transmitted, 2 received, 33% packet loss, time 2010ms
`

	// Same first-packet loss but with -D reply timestamps ([unixtime] prefix, real
	// `ping -D` format from iputils 20240117); the prefix must not confuse parsing.
	const firstLostTimestamped = `PING 10.20.4.2 (10.20.4.2) 56(84) bytes of data.
[1782458023.423317] 64 bytes from 10.20.4.2: icmp_seq=2 ttl=61 time=0.611 ms
[1782458023.927269] 64 bytes from 10.20.4.2: icmp_seq=3 ttl=61 time=0.844 ms
[1782458024.431201] 64 bytes from 10.20.4.2: icmp_seq=4 ttl=61 time=0.885 ms
[1782458024.935112] 64 bytes from 10.20.4.2: icmp_seq=5 ttl=61 time=1.31 ms
--- 10.20.4.2 ping statistics ---
5 packets transmitted, 4 received, 20% packet loss, time 2010ms
rtt min/avg/max/mdev = 0.611/0.912/1.308/0.251 ms
`

	for _, test := range []struct {
		name     string
		stdout   string
		sent     int
		expected []int
	}{
		{name: "all received", stdout: allReceived, sent: 5},
		{name: "first lost (real flake)", stdout: firstLost, sent: 5, expected: []int{1}},
		{name: "first lost with -D timestamps", stdout: firstLostTimestamped, sent: 5, expected: []int{1}},
		{name: "middle lost", stdout: middleLost, sent: 5, expected: []int{3}},
		{name: "last lost", stdout: lastLost, sent: 5, expected: []int{5}},
		{name: "all lost", stdout: allLost, sent: 5, expected: []int{1, 2, 3, 4, 5}},
		{name: "icmp error line is not a reply", stdout: icmpError, sent: 3, expected: []int{2}},
		{name: "sent zero returns nil", stdout: allReceived, sent: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, parsePingLostSeqs(test.stdout, test.sent))
		})
	}
}

func mapSlice[IN, OUT any](f func(IN) OUT, in []IN) []OUT {
	out := make([]OUT, len(in))
	for i, v := range in {
		out[i] = f(v)
	}

	return out
}

func prefixToString(prefix netip.Prefix) string {
	return prefix.String()
}
