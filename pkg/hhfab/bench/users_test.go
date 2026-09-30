// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kvalidation "k8s.io/apimachinery/pkg/util/validation"
)

func TestUsersOptsDefaults(t *testing.T) {
	t.Parallel()

	opts := UsersOpts{UpdateWorkers: 2, InspectWorkers: 1}
	opts.setDefaults()

	require.Equal(t, DefaultUpdateSleep, opts.UpdateSleep)
	require.Equal(t, DefaultInspectSleep, opts.InspectSleep)
	require.Equal(t, DefaultOpTimeout, opts.OpTimeout)
	require.Equal(t, DefaultUserKinds, opts.Kinds)
	require.Equal(t, DefaultUserInspects, opts.Inspects)
	require.NoError(t, opts.validate())
}

func TestUsersOptsValidate(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		opts UsersOpts
		want string
	}{
		"unknown kind": {
			opts: UsersOpts{UpdateWorkers: 1, Kinds: []string{"vpc"}, Inspects: DefaultUserInspects},
			want: "unknown kind",
		},
		"unknown inspect": {
			opts: UsersOpts{InspectWorkers: 1, Kinds: DefaultUserKinds, Inspects: []string{"switch"}},
			want: "unknown inspect",
		},
		"no workers": {
			opts: UsersOpts{Kinds: DefaultUserKinds, Inspects: DefaultUserInspects},
			want: "nothing to do",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.opts.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNewObjectKnownKinds(t *testing.T) {
	t.Parallel()

	// Every kind validate() accepts must be constructible, or an accepted
	// --kinds value would panic at the first update.
	for _, kind := range DefaultUserKinds {
		require.NotNil(t, newObject(kind), "no object for kind %q", kind)
	}

	require.Nil(t, newObject("nope"))
}

func TestRandomMACIsParseableAndLocal(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for range 50 {
		mac := randomMAC()

		parsed, err := net.ParseMAC(mac)
		require.NoError(t, err, "inspect mac rejects anything net.ParseMAC cannot read")
		require.Len(t, parsed, 6)

		// Locally administered and unicast, so it can never collide with a real
		// burned-in address.
		require.Equal(t, byte(0x02), parsed[0]&0x02, "locally administered bit")
		require.Zero(t, parsed[0]&0x01, "unicast bit")

		seen[mac] = true
	}

	require.Greater(t, len(seen), 40, "addresses should vary")
}

func TestTouchLabelValueIsValidAndChanges(t *testing.T) {
	t.Parallel()

	// The value must change every write or the apiserver collapses the update
	// into a no-op: no resourceVersion bump, no watch event, no reconcile.
	first := strconv.FormatInt(time.Now().UnixNano(), 10)
	time.Sleep(time.Millisecond)
	second := strconv.FormatInt(time.Now().UnixNano(), 10)

	require.NotEqual(t, first, second)

	for _, val := range []string{first, second} {
		require.LessOrEqual(t, len(val), 63, "label values are capped at 63 characters")

		require.Empty(t, kvalidation.IsValidLabelValue(val), "%s must be a legal label value", val)
	}
}

func TestStatsClassifiesOutcomes(t *testing.T) {
	t.Parallel()

	stats := &userStats{ops: map[string]*opStat{}}
	ctx := t.Context()

	stats.record(ctx, "update:connection", time.Second, nil)
	stats.record(ctx, "update:connection", time.Second,
		kapierrors.NewInvalid(schema.GroupKind{Kind: "Connection"}, "c1", nil))
	stats.record(ctx, "update:connection", time.Second,
		kapierrors.NewTimeoutError("too slow", 1))
	stats.record(ctx, "update:connection", time.Second, errors.New("connection reset")) //nolint:err113

	snap := stats.snapshot(false)
	stat := snap.ops["update:connection"]

	require.Equal(t, int64(4), stat.Count)
	require.Len(t, stat.Latencies, 1, "only successes contribute latency")

	// A webhook refusing the write is a different signal from the API being
	// unreachable: it means validation still holds under load.
	require.Equal(t, int64(1), stat.Rejected)
	require.Equal(t, int64(1), stat.Timeouts)
	require.Equal(t, int64(1), stat.Errors)
}

func TestStatsIgnoresShutdownErrors(t *testing.T) {
	t.Parallel()

	// A request aborted because the run ended is not a fault; counting it would
	// make a clean run look like it failed at the last moment.
	stats := &userStats{ops: map[string]*opStat{}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats.record(ctx, "inspect:lldp", time.Second, context.Canceled)

	require.Empty(t, stats.snapshot(false).ops)
}

func TestStatsDrainsLatenciesPerWindow(t *testing.T) {
	t.Parallel()

	// Each periodic line should describe its own window, not the whole run.
	stats := &userStats{ops: map[string]*opStat{}}
	ctx := t.Context()

	stats.record(ctx, "inspect:mac", time.Second, nil)
	require.Len(t, stats.snapshot(true).ops["inspect:mac"].Latencies, 1)

	snap := stats.snapshot(false)
	require.Empty(t, snap.ops["inspect:mac"].Latencies)
	require.Equal(t, int64(1), snap.ops["inspect:mac"].Count, "counts are cumulative")

	// The summary reads All, which survives draining - otherwise every periodic
	// line would leave the closing per-operation figures empty.
	require.Len(t, snap.ops["inspect:mac"].All, 1)
}

func TestPickStaysInRange(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b", "c"}
	for range 50 {
		require.Contains(t, items, pick(items))
	}
}
