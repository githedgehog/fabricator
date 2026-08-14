// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
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

func TestGetServerHostBGPCmd(t *testing.T) {
	unbundled := func(port string) *wiringapi.Connection {
		return &wiringapi.Connection{Spec: wiringapi.ConnectionSpec{
			Unbundled: &wiringapi.ConnUnbundled{
				Link: wiringapi.ServerToSwitchLink{Server: wiringapi.BasePortName{Port: port}},
			},
		}}
	}
	bundled := func(ports ...string) *wiringapi.Connection {
		links := []wiringapi.ServerToSwitchLink{}
		for _, port := range ports {
			links = append(links, wiringapi.ServerToSwitchLink{Server: wiringapi.BasePortName{Port: port}})
		}

		return &wiringapi.Connection{Spec: wiringapi.ConnectionSpec{Bundled: &wiringapi.ConnBundled{Links: links}}}
	}

	for _, test := range []struct {
		name     string
		params   []HostBGPParams
		expected string
		wantErr  bool
	}{
		{name: "no params", wantErr: true},
		{
			name: "single vpc single connection",
			params: []HostBGPParams{{
				VPCLabel:    "vpc-01",
				Connections: []*wiringapi.Connection{unbundled("server-01/enp2s1")},
				VLAN:        1001,
				Subnet:      netip.MustParsePrefix("10.0.1.0/24"),
			}},
			expected: "vpc-01:v=1001:i=enp2s1:a=10.0.1.0/32",
		},
		{
			name: "server offset walks the subnet",
			params: []HostBGPParams{{
				VPCLabel:     "vpc-01",
				Connections:  []*wiringapi.Connection{bundled("server-01/enp2s1", "server-01/enp2s2")},
				VLAN:         1001,
				Subnet:       netip.MustParsePrefix("10.0.1.0/24"),
				ServerOffset: 3,
			}},
			expected: "vpc-01:v=1001:i=enp2s1:i=enp2s2:a=10.0.1.3/32",
		},
		{
			name: "two vpcs are space separated",
			params: []HostBGPParams{
				{
					VPCLabel:    "vpc-01",
					Connections: []*wiringapi.Connection{unbundled("server-01/enp2s1")},
					VLAN:        1001,
					Subnet:      netip.MustParsePrefix("10.0.1.0/24"),
				},
				{
					VPCLabel:     "vpc-02",
					Connections:  []*wiringapi.Connection{unbundled("server-01/enp2s2"), unbundled("server-01/enp2s3")},
					VLAN:         1002,
					Subnet:       netip.MustParsePrefix("10.0.2.0/24"),
					ServerOffset: 1,
				},
			},
			expected: "vpc-01:v=1001:i=enp2s1:a=10.0.1.0/32 vpc-02:v=1002:i=enp2s2:i=enp2s3:a=10.0.2.1/32",
		},
		{
			name:    "no connections",
			params:  []HostBGPParams{{VPCLabel: "vpc-01", VLAN: 1001, Subnet: netip.MustParsePrefix("10.0.1.0/24")}},
			wantErr: true,
		},
		{
			name:    "nil connection",
			params:  []HostBGPParams{{VPCLabel: "vpc-01", Connections: []*wiringapi.Connection{nil}, VLAN: 1001}},
			wantErr: true,
		},
		{
			name: "unsupported connection type",
			params: []HostBGPParams{{
				VPCLabel:    "vpc-01",
				Connections: []*wiringapi.Connection{{Spec: wiringapi.ConnectionSpec{}}},
				VLAN:        1001,
			}},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := getServerHostBGPCmd(test.params)
			if test.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			require.Equal(t, test.expected, cmd)
		})
	}
}

func TestParsePingCounts(t *testing.T) {
	for _, test := range []struct {
		name     string
		stdout   string
		sent     int
		received int
	}{
		{
			name: "all received",
			stdout: `PING 10.20.1.4 (10.20.1.4) 56(84) bytes of data.
[1782458023.423317] 64 bytes from 10.20.1.4: icmp_seq=1 ttl=62 time=0.253 ms
--- 10.20.1.4 ping statistics ---
5 packets transmitted, 5 received, 0% packet loss, time 2016ms
rtt min/avg/max/mdev = 0.253/0.408/0.487/0.084 ms
`,
			sent: 5, received: 5,
		},
		{
			name: "partial loss",
			stdout: `--- 10.20.4.2 ping statistics ---
5 packets transmitted, 4 received, 20% packet loss, time 2010ms
`,
			sent: 5, received: 4,
		},
		{
			name: "total loss",
			stdout: `--- 10.20.2.3 ping statistics ---
5 packets transmitted, 0 received, 100% packet loss, time 2014ms
`,
			sent: 5,
		},
		{
			// ping appends "+N errors" after the received count when it saw ICMP
			// errors, adding a field the counts must survive.
			name: "errors reported after received",
			stdout: `--- 10.20.4.2 ping statistics ---
3 packets transmitted, 0 received, +3 errors, 100% packet loss, time 2040ms
`,
			sent: 3,
		},
		{name: "no summary line", stdout: "ping: connect: Network is unreachable\n"},
		{name: "empty output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sent, received := parsePingCounts(test.stdout)
			require.Equal(t, test.sent, sent)
			require.Equal(t, test.received, received)
		})
	}
}

func TestUDPProbeCmd(t *testing.T) {
	for _, test := range []struct {
		name      string
		secs      int
		reachable bool
		expected  string
	}{
		{
			name:     "deny probe bounds the control connect",
			secs:     3,
			expected: "sudo docker exec iperf3 timeout -k 5 18 iperf3 -u -J --connect-timeout 5000 -c 10.0.1.2 -p 5201 -t 3 -b 10M -l 1000",
		},
		{
			name:      "allow probe gets the longer connect budget",
			secs:      3,
			reachable: true,
			expected:  "sudo docker exec iperf3 timeout -k 5 28 iperf3 -u -J --connect-timeout 15000 -c 10.0.1.2 -p 5201 -t 3 -b 10M -l 1000",
		},
		{
			name:     "extended run stretches the backstop",
			secs:     10,
			expected: "sudo docker exec iperf3 timeout -k 5 25 iperf3 -u -J --connect-timeout 5000 -c 10.0.1.2 -p 5201 -t 10 -b 10M -l 1000",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			timing := udpProbeTimingFor(test.secs, test.reachable)
			require.Equal(t, test.expected, udpProbeCmd(netip.MustParseAddr("10.0.1.2"), 5201, test.secs, timing))
			// The SSH deadline must outlive the backstop, or a probe that ran to
			// completion is reported as having produced no result.
			require.Greater(t, timing.outer, timing.inner+20*time.Second)
		})
	}
}

func TestReprobeOutcome(t *testing.T) {
	// ping's own exit status 1 on packet loss cannot be built here (ssh.Waitmsg
	// carries the status in unexported fields), so the loss case is covered with
	// the counts alone; the branch that reads the status is the did-not-run one.
	for _, test := range []struct {
		name     string
		err      error
		sent     int
		received int
		expected string
	}{
		{name: "clean", sent: 5, received: 5, expected: "Diagnostic re-probe recovered"},
		{name: "partial loss", sent: 5, received: 4, expected: "Diagnostic re-probe still losing packets"},
		{name: "total loss", sent: 5, expected: "Diagnostic re-probe still losing packets"},
		{name: "no summary parsed", expected: "Diagnostic re-probe did not run"},
		{
			name: "ssh failure with counts", err: errors.New("session failed"),
			sent: 5, received: 5, expected: "Diagnostic re-probe did not run",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, reprobeOutcome(test.err, test.sent, test.received))
		})
	}
}

func TestPingProbeCmd(t *testing.T) {
	toIP := netip.MustParseAddr("10.20.4.2")
	sourceIP := netip.MustParseAddr("10.20.1.5")

	require.Equal(t, "ping -i 0.5 -c 5 -W 1 -D 10.20.4.2", pingProbeCmd(5, toIP, nil))
	require.Equal(t, "ping -i 0.5 -c 3 -W 1 -D -I 10.20.1.5 10.20.4.2", pingProbeCmd(3, toIP, &sourceIP))
}

func TestIperf3ProbeCmd(t *testing.T) {
	toIP := netip.MustParseAddr("10.30.5.2")

	for _, test := range []struct {
		name     string
		opts     TestConnectivityOpts
		srcIP    netip.Addr
		secs     int
		bidir    bool
		reverse  bool
		expected string
	}{
		{
			name: "measured probe, one direction",
			secs: 10,
			opts: TestConnectivityOpts{IPerfsSeconds: 10},
			// The wrapper outlives the run, so a client that stalls gets SIGTERM.
			expected: "sudo docker exec iperf3 timeout 35 iperf3 -P 4 -J -c 10.30.5.2 -t 10",
		},
		{
			name: "measured probe, both directions in one session", secs: 10, bidir: true,
			opts:     TestConnectivityOpts{IPerfsSeconds: 10},
			expected: "sudo docker exec iperf3 timeout 35 iperf3 -P 4 -J -c 10.30.5.2 -t 10 --bidir",
		},
		{
			name: "forward re-probe drops --bidir", secs: iperfReprobeSeconds,
			expected: "sudo docker exec iperf3 timeout 30 iperf3 -P 4 -J -c 10.30.5.2 -t 5",
		},
		{
			name: "reverse re-probe makes the server the sender", secs: iperfReprobeSeconds, reverse: true,
			expected: "sudo docker exec iperf3 timeout 30 iperf3 -P 4 -J -c 10.30.5.2 -t 5 -R",
		},
		{
			name: "marking flags survive", secs: 10, opts: TestConnectivityOpts{IPerfsSeconds: 10, IPerfsDSCP: 46, IPerfsTOS: 184},
			expected: "sudo docker exec iperf3 timeout 35 iperf3 -P 4 -J -c 10.30.5.2 -t 10 --dscp 46 --tos 184",
		},
		{
			// A re-probe has to leave from the same address as the session it follows,
			// or a hostBGP pair is measured from a different source than the one that
			// stalled.
			name: "reverse re-probe keeps the source binding", secs: iperfReprobeSeconds, reverse: true,
			srcIP:    netip.MustParseAddr("10.30.4.100"),
			expected: "sudo docker exec iperf3 timeout 30 iperf3 -P 4 -J -c 10.30.5.2 -t 5 -B 10.30.4.100 -R",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, iperf3ProbeCmd(test.opts, toIP, test.srcIP, test.secs, test.bidir, test.reverse))
		})
	}
}

func TestIperfReprobeOutcome(t *testing.T) {
	// The stall this chases reaches us as a `timeout` SIGTERM, which iperf3 reports
	// in its own JSON error field rather than as a shell failure. An ssh exit status
	// cannot be built here (ssh.Waitmsg keeps it in unexported fields), so the
	// branch that reads the status is covered by the did-not-run case.
	for _, test := range []struct {
		name     string
		err      error
		parseErr error
		report   *iperf3Report
		expected string
	}{
		{name: "report with no error", report: &iperf3Report{}, expected: "Diagnostic iperf re-probe completed"},
		{
			name:     "client killed by the timeout wrapper",
			report:   &iperf3Report{Error: "interrupt - the client has terminated by signal Terminated(15)"},
			expected: "Diagnostic iperf re-probe stalled",
		},
		{
			name: "output is not a report", parseErr: errors.New("unmarshaling iperf3 report"),
			expected: "Diagnostic iperf re-probe unreadable",
		},
		{
			name: "ssh session never delivered the command", err: errors.New("session failed"),
			expected: "Diagnostic iperf re-probe did not run",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, iperfReprobeOutcome(test.err, test.parseErr, test.report))
		})
	}
}

func TestCollapsePressure(t *testing.T) {
	for _, test := range []struct {
		name     string
		in       string
		expected string
	}{
		{
			name: "both lines",
			in: `some avg10=0.06 avg60=13.83 avg300=25.65 total=162667239576
full avg10=0.00 avg60=0.00 avg300=0.00 total=0
`,
			expected: "some avg10=0.06 avg60=13.83 avg300=25.65 total=162667239576; full avg10=0.00 avg60=0.00 avg300=0.00 total=0",
		},
		{
			name:     "single line without trailing newline",
			in:       "some avg10=0.00 avg60=0.01 avg300=0.00 total=28517747892",
			expected: "some avg10=0.00 avg60=0.01 avg300=0.00 total=28517747892",
		},
		{
			name:     "empty",
			in:       "",
			expected: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, collapsePressure([]byte(test.in)))
		})
	}
}

func TestRunnerPressure(t *testing.T) {
	// Both sources are optional by design, so this only pins that reading them
	// never panics and never returns a value spanning several log lines.
	loadavg, cpuPressure := runnerPressure()
	require.NotContains(t, loadavg, "\n")
	require.NotContains(t, cpuPressure, "\n")
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

func TestExpectationWhy(t *testing.T) {
	for _, test := range []struct {
		name     string
		r        Reachability
		expected string
	}{
		{
			name:     "allow names the peering that grants it",
			r:        Reachability{Reachable: true, Reason: ReachabilityReasonGatewayPeering, Peering: "vpc-01--vpc-02"},
			expected: `gateway-peering "vpc-01--vpc-02"`,
		},
		{
			name:     "allow with no peering falls back to the reason alone",
			r:        Reachability{Reachable: true, Reason: ReachabilityReasonIntraVPC},
			expected: "intra-vpc",
		},
		{
			name:     "an ACL deny reads as withholding a peered pair",
			r:        Reachability{Reason: ReachabilityReasonGatewayPeering, Peering: "vpc-01--vpc-02", Detail: "ACL"},
			expected: `ACL on gateway-peering "vpc-01--vpc-02"`,
		},
		{
			name:     "a deny on a peered pair with no detail still reads as a deny",
			r:        Reachability{Reason: ReachabilityReasonGatewayPeering, Peering: "vpc-01--vpc-02"},
			expected: `gateway-peering "vpc-01--vpc-02" does not allow it`,
		},
		{
			name:     "a deny with only a detail reports it",
			r:        Reachability{Detail: "gw peering with non-empty expose 'As'"},
			expected: "gw peering with non-empty expose 'As'",
		},
		{
			name:     "a pair no peering covers says so",
			r:        Reachability{},
			expected: "no peering allows it",
		},
		{
			name:     "an allow with nothing recorded admits it",
			r:        Reachability{Reachable: true},
			expected: "no reason recorded",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, expectationWhy(test.r))
		})
	}
}
