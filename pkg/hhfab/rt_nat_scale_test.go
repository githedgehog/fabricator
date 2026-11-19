// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
)

func TestNATScaleFillDecision(t *testing.T) {
	const preFill = 1000
	// natScaleMinPortsPerSec new ports per second over the rate window
	atRate := preFill + natScaleMinPortsPerSec*int(natScaleRateWindow/time.Second)

	for _, tt := range []struct {
		name        string
		pool        masqPoolState
		elapsed     time.Duration
		sourcesUsed int
		loadDone    bool
		rateChecked bool
		want        natScaleFillOutcome
	}{
		{name: "filling", pool: masqPoolState{HeldPorts: 5000}, elapsed: 30 * time.Second, sourcesUsed: 1, want: natScaleFillContinue},
		{name: "full", pool: masqPoolState{HeldPorts: natScalePoolPorts, Full: true}, elapsed: 100 * time.Second, sourcesUsed: 3, rateChecked: true, want: natScaleFillFull},
		{name: "full exactly at the fill timeout", pool: masqPoolState{HeldPorts: natScalePoolPorts, Full: true}, elapsed: natScaleFillTimeout, sourcesUsed: 4, rateChecked: true, want: natScaleFillFull},
		{name: "full with the last load done", pool: masqPoolState{HeldPorts: natScalePoolPorts, Full: true}, elapsed: 200 * time.Second, sourcesUsed: natScaleSourceIPs, loadDone: true, rateChecked: true, want: natScaleFillFull},
		{name: "just before the fill timeout", pool: masqPoolState{HeldPorts: 60000}, elapsed: natScaleFillTimeout - time.Nanosecond, sourcesUsed: 4, rateChecked: true, want: natScaleFillContinue},
		{name: "exactly at the fill timeout", pool: masqPoolState{HeldPorts: 60000}, elapsed: natScaleFillTimeout, sourcesUsed: 4, rateChecked: true, want: natScaleFillTimedOut},
		{name: "the fill timeout before the sources out", pool: masqPoolState{HeldPorts: 60000}, elapsed: natScaleFillTimeout, sourcesUsed: natScaleSourceIPs, loadDone: true, rateChecked: true, want: natScaleFillTimedOut},
		{name: "exactly at the rate threshold", pool: masqPoolState{HeldPorts: atRate}, elapsed: natScaleRateWindow, sourcesUsed: 1, want: natScaleFillContinue},
		{name: "one port below the rate threshold", pool: masqPoolState{HeldPorts: atRate - 1}, elapsed: natScaleRateWindow, sourcesUsed: 1, want: natScaleFillSlow},
		{name: "slow before the rate window", pool: masqPoolState{HeldPorts: preFill}, elapsed: natScaleRateWindow - time.Nanosecond, sourcesUsed: 1, want: natScaleFillContinue},
		{name: "slow after the rate was checked", pool: masqPoolState{HeldPorts: preFill}, elapsed: 2 * natScaleRateWindow, sourcesUsed: 1, rateChecked: true, want: natScaleFillContinue},
		{name: "slow before the sources out", pool: masqPoolState{HeldPorts: atRate - 1}, elapsed: natScaleRateWindow, sourcesUsed: natScaleSourceIPs, loadDone: true, want: natScaleFillSlow},
		{name: "sources out", pool: masqPoolState{HeldPorts: 60000}, elapsed: 200 * time.Second, sourcesUsed: natScaleSourceIPs, loadDone: true, rateChecked: true, want: natScaleFillSourcesOut},
		{name: "a load done with sources left", pool: masqPoolState{HeldPorts: 40000}, elapsed: 150 * time.Second, sourcesUsed: natScaleSourceIPs - 1, loadDone: true, rateChecked: true, want: natScaleFillContinue},
		{name: "the last load still running", pool: masqPoolState{HeldPorts: 60000}, elapsed: 200 * time.Second, sourcesUsed: natScaleSourceIPs, rateChecked: true, want: natScaleFillContinue},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, natScaleFillDecision(tt.pool, preFill, tt.elapsed, tt.sourcesUsed, tt.loadDone, tt.rateChecked))
		})
	}
}

func TestNATScaleReleaseDecision(t *testing.T) {
	const preFill = 1000

	for _, tt := range []struct {
		name    string
		pool    masqPoolState
		preFill int
		since   time.Duration
		want    natScaleReleaseOutcome
	}{
		{name: "back to the pre-fill level", pool: masqPoolState{HeldPorts: preFill}, preFill: preFill, since: time.Minute, want: natScaleReleased},
		{name: "below the pre-fill level", pool: masqPoolState{HeldPorts: 1}, preFill: preFill, since: time.Minute, want: natScaleReleased},
		{name: "one port above the pre-fill level", pool: masqPoolState{HeldPorts: preFill + 1}, preFill: preFill, since: time.Minute, want: natScaleReleaseWait},
		{name: "just before the release timeout", pool: masqPoolState{HeldPorts: preFill + 1}, preFill: preFill, since: natScaleReleaseTimeout - time.Nanosecond, want: natScaleReleaseWait},
		{name: "exactly at the release timeout", pool: masqPoolState{HeldPorts: preFill + 1}, preFill: preFill, since: natScaleReleaseTimeout, want: natScaleReleaseTimedOut},
		{name: "released exactly at the release timeout", pool: masqPoolState{HeldPorts: preFill}, preFill: preFill, since: natScaleReleaseTimeout, want: natScaleReleased},
		{name: "a full pool", pool: masqPoolState{HeldPorts: natScalePoolPorts, Full: true}, preFill: preFill, since: time.Minute, want: natScaleReleaseWait},
		{name: "a full pool at the pre-fill level", pool: masqPoolState{HeldPorts: natScalePoolPorts, Full: true}, preFill: natScalePoolPorts, since: time.Minute, want: natScaleReleaseWait},
		{name: "a full pool at the release timeout", pool: masqPoolState{HeldPorts: natScalePoolPorts, Full: true}, preFill: preFill, since: natScaleReleaseTimeout, want: natScaleReleaseTimedOut},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, natScaleReleaseDecision(tt.pool, tt.preFill, tt.since))
		})
	}
}

func TestParseLoadDone(t *testing.T) {
	for _, tt := range []struct {
		name     string
		out      string
		wantAgo  time.Duration
		wantDone bool
		wantErr  error
	}{
		{name: "still running", out: "1794532310\n"},
		{name: "still running with an empty exit time", out: "1794532310\n \n"},
		{name: "exited", out: "1794532310\n@1794532306\n", wantAgo: 4 * time.Second, wantDone: true},
		{name: "exited without a trailing newline", out: "1794532306\n@1794532306", wantDone: true},
		{name: "malformed exit time", out: "1794532310\n@17945x2306\n", wantErr: errNATScaleUnitOutput},
		{name: "exit time without @", out: "1794532310\n1794532306\n", wantErr: errNATScaleUnitOutput},
		{name: "missing server time", out: "\n@1794532306\n", wantErr: errNATScaleUnitOutput},
		{name: "malformed server time", out: "now\n@1794532306\n", wantErr: errNATScaleUnitOutput},
		{name: "extra lines", out: "1794532310\n@1794532306\n1794532310\n", wantErr: errNATScaleUnitOutput},
		{name: "no output", out: "", wantErr: errNATScaleUnitOutput},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ago, done, err := parseLoadDone(tt.out)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantDone, done)
			require.Equal(t, tt.wantAgo, ago)
		})
	}
}

func TestGatewayChange(t *testing.T) {
	pod := func(name, uid string, restarts int32) corev1.Pod {
		return corev1.Pod{
			Name: name,
			UID:  ktypes.UID(uid),
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "frr", RestartCount: 1},
				{
					Name:                 "dataplane",
					RestartCount:         restarts,
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
				},
			}},
		}
	}
	start := []corev1.Pod{pod("gw-a", "a", 2), pod("gw-b", "b", 0)}

	require.NoError(t, gatewayChange(start, []corev1.Pod{pod("gw-a", "a", 2), pod("gw-b", "b", 0)}), "restarts before the test")
	require.EqualError(t, gatewayChange(start, []corev1.Pod{pod("gw-a", "a", 2)}), "a gateway pod was replaced or restarted: pod gw-b is gone")
	require.EqualError(t, gatewayChange(start, []corev1.Pod{pod("gw-a", "a", 3), pod("gw-b", "b", 0)}),
		"a gateway pod was replaced or restarted: container dataplane of pod gw-a restarted 1 times during the test, last exit Error code 1")
	require.EqualError(t, gatewayChange(start, []corev1.Pod{pod("gw-a", "a", 2), pod("gw-b", "b", 0), pod("gw-c", "c", 0)}),
		"a gateway pod was replaced or restarted: pod gw-c is new")

	noStatus := pod("gw-b", "b", 0)
	noStatus.Status.ContainerStatuses = nil
	require.ErrorIs(t, gatewayChange([]corev1.Pod{noStatus}, []corev1.Pod{pod("gw-b", "b", 1)}), errNATScaleGatewayRestart, "a container without a status at the start")
}

func TestGatewayNATPortExhaustionSkipsOnVirtualSwitches(t *testing.T) {
	testCtx := &VPCPeeringTestCtx{skipFlags: SkipFlags{VirtualSwitch: true}}

	skip, reverts, err := gatewayNATPortExhaustionTest(t.Context(), testCtx, nil)
	require.True(t, skip)
	require.Empty(t, reverts)
	require.ErrorIs(t, err, errNATScaleVirtualSwitch)
}
