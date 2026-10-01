// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// The drainWatch tests run inside a synctest bubble, where the clock is fake
// and only advances once every goroutine in the bubble is blocked. Timers fire
// at exact virtual times, so the results do not depend on how busy the machine
// running them is, and the durations can be asserted exactly.

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
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan watch.Event, 1)

		go func() {
			time.Sleep(20 * time.Millisecond)
			ch <- genEvent(7)
		}()

		start := time.Now()
		got := drainWatch(t.Context(), ch, 5, 100*time.Millisecond, time.Second)

		require.Equal(t, int64(7), got, "a rewrite inside the quiet period must be coalesced")
		require.Equal(t, 120*time.Millisecond, time.Since(start), "the quiet period restarts at the rewrite")
	})
}

func TestDrainWatchReturnsAfterQuietNotLimit(t *testing.T) {
	t.Parallel()

	// A lone rewrite costs the quiet period, not the full cap - which is what
	// keeps the simulated apply latency matching the real agent's.
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan watch.Event)

		start := time.Now()
		got := drainWatch(t.Context(), ch, 3, 50*time.Millisecond, 5*time.Second)

		require.Equal(t, int64(3), got)
		require.Equal(t, 50*time.Millisecond, time.Since(start), "must not wait for the cap when the watch is quiet")
	})
}

func TestDrainWatchStopsAtLimitUnderSteadyEvents(t *testing.T) {
	t.Parallel()

	// Events arrive faster than the quiet period forever; the cap is what ends
	// the drain, and the newest generation seen is returned.
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan watch.Event)
		done := make(chan struct{})
		exited := make(chan struct{})

		// The pause between events also watches done: once the test returns
		// the bubble's clock stops, so a plain Sleep would never end.
		go func() {
			defer close(exited)

			for gen := int64(10); ; gen++ {
				select {
				case <-done:
					return
				case ch <- genEvent(gen):
				}

				select {
				case <-done:
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}()

		start := time.Now()
		got := drainWatch(t.Context(), ch, 1, 50*time.Millisecond, 200*time.Millisecond)
		close(done)
		<-exited

		require.Equal(t, 200*time.Millisecond, time.Since(start), "the cap, not the quiet period, must end the drain")
		require.Greater(t, got, int64(10), "should have kept the newest generation")
	})
}

func TestDrainWatchClosedChannel(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ch := make(chan watch.Event)
		close(ch)

		start := time.Now()
		require.Equal(t, int64(4), drainWatch(t.Context(), ch, 4, time.Second, time.Second))
		require.Zero(t, time.Since(start), "a closed watch must return without waiting")
	})
}
