// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParsePingTimeline(t *testing.T) {
	// real iputils `ping -i 0.2 -D -O` shapes: a lost seq gets a "no answer yet" line stamped
	// when the next probe goes out, one interval after the lost one
	const out = `PING 10.20.1.4 (10.20.1.4) 56(84) bytes of data.
[1782458023.000000] 64 bytes from 10.20.1.4: icmp_seq=1 ttl=62 time=0.253 ms
[1782458023.200000] 64 bytes from 10.20.1.4: icmp_seq=2 ttl=62 time=0.400 ms
[1782458023.600000] no answer yet for icmp_seq=3
[1782458023.800000] no answer yet for icmp_seq=4
[1782458024.000000] 64 bytes from 10.20.1.4: icmp_seq=6 ttl=62 time=0.425 ms
[1782458024.200000] 64 bytes from 10.20.1.4: icmp_seq=7 ttl=62 time=0.478 ms
`

	got := parsePingTimeline(out)
	require.Len(t, got, 7)
	at := func(frac string) time.Time {
		ts, ok := parsePingStamp("[" + frac + "]")
		require.True(t, ok)

		return ts
	}
	require.Equal(t, pingSample{Seq: 1, Replied: true, At: at("1782458023.000000")}, got[0])
	require.Equal(t, pingSample{Seq: 3, At: at("1782458023.600000")}, got[2])
	require.Equal(t, pingSample{Seq: 4, At: at("1782458023.800000")}, got[3])
	// seq 5 has no line at all: lost, placed one interval after the previous stamped sample
	require.Equal(t, pingSample{Seq: 5, At: at("1782458024.000000")}, got[4])
	require.True(t, got[6].Replied)
}

func TestParsePingTimelineLateReplyIsNotLost(t *testing.T) {
	const out = `[1782458023.000000] 64 bytes from 10.20.1.4: icmp_seq=1 ttl=62 time=0.2 ms
[1782458023.400000] no answer yet for icmp_seq=2
[1782458023.450000] 64 bytes from 10.20.1.4: icmp_seq=2 ttl=62 time=250 ms
[1782458023.600000] 64 bytes from 10.20.1.4: icmp_seq=3 ttl=62 time=0.2 ms
`
	got := parsePingTimeline(out)
	require.Len(t, got, 3)
	for _, s := range got {
		require.True(t, s.Replied, "seq %d", s.Seq)
	}
}

func TestParsePingTimelineIgnoresErrorLinesAndPartialTail(t *testing.T) {
	const out = `PING 10.20.4.2 (10.20.4.2) 56(84) bytes of data.
[1782458023.000000] 64 bytes from 10.20.4.2: icmp_seq=1 ttl=61 time=0.5 ms
[1782458023.200000] From 10.20.4.1 icmp_seq=2 Destination Host Unreachable
[1782458023.400000] 64 bytes from 10.20.4.2: icmp_seq=3 ttl=61 time=0.5 ms
[17824`

	got := parsePingTimeline(out)
	require.Len(t, got, 3)
	require.True(t, got[0].Replied)
	require.False(t, got[1].Replied)
	require.True(t, got[2].Replied)
}

func TestLostPingsAndGroupLoss(t *testing.T) {
	base := time.Unix(1782458000, 0)
	mk := func(seq int, offset time.Duration, replied bool) pingSample {
		return pingSample{Seq: seq, Replied: replied, At: base.Add(offset)}
	}
	var samples []pingSample
	// 10 s of probes at 0.2 s, lost from 3.0 s to 6.2 s
	for i := range 50 {
		off := time.Duration(i) * trafficPingInterval
		samples = append(samples, mk(i+1, off, off < 3*time.Second || off > 6200*time.Millisecond))
	}
	tl := []ProbeTimeline{
		{Probe: "a -> b", Samples: samples, Replies: 1},
		{Probe: "c -> d", Samples: []pingSample{mk(1, 0, true)}, Replies: 1},
	}

	all := LostPings(tl, base, base.Add(time.Minute))
	require.Len(t, all, 17)
	require.Empty(t, LostPings(tl, base, base.Add(2*time.Second)))
	require.Len(t, LostPings(tl, base.Add(3*time.Second), base.Add(4*time.Second)), 6)

	groups := GroupLoss(all, 2*trafficPingInterval)
	require.Len(t, groups, 1)
	require.Equal(t, 17, groups[0].Pings)
	require.Equal(t, 3200*time.Millisecond, groups[0].To.Sub(groups[0].From))

	require.Equal(t, []string{"c -> d"}, BrokenProbes([]ProbeTimeline{{Probe: "a -> b", Replies: 3}, {Probe: "c -> d"}}))
}
