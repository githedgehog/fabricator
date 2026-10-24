// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// installer builds what fabric-boot streams: a stub, the payload, the
// payload's size and the magic.
func installer(payload int) []byte {
	out := []byte("#!/bin/sh\nexit 0\n")
	out = append(out, bytes.Repeat([]byte{0xab}, payload)...)
	out = binary.BigEndian.AppendUint64(out, uint64(payload)) //nolint:gosec // test sizes
	out = append(out, bootMagic...)

	return out
}

// fakeBoot serves /onie and /log the way fabric-boot does: a throttle that
// turns away whatever exceeds it, a 404 for a switch it does not know, and an
// installer that can be cut short to stand in for a stream that timed out.
type fakeBoot struct {
	known map[string]string // MAC to serial
	slots chan struct{}

	throttled atomic.Int64
	notFound  atomic.Int64

	mu      sync.Mutex
	cut     map[string]int // MAC to how many of its downloads to cut short
	served  map[string]int // MAC to complete installers
	logs    map[string]int // MAC to log lines
	badLogs int

	// spread is the most installs any switch was ever ahead of another,
	// which rounds kept in step hold to one.
	spread int
}

func newFakeBoot(slots int, known map[string]string) *fakeBoot {
	return &fakeBoot{
		known:  known,
		slots:  make(chan struct{}, slots),
		cut:    map[string]int{},
		served: map[string]int{},
		logs:   map[string]int{},
	}
}

func (f *fakeBoot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case bootONIEPath:
		f.serveONIE(w, r)
	case bootLogPath:
		f.serveLog(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeBoot) serveONIE(w http.ResponseWriter, r *http.Request) {
	select {
	case f.slots <- struct{}{}:
		defer func() { <-f.slots }()
	default:
		f.throttled.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)

		return
	}

	mac, serial := r.Header.Get("Onie-Eth-Addr"), r.Header.Get("Onie-Serial-Number")
	if r.Header.Get("Onie-Operation") != "os-install" || r.Header.Get("Onie-Arch") != "x86_64" || mac == "" || serial == "" {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	if f.known[mac] != serial {
		f.notFound.Add(1)
		w.WriteHeader(http.StatusNotFound)

		return
	}

	// Hold the slot long enough for the rest of the fleet to find it taken.
	time.Sleep(5 * time.Millisecond)

	body := installer(64 << 10)

	f.mu.Lock()
	cut := f.cut[mac] > 0
	if cut {
		f.cut[mac]--
		body = body[:len(body)/2]
	} else {
		f.served[mac]++

		least := f.served[mac]
		for known := range f.known {
			least = min(least, f.served[known])
		}
		f.spread = max(f.spread, f.served[mac]-least)
	}
	f.mu.Unlock()

	_, _ = w.Write(body)
}

func (f *fakeBoot) serveLog(w http.ResponseWriter, r *http.Request) {
	entry := struct {
		Level   string            `json:"level"`
		Message string            `json:"message"`
		Extra   map[string]string `json:"extra"`
	}{}

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil || entry.Message == "" || f.known[entry.Extra["ethaddr"]] != entry.Extra["serial"] {
		f.badLogs++
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	f.logs[entry.Extra["ethaddr"]]++
}

// bootSwitches is a fake cluster with n bench switches, each with its boot
// identifiers, and a switch the bench did not create.
func bootSwitches(t *testing.T, n int) (kclient.Client, map[string]string) {
	t.Helper()

	scheme, err := benchScheme()
	require.NoError(t, err)

	known := map[string]string{}
	objs := []kclient.Object{&wiringapi.Switch{Name: "not-bench", Namespace: kmetav1.NamespaceDefault}} //nolint:prealloc

	for idx := range n {
		boot := wiringapi.SwitchBoot{Serial: fmt.Sprintf(BootSerialFmt, idx), MAC: fmt.Sprintf("02:b0:00:00:00:%02x", idx)}
		known[boot.MAC] = boot.Serial

		objs = append(objs, &wiringapi.Switch{
			Name:      fmt.Sprintf("f1-leaf-%02d", idx+1),
			Namespace: kmetav1.NamespaceDefault,
			Labels:    map[string]string{LabelFabric: "f1"},
			Spec:      wiringapi.SwitchSpec{Boot: boot},
		})
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(), known
}

func bootOpts(url string) BootsOpts {
	return BootsOpts{
		URL:         url,
		Rounds:      3,
		LogLines:    9,
		InstallTime: 20 * time.Millisecond,
		RetryDelay:  10 * time.Millisecond,
		MaxFailures: 3,
	}
}

// Every switch installs exactly the requested number of times and posts every
// log line of every install, even though the throttle admits one at a time and
// most attempts are turned away.
func TestBootsEverySwitchInstallsEveryRound(t *testing.T) {
	t.Parallel()

	admin, known := bootSwitches(t, 6)

	boot := newFakeBoot(1, known)
	srv := httptest.NewServer(boot)
	defer srv.Close()

	opts := bootOpts(srv.URL)
	require.NoError(t, RunBoots(t.Context(), admin, opts))

	require.Positive(t, boot.throttled.Load(), "six switches against one slot must be turned away")
	require.Zero(t, boot.notFound.Load())
	require.Zero(t, boot.badLogs)
	require.Equal(t, 1, boot.spread, "no switch starts a round before the whole fleet is through the last one")

	for mac := range known {
		require.Equal(t, opts.Rounds, boot.served[mac], "installs of %s", mac)
		require.Equal(t, opts.Rounds*opts.LogLines, boot.logs[mac], "log lines of %s", mac)
	}
}

// Being turned away by the throttle does not count towards giving up: with a
// single allowed failure, a switch still installs every round.
func TestBootsThrottlingIsNotFailure(t *testing.T) {
	t.Parallel()

	admin, known := bootSwitches(t, 8)

	boot := newFakeBoot(1, known)
	srv := httptest.NewServer(boot)
	defer srv.Close()

	opts := bootOpts(srv.URL)
	opts.MaxFailures = 1
	require.NoError(t, RunBoots(t.Context(), admin, opts))

	require.Positive(t, boot.throttled.Load())

	for mac := range known {
		require.Equal(t, opts.Rounds, boot.served[mac], "installs of %s", mac)
	}
}

// An installer cut short is caught by its missing trailer and downloaded again,
// so the switch still gets its full count of complete installs.
func TestBootsRetriesTruncatedInstaller(t *testing.T) {
	t.Parallel()

	admin, known := bootSwitches(t, 2)

	boot := newFakeBoot(2, known)
	boot.cut["02:b0:00:00:00:00"] = 2
	srv := httptest.NewServer(boot)
	defer srv.Close()

	opts := bootOpts(srv.URL)
	require.NoError(t, RunBoots(t.Context(), admin, opts))

	require.Zero(t, boot.cut["02:b0:00:00:00:00"], "both cut downloads were made")
	for mac := range known {
		require.Equal(t, opts.Rounds, boot.served[mac], "installs of %s", mac)
	}
}

// A switch fabric-boot does not know is tried MaxFailures times per round and
// then given up on, without holding up the run.
func TestBootsGivesUpOnUnknownSwitch(t *testing.T) {
	t.Parallel()

	admin, known := bootSwitches(t, 2)

	// fabric-boot knows only the first switch.
	boot := newFakeBoot(2, map[string]string{"02:b0:00:00:00:00": known["02:b0:00:00:00:00"]})
	srv := httptest.NewServer(boot)
	defer srv.Close()

	opts := bootOpts(srv.URL)
	require.NoError(t, RunBoots(t.Context(), admin, opts))

	require.Equal(t, int64(opts.Rounds*opts.MaxFailures), boot.notFound.Load())
	require.Equal(t, opts.Rounds, boot.served["02:b0:00:00:00:00"])
	require.Zero(t, boot.logs["02:b0:00:00:00:01"], "a switch that never installed has nothing to log")
}

// Switches generated before the bench set boot identifiers would only ever get
// 404s, so the run refuses to start rather than spend its time on them.
func TestBootsRefusesSwitchesWithoutBootIdentifiers(t *testing.T) {
	t.Parallel()

	scheme, err := benchScheme()
	require.NoError(t, err)

	admin := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&wiringapi.Switch{
		Name: "f1-leaf-01", Namespace: kmetav1.NamespaceDefault, Labels: map[string]string{LabelFabric: "f1"},
	}).Build()

	err = RunBoots(t.Context(), admin, bootOpts("http://127.0.0.1:1"))
	require.ErrorContains(t, err, "re-run bench init")
}

// --only narrows the run to matching switches.
func TestBootsOnlyMatchingSwitches(t *testing.T) {
	t.Parallel()

	admin, _ := bootSwitches(t, 12)

	targets, err := discoverBootTargets(t.Context(), admin, []string{"f1-leaf-1"})
	require.NoError(t, err)

	names := []string{} //nolint:prealloc
	for _, target := range targets {
		names = append(names, target.name)
	}
	require.Equal(t, []string{"f1-leaf-10", "f1-leaf-11", "f1-leaf-12"}, names)
}

// The trailer check holds however the stream is split into writes, including
// with the trailer itself straddling two of them.
func TestTailWriterAcceptsCompleteInstaller(t *testing.T) {
	t.Parallel()

	body := installer(1000)

	for _, chunk := range []int{1, 3, 7, bootTrailerLen - 1, bootTrailerLen, 100, len(body)} {
		var counter atomic.Int64

		w := &tailWriter{counter: &counter}
		for start := 0; start < len(body); start += chunk {
			_, err := w.Write(body[start:min(start+chunk, len(body))])
			require.NoError(t, err)
		}

		require.NoError(t, w.check(), "chunk %d", chunk)
		require.Equal(t, int64(len(body)), w.n)
		require.Equal(t, int64(len(body)), counter.Load())
		require.LessOrEqual(t, len(w.tail), bootTrailerLen)
	}
}

func TestTailWriterRejectsBrokenInstaller(t *testing.T) {
	t.Parallel()

	body := installer(1000)

	oversized := slices.Clone(body)
	binary.BigEndian.PutUint64(oversized[len(oversized)-bootTrailerLen:], uint64(len(body)))

	for name, broken := range map[string][]byte{
		"cut":       body[:len(body)/2],
		"short":     body[len(body)-bootTrailerLen+1:],
		"empty":     nil,
		"no magic":  append(slices.Clone(body[:len(body)-1]), 'x'),
		"oversized": oversized,
	} {
		w := &tailWriter{}
		_, err := w.Write(broken)
		require.NoError(t, err)
		require.Error(t, w.check(), name)
	}
}

// The log keeps a real install's shape: the head at once, the tail at the end,
// the unzip burst in one instant in between, and exactly the lines asked for.
func TestInstallLogShape(t *testing.T) {
	t.Parallel()

	total := 30 * time.Second
	lines := installLog(DefaultBootLogLines, total)
	require.Len(t, lines, DefaultBootLogLines)

	for idx := range len(installLogHead) {
		require.Zero(t, lines[idx].at)
		require.Equal(t, installLogHead[idx], lines[idx].msg)
	}
	for idx := range len(installLogTail) {
		line := lines[len(lines)-len(installLogTail)+idx]
		require.Equal(t, total, line.at)
		require.Equal(t, installLogTail[idx], line.msg)
	}

	at := map[time.Duration]int{} //nolint:exhaustive // filled below
	for idx, line := range lines {
		require.NotEmpty(t, line.msg)
		require.LessOrEqual(t, line.at, total)
		if idx > 0 {
			require.GreaterOrEqual(t, line.at, lines[idx-1].at, "lines are in order")
		}
		at[line.at]++
	}
	require.Equal(t, 30, at[time.Duration(float64(total)*installLogBurstAt)], "the unzip burst lands at once")

	require.Empty(t, installLog(0, total))
	require.Len(t, installLog(3, total), 3)
	require.Len(t, installLog(500, total), 500)
}

// The fleet's download time is the union of its downloads, overlapping or
// not, and none of the idle time between them: that is what the active rate
// divides by.
func TestBusyTimeIsUnionOfDownloads(t *testing.T) {
	t.Parallel()

	at := func(sec int) time.Time { return time.Date(2026, 10, 18, 12, 0, sec, 0, time.UTC) }

	require.Zero(t, busyTime(nil))
	require.Equal(t, 5*time.Second, busyTime([]span{
		{start: at(10), end: at(11)}, // alone, after a gap
		{start: at(0), end: at(3)},
		{start: at(2), end: at(4)}, // overlaps the first
		{start: at(1), end: at(2)}, // inside it
	}))
}

// A window's rate takes only the part of each download inside it, with its
// bytes in proportion, so a download that straddles two windows does not drag
// one window's time into the other: the peak can never read below the average.
func TestActiveRateClipsDownloadsToWindow(t *testing.T) {
	t.Parallel()

	at := func(sec int) time.Time { return time.Date(2026, 10, 18, 12, 0, sec, 0, time.UTC) }

	spans := []span{
		{start: at(0), end: at(10), bytes: 10},   // 1 B/s, inside the first window
		{start: at(25), end: at(35), bytes: 100}, // 10 B/s, across the edge
	}

	require.InDelta(t, 110.0/20, activeRate(spans, at(0), at(35)), 1e-9, "whole run: 110 B over 20s busy")
	require.InDelta(t, 60.0/15, activeRate(spans, at(0), at(30)), 1e-9, "first window: 10 B + half of 100 B over 15s busy")
	require.InDelta(t, 10.0, activeRate(spans, at(30), at(60)), 1e-9, "second window: the other half over 5s")
	require.Zero(t, activeRate(spans, at(40), at(50)))

	require.InDelta(t, 10.0, peakActiveRate(spans, at(0), at(35)), 1e-9)
	require.GreaterOrEqual(t, peakActiveRate(spans, at(0), at(35)), activeRate(spans, at(0), at(35)))
}

// Download speed runs from the request to the last byte, so a slow first byte
// counts against it, and only successful downloads count at all: a 429 or a
// cut stream is a retry, not a download.
func TestBootStatsSpeedIsRequestToLastByte(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	s := newBootStats(1, 1)
	s.recordDownload(downloadResult{outcome: bootThrottled, start: start, ttfb: time.Millisecond, took: time.Millisecond})
	s.recordDownload(downloadResult{outcome: bootTruncated, start: start, ttfb: time.Second, took: 5 * time.Second, bytes: 1 << 30, err: errNoTrailer})
	s.recordDownload(downloadResult{outcome: bootOK, start: start, ttfb: 2 * time.Second, took: 4 * time.Second, bytes: 2 << 30})

	require.Equal(t, []float64{512 << 20}, s.speed)
	require.Equal(t, []time.Duration{2 * time.Second}, s.ttfb)
	require.Equal(t, []span{{start: start, end: start.Add(4 * time.Second), bytes: 2 << 30}}, s.spans)
	require.Equal(t, int64(1), s.throttled.Load())
	require.Equal(t, int64(1), s.truncated.Load())
}
