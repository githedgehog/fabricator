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
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kvalidation "k8s.io/apimachinery/pkg/util/validation"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestPercentilesOnSmallSamples(t *testing.T) {
	t.Parallel()

	ms := func(vals ...int) []time.Duration {
		out := make([]time.Duration, 0, len(vals))
		for _, v := range vals {
			out = append(out, time.Duration(v)*time.Millisecond)
		}

		return out
	}

	for name, tc := range map[string]struct {
		in            []time.Duration
		wantP50       time.Duration
		wantP95       time.Duration
		distinctNotes string
	}{
		"empty":  {in: nil, wantP50: 0, wantP95: 0},
		"single": {in: ms(100), wantP50: 100 * time.Millisecond, wantP95: 100 * time.Millisecond},
		// The case that prompted this: two very different samples must not
		// collapse to one number.
		"pair": {
			in: ms(10, 1000), wantP50: 10 * time.Millisecond, wantP95: 1000 * time.Millisecond,
			distinctNotes: "p50 must be the lower of two, not the higher",
		},
		"three":      {in: ms(10, 50, 1000), wantP50: 50 * time.Millisecond, wantP95: 1000 * time.Millisecond},
		"four":       {in: ms(10, 20, 30, 40), wantP50: 20 * time.Millisecond, wantP95: 40 * time.Millisecond},
		"unsorted":   {in: ms(40, 10, 30, 20), wantP50: 20 * time.Millisecond, wantP95: 40 * time.Millisecond},
		"ten":        {in: ms(1, 2, 3, 4, 5, 6, 7, 8, 9, 10), wantP50: 5 * time.Millisecond, wantP95: 10 * time.Millisecond},
		"twenty_p95": {in: ms(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 100), wantP50: 10 * time.Millisecond, wantP95: 19 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p50, p95 := percentiles(tc.in)
			require.Equal(t, tc.wantP50, p50, "p50 %s", tc.distinctNotes)
			require.Equal(t, tc.wantP95, p95, "p95 %s", tc.distinctNotes)
		})
	}
}

func TestNearestRankStaysInBounds(t *testing.T) {
	t.Parallel()

	for n := 1; n <= 200; n++ {
		for _, p := range []int{0, 50, 95, 100} {
			idx := nearestRank(n, p)
			require.GreaterOrEqual(t, idx, 0, "n=%d p=%d", n, p)
			require.Less(t, idx, n, "n=%d p=%d", n, p)
		}
	}
}

func TestWindowCountResetsWithLatencies(t *testing.T) {
	t.Parallel()

	// The periodic line pairs Win with the window's percentiles, so the two
	// have to be drained together or the count describes a different set of
	// operations than the latencies do.
	stats := &userStats{ops: map[string]*opStat{}}
	ctx := t.Context()

	stats.record(ctx, "inspect:bfd", time.Second, nil)
	stats.record(ctx, "inspect:bfd", 2*time.Second, nil)

	first := stats.snapshot(true).ops["inspect:bfd"]
	require.Equal(t, int64(2), first.Win)
	require.Len(t, first.Latencies, 2)

	second := stats.snapshot(false).ops["inspect:bfd"]
	require.Zero(t, second.Win, "a window with no completions must not report the cumulative count")
	require.Empty(t, second.Latencies)
	require.Equal(t, int64(2), second.Count, "cumulative count survives")
}

// fakeBench returns a fake client holding bench-labelled objects of the given
// kinds for one fabric.
func fakeBench(t *testing.T, switches, connections, attachments bool) kclient.Client {
	t.Helper()

	scheme, err := benchScheme()
	require.NoError(t, err)

	meta := func(name, fabric string) kmetav1.ObjectMeta {
		return kmetav1.ObjectMeta{
			Name: name, Namespace: kmetav1.NamespaceDefault,
			Labels: map[string]string{LabelFabric: fabric},
		}
	}

	objs := []kclient.Object{}

	for _, fabric := range []string{"f1", "f2"} {
		if switches {
			objs = append(objs, &wiringapi.Switch{ObjectMeta: meta(fabric+"-leaf-01", fabric)})
		}
		if connections {
			objs = append(objs, &wiringapi.Connection{ObjectMeta: meta(fabric+"-conn", fabric)})
		}
		if attachments {
			objs = append(objs, &vpcapi.VPCAttachment{ObjectMeta: meta(fabric+"-attach", fabric)})
		}
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestDiscoverTargetsInspectOnlyNeedsNoUpdateTargets(t *testing.T) {
	t.Parallel()

	// No attachments or connections exist. An inspect-only run must not
	// demand them.
	kube := fakeBench(t, true, false, false)

	opts := UsersOpts{InspectWorkers: 1}
	opts.setDefaults()

	got, err := discoverTargets(t.Context(), kube, opts)
	require.NoError(t, err)
	require.Len(t, got.switches, 2)
	require.Empty(t, got.byKind)
}

func TestDiscoverTargetsUpdateOnlyNeedsNoSwitches(t *testing.T) {
	t.Parallel()

	kube := fakeBench(t, false, true, true)

	opts := UsersOpts{UpdateWorkers: 1}
	opts.setDefaults()

	got, err := discoverTargets(t.Context(), kube, opts)
	require.NoError(t, err)
	require.Empty(t, got.switches)
	require.Len(t, got.byKind[KindConnection], 2)
	require.Len(t, got.byKind[KindVPCAttachment], 2)
}

func TestDiscoverTargetsStillFailsWhenAUsedHalfIsMissing(t *testing.T) {
	t.Parallel()

	// Gating on worker count must not hide a genuinely missing topology.
	kube := fakeBench(t, true, false, false)

	opts := UsersOpts{UpdateWorkers: 1, InspectWorkers: 1}
	opts.setDefaults()

	_, err := discoverTargets(t.Context(), kube, opts)
	require.ErrorContains(t, err, "run bench init first")
}

func TestInspectSwitchesHonoursFabricFilter(t *testing.T) {
	t.Parallel()

	selected := &targets{switches: []string{"f2-leaf-01", "f2-leaf-02"}}

	// Unscoped and unfiltered: every switch in the cluster, as an operator sees.
	require.Nil(t, (&userSim{targets: selected}).inspectSwitches())

	// Unscoped but --only: every switch in the selected fabrics, not the
	// cluster - otherwise the filter would be ignored.
	require.Equal(t, selected.switches, (&userSim{targets: selected, fabricScoped: true}).inspectSwitches())

	// --inspect-one-switch wins either way.
	one := (&userSim{targets: selected, fabricScoped: true, oneSwitch: true}).inspectSwitches()
	require.Len(t, one, 1)
	require.Contains(t, selected.switches, one[0])
}

func TestPickStaysInRange(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b", "c"}
	for range 50 {
		require.Contains(t, items, pick(items))
	}
}
