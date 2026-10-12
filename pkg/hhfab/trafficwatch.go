// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.githedgehog.com/fabricator/pkg/util/sshutil"
)

// trafficPingInterval is the ping interval of the background probes. It is also the spacing of
// the "no answer yet" lines ping writes for unanswered probes.
const trafficPingInterval = 200 * time.Millisecond

// trafficClockSamples is the number of round trips used to measure a server clock offset, the
// one with the shortest round trip wins.
const trafficClockSamples = 5

var errTrafficProbe = errors.New("traffic probe")

// pingSample is the outcome of one ICMP echo of a background ping.
type pingSample struct {
	Seq     int
	Replied bool
	// At is the stamp ping wrote (-D) on the reply line, or on the "no answer yet" line (-O) of a
	// lost probe. ping writes the latter when it sends the next probe, one interval after the lost
	// one. Zero when ping wrote nothing for the seq.
	At time.Time
}

var pingStampRegexp = regexp.MustCompile(`^\[(\d+)\.(\d+)\]`)

// parsePingStamp returns the -D stamp that starts a ping output line.
func parsePingStamp(line string) (time.Time, bool) {
	m := pingStampRegexp.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	frac := m[2]
	if len(frac) > 9 {
		frac = frac[:9]
	}
	frac += strings.Repeat("0", 9-len(frac))
	nsec, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return time.Time{}, false
	}

	return time.Unix(sec, nsec), true
}

// parsePingTimeline returns one sample per seq from 1 to the highest seq ping wrote a line for,
// from the output of `ping -D -O`. A seq with an echo reply is replied, even when a "no answer
// yet" line was written for it before. A seq without a reply is lost and takes the stamp of its
// own "no answer yet" line. A seq with neither line is lost too, with the previous stamp plus one
// interval. The seq still in flight when the output was taken has no line yet and is not returned.
// Unlike parsePingLostSeqs, it does not need the sent count, so it works on running pings.
func parsePingTimeline(stdout string) []pingSample {
	replies := map[int]time.Time{}
	noAnswer := map[int]time.Time{}
	maxSeq := 0
	for line := range strings.SplitSeq(stdout, "\n") {
		stamp, _ := parsePingStamp(line)
		if seq, ok := parsePingReplySeq(line); ok {
			replies[seq] = stamp
			maxSeq = max(maxSeq, seq)

			continue
		}
		if !strings.Contains(line, "no answer yet for") {
			continue
		}
		if seq, ok := parsePingSeqAfter(line, "icmp_seq="); ok {
			noAnswer[seq] = stamp
			maxSeq = max(maxSeq, seq)
		}
	}

	samples := make([]pingSample, 0, maxSeq)
	var last time.Time
	for seq := 1; seq <= maxSeq; seq++ {
		if at, ok := replies[seq]; ok {
			samples = append(samples, pingSample{Seq: seq, Replied: true, At: at})
			if !at.IsZero() {
				last = at
			}

			continue
		}
		at := noAnswer[seq]
		if at.IsZero() && !last.IsZero() {
			at = last.Add(trafficPingInterval)
		}
		if !at.IsZero() {
			last = at
		}
		samples = append(samples, pingSample{Seq: seq, At: at})
	}

	return samples
}

// TrafficProbe is one ping flow kept running in the background.
type TrafficProbe struct {
	// Name identifies the flow in reports, e.g. "server-03 -> server-01".
	Name    string
	From    string
	FromSSH *sshutil.Config
	To      netip.Addr
}

type trafficProbeState struct {
	TrafficProbe
	file string
	pid  string
}

// ProbeTimeline is what one probe has seen so far, with the times already on the runner's clock.
type ProbeTimeline struct {
	Probe   string
	Samples []pingSample
	Replies int
}

// LostPing is one unanswered echo, placed on the runner's clock.
type LostPing struct {
	Probe string
	Seq   int
	At    time.Time
}

// TrafficWatch keeps `ping -i 0.2` running on a set of servers and reports the unanswered echoes
// with wall-clock times, so that loss can be attributed to the window of a change. Server clocks
// are not synced, so it measures each server's offset to the runner clock and corrects the
// stamps. It knows nothing about what the traffic is for, so it serves any test that needs
// traffic during a change.
type TrafficWatch struct {
	probes  []*trafficProbeState
	offsets map[string]time.Duration
}

func NewTrafficWatch(probes []TrafficProbe) *TrafficWatch {
	w := &TrafficWatch{offsets: map[string]time.Duration{}}
	for i, p := range probes {
		w.probes = append(w.probes, &trafficProbeState{
			TrafficProbe: p,
			file:         fmt.Sprintf("/tmp/hhfab-traffic-%d.log", i),
		})
	}

	return w
}

// measureClockOffset returns remote clock minus runner clock for the server, using the sample
// with the shortest round trip.
func measureClockOffset(ctx context.Context, ssh *sshutil.Config) (time.Duration, error) {
	var best time.Duration
	bestRTT := time.Duration(-1)
	for range trafficClockSamples {
		before := time.Now()
		stdout, stderr, err := ssh.Run(ctx, "date +%s.%N")
		after := time.Now()
		if err != nil {
			return 0, fmt.Errorf("reading remote clock: %w: %s", err, stderr)
		}
		stamp, ok := parsePingStamp("[" + strings.TrimSpace(stdout) + "]")
		if !ok {
			return 0, fmt.Errorf("%w: unexpected date output %q", errTrafficProbe, stdout)
		}
		rtt := after.Sub(before)
		if bestRTT < 0 || rtt < bestRTT {
			bestRTT = rtt
			best = stamp.Sub(before.Add(rtt / 2))
		}
	}

	return best, nil
}

func (w *TrafficWatch) measureOffsets(ctx context.Context) (map[string]time.Duration, error) {
	offsets := map[string]time.Duration{}
	for _, p := range w.probes {
		if _, ok := offsets[p.From]; ok {
			continue
		}
		off, err := measureClockOffset(ctx, p.FromSSH)
		if err != nil {
			return nil, fmt.Errorf("measuring clock of %s: %w", p.From, err)
		}
		offsets[p.From] = off
	}

	return offsets, nil
}

// Start measures the server clocks and starts every probe.
func (w *TrafficWatch) Start(ctx context.Context) error {
	offsets, err := w.measureOffsets(ctx)
	if err != nil {
		return err
	}
	w.offsets = offsets

	for _, p := range w.probes {
		// Detached, so the ssh session returns; the pid file is the handle to stop it, as
		// pgrep over ssh matches its own command line.
		cmd := fmt.Sprintf("nohup ping -i %.1f -D -O %s >%s 2>&1 </dev/null & echo $!",
			trafficPingInterval.Seconds(), p.To, p.file)
		stdout, stderr, err := p.FromSSH.Run(ctx, cmd)
		if err != nil {
			_ = w.Stop(context.WithoutCancel(ctx))

			return fmt.Errorf("starting probe %s: %w: %s", p.Name, err, stderr)
		}
		p.pid = strings.TrimSpace(stdout)
		if _, err := strconv.Atoi(p.pid); err != nil {
			p.pid = ""
			_ = w.Stop(context.WithoutCancel(ctx))

			return fmt.Errorf("%w: %s: unexpected pid %q", errTrafficProbe, p.Name, stdout)
		}
	}

	return nil
}

// ClockDrift re-measures the server clocks and returns the largest change of an offset since
// Start. A large drift makes the placement of lost pings in a window unreliable.
func (w *TrafficWatch) ClockDrift(ctx context.Context) (time.Duration, error) {
	now, err := w.measureOffsets(ctx)
	if err != nil {
		return 0, err
	}
	var worst time.Duration
	for server, off := range now {
		d := off - w.offsets[server]
		if d < 0 {
			d = -d
		}
		worst = max(worst, d)
	}

	return worst, nil
}

// Snapshot reads what every probe has recorded so far.
func (w *TrafficWatch) Snapshot(ctx context.Context) ([]ProbeTimeline, error) {
	out := make([]ProbeTimeline, 0, len(w.probes))
	for _, p := range w.probes {
		stdout, stderr, err := p.FromSSH.Run(ctx, "cat "+p.file)
		if err != nil {
			return nil, fmt.Errorf("reading probe %s: %w: %s", p.Name, err, stderr)
		}
		off := w.offsets[p.From]
		tl := ProbeTimeline{Probe: p.Name, Samples: parsePingTimeline(stdout)}
		for i := range tl.Samples {
			if !tl.Samples[i].At.IsZero() {
				tl.Samples[i].At = tl.Samples[i].At.Add(-off)
			}
			if tl.Samples[i].Replied {
				tl.Replies++
			}
		}
		out = append(out, tl)
	}

	return out, nil
}

// Stop kills the probes and removes their files. It keeps going on errors and returns them all.
func (w *TrafficWatch) Stop(ctx context.Context) error {
	var errs []error
	for _, p := range w.probes {
		cmd := "rm -f " + p.file
		if p.pid != "" {
			cmd = fmt.Sprintf("kill %s 2>/dev/null; %s", p.pid, cmd)
		}
		if _, stderr, err := p.FromSSH.Run(ctx, cmd); err != nil {
			errs = append(errs, fmt.Errorf("stopping probe %s: %w: %s", p.Name, err, stderr))
		}
		p.pid = ""
	}

	return errors.Join(errs...)
}

// BrokenProbes returns the probes that have not received a single reply. They never worked, so
// whatever they report as lost is not a measurement.
func BrokenProbes(timelines []ProbeTimeline) []string {
	var broken []string
	for _, tl := range timelines {
		if tl.Replies == 0 {
			broken = append(broken, tl.Probe)
		}
	}

	return broken
}

// LostPings returns the lost echoes with a stamp in [from, to], oldest first. A lost echo whose
// stamp could not be placed is reported too, it can't be ruled out of the window.
func LostPings(timelines []ProbeTimeline, from, to time.Time) []LostPing {
	var lost []LostPing
	for _, tl := range timelines {
		for _, s := range tl.Samples {
			if s.Replied {
				continue
			}
			if !s.At.IsZero() && (s.At.Before(from) || s.At.After(to)) {
				continue
			}
			lost = append(lost, LostPing{Probe: tl.Probe, Seq: s.Seq, At: s.At})
		}
	}
	sort.SliceStable(lost, func(i, j int) bool { return lost[i].At.Before(lost[j].At) })

	return lost
}

// FormatLostPings renders lost echoes for a report, one line each.
func FormatLostPings(lost []LostPing) string {
	var b strings.Builder
	for _, l := range lost {
		at := "unplaced"
		if !l.At.IsZero() {
			at = l.At.Format("15:04:05.000")
		}
		fmt.Fprintf(&b, "%s icmp_seq=%d %s\n", l.Probe, l.Seq, at)
	}

	return b.String()
}

// LossWindow is a run of lost echoes of one probe, for a summary like "3.2s, 16 pings".
type LossWindow struct {
	Probe string
	From  time.Time
	To    time.Time
	Pings int
}

// GroupLoss joins lost echoes of one probe that follow each other within gap into windows.
func GroupLoss(lost []LostPing, gap time.Duration) []LossWindow {
	byProbe := map[string][]LostPing{}
	var order []string
	for _, l := range lost {
		if _, ok := byProbe[l.Probe]; !ok {
			order = append(order, l.Probe)
		}
		byProbe[l.Probe] = append(byProbe[l.Probe], l)
	}

	var out []LossWindow
	for _, probe := range order {
		var cur *LossWindow
		for _, l := range byProbe[probe] {
			if cur != nil && !l.At.IsZero() && l.At.Sub(cur.To) <= gap {
				cur.To = l.At
				cur.Pings++

				continue
			}
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &LossWindow{Probe: probe, From: l.At, To: l.At, Pings: 1}
		}
		if cur != nil {
			out = append(out, *cur)
		}
	}

	slog.Debug("Grouped lost pings", "pings", len(lost), "windows", len(out))

	return out
}
