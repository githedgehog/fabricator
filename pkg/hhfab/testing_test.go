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

func TestParseIPerf3Report_DiagnosticFields(t *testing.T) {
	// Fail loudly if iperf3 renames or reshapes the keys diagnostics depend on,
	// instead of the unmarshal silently producing zero-valued fields.
	data := []byte(`{
		"intervals": [
			{"sum": {"bytes": 800000000, "bits_per_second": 6400000000, "retransmits": 12}},
			{"sum": {"bytes": 750000000, "bits_per_second": 6000000000, "retransmits": 5}}
		],
		"end": {
			"streams": [
				{
					"sender":   {"bytes": 1500000000, "bits_per_second": 1200000000, "retransmits": 7,
					             "min_rtt": 1100, "mean_rtt": 1500, "max_rtt": 2000},
					"receiver": {"bytes": 1490000000, "bits_per_second": 1192000000}
				},
				{
					"sender":   {"bytes": 2000000000, "bits_per_second": 1600000000, "retransmits": 10,
					             "min_rtt": 900, "mean_rtt": 1200, "max_rtt": 1700},
					"receiver": {"bytes": 1980000000, "bits_per_second": 1584000000}
				}
			],
			"sum_sent":     {"bytes": 7500000000, "bits_per_second": 6000000000, "retransmits": 17},
			"sum_received": {"bytes": 7400000000, "bits_per_second": 5920000000}
		}
	}`)

	report, err := parseIPerf3Report(data)
	require.NoError(t, err)
	require.NotNil(t, report)

	require.Len(t, report.Intervals, 2)
	require.Equal(t, int64(12), report.Intervals[0].Sum.Retransmits)
	require.InDelta(t, 6.4e9, report.Intervals[0].Sum.BitsPerSecond, 1)

	require.Len(t, report.End.Streams, 2)
	require.Equal(t, int64(7), report.End.Streams[0].Sender.Retransmits)
	require.InDelta(t, 1100.0, report.End.Streams[0].Sender.MinRtt, 1)
	require.InDelta(t, 1500.0, report.End.Streams[0].Sender.MeanRtt, 1)
	require.InDelta(t, 2000.0, report.End.Streams[0].Sender.MaxRtt, 1)
	require.InDelta(t, 1.192e9, report.End.Streams[0].Receiver.BitsPerSecond, 1)

	require.Equal(t, int64(17), report.End.SumSent.Retransmits)
	require.InDelta(t, 6.0e9, report.End.SumSent.BitsPerSecond, 1)
	require.InDelta(t, 5.92e9, report.End.SumReceived.BitsPerSecond, 1)
}
