// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// bootStats aggregates what the provisioning fleet is doing. Like the rest of
// the bench it measures and does not judge: a truncated installer is counted,
// not investigated.
type bootStats struct {
	switches int
	rounds   int

	// Where the fleet is right now.
	downloading atomic.Int64
	installing  atomic.Int64
	waiting     atomic.Int64

	bytes atomic.Int64

	throttled atomic.Int64
	notFound  atomic.Int64
	truncated atomic.Int64
	errors    atomic.Int64

	installs atomic.Int64
	failed   atomic.Int64

	logPosts  atomic.Int64
	logFailed atomic.Int64

	mu    sync.Mutex
	start time.Time

	// The window since the last report, and the whole run for the summary.
	// Ten rounds of a few hundred switches is thousands of samples, not
	// millions, so keeping every one costs nothing.
	winTTFB     []time.Duration
	winDownload []time.Duration
	ttfb        []time.Duration
	download    []time.Duration
	provision   []time.Duration
	attempts    []int

	// The round under way: when it started and how its switches fared, for
	// the line printed as it ends. roundDone counts the switches through each
	// round and roundTook is how long each finished round took.
	round          int
	roundStart     time.Time
	roundProvision []time.Duration
	roundAttempts  []int
	roundFailed    int
	roundDone      []int
	roundTook      []time.Duration

	// Download speed is what a switch experiences: from sending the request to
	// the last byte, so a slow first byte counts against it. Per download, and
	// for the fleet over only the time some download was in flight, which a
	// wall-clock rate over a reporting window is not: it reads low whenever the
	// downloads take a fraction of the window.
	//
	// Only successful downloads count, here and in the latencies above: an
	// attempt turned away or cut short is a retry, counted as such, and would
	// otherwise pass a 429 off as a fast download or a cut stream as a slow one.
	winSpeed []float64
	speed    []float64
	spans    []span

	perSwitch map[string]time.Duration
	lastErr   error
}

func newBootStats(switches, rounds int) *bootStats {
	return &bootStats{
		switches:  switches,
		rounds:    rounds,
		roundDone: make([]int, rounds),
		roundTook: make([]time.Duration, rounds),
		perSwitch: map[string]time.Duration{},
	}
}

func (s *bootStats) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.start = time.Now()
}

func (s *bootStats) recordDownload(res downloadResult) {
	switch res.outcome {
	case bootOK:
		s.mu.Lock()
		defer s.mu.Unlock()

		s.winTTFB = append(s.winTTFB, res.ttfb)
		s.winDownload = append(s.winDownload, res.took)
		s.ttfb = append(s.ttfb, res.ttfb)
		s.download = append(s.download, res.took)

		if res.took > 0 {
			speed := float64(res.bytes) / res.took.Seconds()
			s.winSpeed = append(s.winSpeed, speed)
			s.speed = append(s.speed, speed)
		}

		s.spans = append(s.spans, span{start: res.start, end: res.start.Add(res.took), bytes: res.bytes})

		return
	case bootThrottled:
		// The expected answer for most of the fleet at any moment, so it is
		// counted but never reported as the last error.
		s.throttled.Add(1)

		return
	case bootNotFound:
		s.notFound.Add(1)
	case bootTruncated:
		s.truncated.Add(1)
	case bootError:
		s.errors.Add(1)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastErr = res.err
}

// span is one download, from its request to its last byte.
type span struct {
	start time.Time
	end   time.Time
	bytes int64
}

// busyTime is how long at least one of the spans was in flight: their union,
// so overlapping downloads count once and the gaps between them not at all.
func busyTime(spans []span) time.Duration {
	if len(spans) == 0 {
		return 0
	}

	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b span) int { return a.start.Compare(b.start) })

	busy := time.Duration(0)
	cur := sorted[0]

	for _, next := range sorted[1:] {
		if next.start.After(cur.end) {
			busy += cur.end.Sub(cur.start)
			cur = next

			continue
		}

		if next.end.After(cur.end) {
			cur.end = next.end
		}
	}

	return busy + cur.end.Sub(cur.start)
}

// rateWindow is how often the fleet's download rate is taken, both for the
// periodic line and for the peak in the summary.
const rateWindow = 30 * time.Second

// activeRate is the downloads' rate over [from, to) while any of them was in
// flight. Each download counts only for the part of it inside the window, with
// its bytes in proportion, so one that started in an earlier window does not
// bring that window's time along.
func activeRate(spans []span, from, to time.Time) float64 {
	clipped := make([]span, 0, len(spans))
	bytes := 0.0

	for _, sp := range spans {
		start, end := sp.start, sp.end
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}

		bytes += float64(sp.bytes) * float64(end.Sub(start)) / float64(sp.end.Sub(sp.start))
		clipped = append(clipped, span{start: start, end: end})
	}

	busy := busyTime(clipped)
	if busy <= 0 {
		return 0
	}

	return bytes / busy.Seconds()
}

// peakActiveRate is the busiest rateWindow of the run, taken over every
// download once all of them are known, so none is split off at a window edge
// by not having finished yet.
func peakActiveRate(spans []span, from, to time.Time) float64 {
	peak := 0.0
	for at := from; at.Before(to); at = at.Add(rateWindow) {
		peak = max(peak, activeRate(spans, at, at.Add(rateWindow)))
	}

	return peak
}

// recordLog files one log post. One that failed only because the run was
// interrupted is not a failure.
func (s *bootStats) recordLog(ctx context.Context, err error) {
	switch {
	case err == nil:
		s.logPosts.Add(1)
	case ctx.Err() != nil:
	default:
		s.logFailed.Add(1)

		s.mu.Lock()
		defer s.mu.Unlock()

		s.lastErr = err
	}
}

// finishRound records a switch through one round, whether it installed or gave
// up, with what it took from its first attempt to the install being done: how
// long an operator would wait for that switch.
func (s *bootStats) finishRound(name string, round int, ok bool, attempts int, took time.Duration) {
	if ok {
		s.installs.Add(1)
	} else {
		s.failed.Add(1)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if ok {
		s.provision = append(s.provision, took)
	}

	s.attempts = append(s.attempts, attempts)
	s.perSwitch[name] += took
	s.roundDone[round]++

	if round == s.round {
		if ok {
			s.roundProvision = append(s.roundProvision, took)
		} else {
			s.roundFailed++
		}

		s.roundAttempts = append(s.roundAttempts, attempts)
	}
}

// startRound marks the whole fleet being released into a round.
func (s *bootStats) startRound(round int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.round = round
	s.roundStart = time.Now()
	s.roundProvision, s.roundAttempts, s.roundFailed = nil, nil, 0
}

// endRound records the round's length and prints how it went: the time from
// releasing the fleet to the last switch installed, which is the figure the
// whole run exists to produce, and how long switches waited along the way.
func (s *bootStats) endRound(round int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	took := time.Since(s.roundStart)
	s.roundTook[round] = took

	args := []any{
		"round", fmt.Sprintf("%d/%d", round+1, s.rounds),
		"took", took.Truncate(time.Second),
		"installs", len(s.roundProvision),
		"failed", s.roundFailed,
	}

	if len(s.roundProvision) > 0 {
		provision := slices.Clone(s.roundProvision)
		p50, p95 := percentiles(provision)
		args = append(args, "provisionP50", p50.Truncate(time.Second), "provisionP95", p95.Truncate(time.Second),
			"provisionMax", provision[len(provision)-1].Truncate(time.Second))
	}

	if len(s.roundAttempts) > 0 {
		attempts := slices.Clone(s.roundAttempts)
		slices.Sort(attempts)
		args = append(args, "attemptsP50", attempts[nearestRank(len(attempts), 50)],
			"attemptsMax", attempts[len(attempts)-1])
	}

	slog.Info("Round done", args...)
}

// roundsComplete is how many rounds the whole fleet is through.
func (s *bootStats) roundsComplete() int {
	done := 0
	for _, took := range s.roundTook {
		if took > 0 {
			done++
		}
	}

	return done
}

// report prints an aggregate line every 30s, which is how a long run is
// watched without per-switch noise.
func (s *bootStats) report(ctx context.Context) {
	ticker := time.NewTicker(rateWindow)
	defer ticker.Stop()

	last := s.bytes.Load()
	lastAt := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now, nowAt := s.bytes.Load(), time.Now()
			rate := float64(now-last) / nowAt.Sub(lastAt).Seconds()

			s.mu.Lock()
			// Downloads still in flight are not in it yet, which reads a little
			// low; the summary's peak is taken once every download is known.
			windowRate := activeRate(s.spans, lastAt, nowAt)
			ttfbP50, ttfbP95 := percentiles(s.winTTFB)
			dlP50, dlP95 := percentiles(s.winDownload)
			speedP50, speedP5 := speedPercentiles(s.winSpeed)
			s.winTTFB, s.winDownload, s.winSpeed = nil, nil, nil
			round, roundDone := s.round, s.roundDone[s.round]
			lastErr := s.lastErr
			s.lastErr = nil
			s.mu.Unlock()

			last, lastAt = now, nowAt

			args := []any{
				"downloading", s.downloading.Load(),
				"installing", s.installing.Load(),
				"retrying", s.waiting.Load(),
				"round", fmt.Sprintf("%d/%d", round+1, s.rounds),
				"roundDone", fmt.Sprintf("%d/%d", roundDone, s.switches),
				"installs", fmt.Sprintf("%d/%d", s.installs.Load(), s.switches*s.rounds),
				"rate", humanBytes(int(rate)) + "/s",
				"activeRate", humanBytes(int(windowRate)) + "/s",
				"throttled", s.throttled.Load(),
			}

			if dlP50 > 0 {
				args = append(args,
					"ttfbP50", ttfbP50.Truncate(time.Millisecond), "ttfbP95", ttfbP95.Truncate(time.Millisecond),
					"downloadP50", dlP50.Truncate(time.Millisecond), "downloadP95", dlP95.Truncate(time.Millisecond),
					"speedP50", humanBytes(int(speedP50))+"/s", "speedP5", humanBytes(int(speedP5))+"/s")
			}

			args = append(args, "notFound", s.notFound.Load(), "truncated", s.truncated.Load(), "errors", s.errors.Load(),
				"failed", s.failed.Load(), "logPosts", s.logPosts.Load())

			if failed := s.logFailed.Load(); failed > 0 {
				args = append(args, "logFailed", failed)
			}
			if lastErr != nil {
				args = append(args, "lastErr", lastErr.Error())
			}

			slog.Info("Boots", args...)
		}
	}
}

// summary describes the whole run: how long the fleet took to reinstall, when
// each round was through, and what it cost along the way.
func (s *bootStats) summary(interrupted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	end := time.Now()
	took := end.Sub(s.start)
	total := s.bytes.Load()

	msg := "Boots finished"
	if interrupted {
		msg = "Boots interrupted"
	}

	slog.Info(msg,
		"switches", s.switches,
		"rounds", fmt.Sprintf("%d/%d", s.roundsComplete(), s.rounds),
		"installs", s.installs.Load(),
		"failed", s.failed.Load(),
		"took", took.Truncate(time.Second),
		"served", humanBytes(int(total)),
		"avgRate", humanBytes(int(float64(total)/took.Seconds()))+"/s")

	// Detail after the headline, where it can be read without scrolling past
	// the run.
	rounds := make([]string, 0, len(s.roundTook))
	for idx, took := range s.roundTook {
		if took > 0 {
			rounds = append(rounds, fmt.Sprintf("%d=%s", idx+1, took.Truncate(time.Second)))
		}
	}
	if len(rounds) > 0 {
		slog.Info("  rounds took", "rounds", strings.Join(rounds, " "))
	}

	// What switches actually got: across the fleet while any download was in
	// flight, and per download from request to last byte. p5 rather than p95
	// for the latter, since the slow downloads are the ones that matter.
	slog.Info("  downloading", "busy", busyTime(s.spans).Truncate(time.Millisecond),
		"activeRate", humanBytes(int(activeRate(s.spans, s.start, end)))+"/s",
		"peakActiveRate", humanBytes(int(peakActiveRate(s.spans, s.start, end)))+"/s")

	if len(s.speed) > 0 {
		speed := slices.Clone(s.speed)
		slices.Sort(speed)

		slog.Info("  download speed", "count", len(speed),
			"p50", humanBytes(int(speed[nearestRank(len(speed), 50)]))+"/s",
			"p5", humanBytes(int(speed[nearestRank(len(speed), 5)]))+"/s",
			"min", humanBytes(int(speed[0]))+"/s",
			"max", humanBytes(int(speed[len(speed)-1]))+"/s")
	}

	logDurations("  download", s.download, time.Millisecond)
	logDurations("  ttfb", s.ttfb, time.Millisecond)
	logDurations("  provision", s.provision, time.Second)

	if len(s.attempts) > 0 {
		attempts := slices.Clone(s.attempts)
		slices.Sort(attempts)

		slog.Info("  attempts per install",
			"p50", attempts[nearestRank(len(attempts), 50)],
			"p95", attempts[nearestRank(len(attempts), 95)],
			"max", attempts[len(attempts)-1])
	}

	slog.Info("  retries",
		"throttled", s.throttled.Load(), "notFound", s.notFound.Load(),
		"truncated", s.truncated.Load(), "errors", s.errors.Load())
	slog.Info("  logs", "posted", s.logPosts.Load(), "failed", s.logFailed.Load())

	if slowest := slowestSwitches(s.perSwitch, 5); len(slowest) > 0 {
		slog.Info("  slowest switches", "total", strings.Join(slowest, " "))
	}
}

// speedPercentiles is p50 and p5 of per-download speeds: the slow tail is the
// low end here, not the high one.
func speedPercentiles(speeds []float64) (float64, float64) {
	if len(speeds) == 0 {
		return 0, 0
	}

	slices.Sort(speeds)

	return speeds[nearestRank(len(speeds), 50)], speeds[nearestRank(len(speeds), 5)]
}

// logDurations prints p50, p95 and max of a whole run's samples.
func logDurations(msg string, samples []time.Duration, unit time.Duration) {
	if len(samples) == 0 {
		return
	}

	sorted := slices.Clone(samples)
	p50, p95 := percentiles(sorted)

	slog.Info(msg, "count", len(sorted),
		"p50", p50.Truncate(unit), "p95", p95.Truncate(unit), "max", sorted[len(sorted)-1].Truncate(unit))
}

// slowestSwitches names the n switches that spent longest getting through all
// their rounds, which is where a throttle that plays favourites shows.
func slowestSwitches(perSwitch map[string]time.Duration, n int) []string {
	names := make([]string, 0, len(perSwitch))
	for name := range perSwitch {
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		if perSwitch[names[i]] != perSwitch[names[j]] {
			return perSwitch[names[i]] > perSwitch[names[j]]
		}

		return names[i] < names[j]
	})

	out := make([]string, 0, n)
	for _, name := range names[:min(n, len(names))] {
		out = append(out, fmt.Sprintf("%s=%s", name, perSwitch[name].Truncate(time.Second)))
	}

	return out
}
