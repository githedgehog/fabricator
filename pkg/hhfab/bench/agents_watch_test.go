// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func genEvent(gen int64) watch.Event {
	return watch.Event{
		Type:   watch.Modified,
		Object: &agentapi.Agent{ObjectMeta: kmetav1.ObjectMeta{Generation: gen}},
	}
}

func TestDrainWatchWaitsForQuietBeforeReturning(t *testing.T) {
	t.Parallel()

	// Nothing is queued when drainWatch starts, but a second rewrite lands
	// shortly after. Returning immediately - the old behaviour - would miss it
	// and cost a second apply.
	ch := make(chan watch.Event, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		ch <- genEvent(7)
	}()

	start := time.Now()
	got := drainWatch(t.Context(), ch, 5, 100*time.Millisecond, time.Second)

	require.Equal(t, int64(7), got, "a rewrite inside the quiet period must be coalesced")
	require.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "must wait out the quiet period")
}

func TestDrainWatchReturnsAfterQuietNotLimit(t *testing.T) {
	t.Parallel()

	// A lone rewrite costs the quiet period, not the full cap - which is what
	// keeps the simulated apply latency matching the real agent's.
	ch := make(chan watch.Event)

	start := time.Now()
	got := drainWatch(t.Context(), ch, 3, 50*time.Millisecond, 5*time.Second)

	require.Equal(t, int64(3), got)
	require.Less(t, time.Since(start), time.Second, "must not wait for the cap when the watch is quiet")
}

func TestDrainWatchStopsAtLimitUnderSteadyEvents(t *testing.T) {
	t.Parallel()

	// Events arrive faster than the quiet period forever; the cap is what ends
	// the drain, and the newest generation seen is returned.
	ch := make(chan watch.Event)
	done := make(chan struct{})

	go func() {
		for gen := int64(10); ; gen++ {
			select {
			case <-done:
				return
			case ch <- genEvent(gen):
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	start := time.Now()
	got := drainWatch(t.Context(), ch, 1, 50*time.Millisecond, 200*time.Millisecond)
	close(done)

	took := time.Since(start)
	require.GreaterOrEqual(t, took, 200*time.Millisecond)
	require.Less(t, took, time.Second)
	require.Greater(t, got, int64(10), "should have kept the newest generation")
}

func TestDrainWatchClosedChannel(t *testing.T) {
	t.Parallel()

	ch := make(chan watch.Event)
	close(ch)

	require.Equal(t, int64(4), drainWatch(t.Context(), ch, 4, time.Second, time.Second))
}
