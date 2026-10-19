// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// What fabric-boot serves and how its installer ends. They mirror fabric's
// pkg/boot/nosinstall, which fabricator does not vendor: GET /onie streams the
// installer for the switch ONIE identifies, and the installer posts each of its
// log lines back to /log. The installer is a shell stub followed by a tar of
// the NOS image and the agent, then the tar's size as 8 big-endian bytes and
// the magic.
const (
	bootONIEPath   = "/onie"
	bootLogPath    = "/log"
	bootMagic      = "hedgehog"
	bootTrailerLen = 8 + len(bootMagic)
)

// Defaults for `bench boots`. A standard run reinstalls every switch ten times.
// The log line count is what a SONiC install posted on a VLAB, and the install
// time roughly how long it took there between download and done.
const (
	DefaultBootRounds      = 10
	DefaultBootLogLines    = 70
	DefaultBootInstallTime = 30 * time.Second
	DefaultBootRetryDelay  = 10 * time.Second
	DefaultBootMaxFailures = 10
)

// bootDownloadTimeout bounds one download on the bench side. fabric-boot cuts a
// stream at 300s itself; this only keeps a hung connection from holding a
// switch for the rest of the run.
const bootDownloadTimeout = 10 * time.Minute

// bootLogTimeout is slog-webhook's, which is what the installer posts with.
const bootLogTimeout = 10 * time.Second

// bootMachine is the ONIE machine every simulated switch reports. fabric-boot
// only logs it for an install, so one value does for the whole fleet.
const bootMachine = "bench"

// BootsOpts configures the provisioning simulation.
type BootsOpts struct {
	// URL is fabric-boot's base, e.g. http://172.30.0.1:32000.
	URL string
	// SourceAddr, when set, is the local address every switch dials from, as
	// for the agents: an address on the management bridge.
	SourceAddr netip.Addr

	// Switches restricts the run to switches whose name starts with one of
	// these prefixes; empty means every bench switch.
	Switches []string

	Rounds      int
	LogLines    int
	InstallTime time.Duration
	RetryDelay  time.Duration
	MaxFailures int
}

// bootTarget is one switch as ONIE would identify it.
type bootTarget struct {
	name   string
	serial string
	mac    string
}

// RunBoots makes every bench switch install itself from fabric-boot opts.Rounds
// times. Each round starts the whole fleet at the same instant, the worst case
// of a datacenter powering up into ONIE together, and ends when every switch
// has installed once: fabric-boot admits a few installs at a time and turns the
// rest away, and they retry, exactly as ONIE does.
//
// Rounds are kept in step on purpose. Left to run free, the switches that win
// the throttle early would go straight back for more while the unlucky ones
// still wait for their first install, so after round one the run would no
// longer be a fleet reinstalling together and the slowest switch would fall
// further behind every round.
func RunBoots(ctx context.Context, admin kclient.Client, opts BootsOpts) error {
	if opts.URL == "" {
		return fmt.Errorf("no fabric-boot URL") //nolint:err113
	}
	if opts.Rounds < 1 {
		return fmt.Errorf("rounds must be at least 1, got %d", opts.Rounds) //nolint:err113
	}
	if opts.LogLines < 0 {
		return fmt.Errorf("log lines must not be negative, got %d", opts.LogLines) //nolint:err113
	}
	if opts.InstallTime < 0 || opts.RetryDelay <= 0 {
		return fmt.Errorf("install time must not be negative and retry delay must be positive") //nolint:err113
	}
	if opts.MaxFailures < 1 {
		return fmt.Errorf("max failures must be at least 1, got %d", opts.MaxFailures) //nolint:err113
	}

	targets, err := discoverBootTargets(ctx, admin, opts.Switches)
	if err != nil {
		return err
	}

	b := newBootRunner(opts, len(targets))

	slog.Info("Preparing boots", "switches", len(targets), "rounds", opts.Rounds, "url", opts.URL,
		"logLines", opts.LogLines, "installTime", opts.InstallTime, "retryDelay", opts.RetryDelay)

	reportCtx, stopReport := context.WithCancel(ctx)
	defer stopReport()

	var reportWG sync.WaitGroup
	reportWG.Go(func() {
		b.stats.report(reportCtx)
	})

	slog.Info("Starting boots", "switches", len(targets))

	b.stats.begin()

	for round := range opts.Rounds {
		if ctx.Err() != nil {
			break
		}

		b.runRound(ctx, targets, round)
	}

	stopReport()
	reportWG.Wait()

	b.stats.summary(ctx.Err() != nil)

	return nil
}

// discoverBootTargets lists the bench switches with their boot identifiers.
// A switch without them was generated before the bench set them, and would
// only ever be told fabric-boot has no such switch.
func discoverBootTargets(ctx context.Context, admin kclient.Client, prefixes []string) ([]bootTarget, error) {
	switches := &wiringapi.SwitchList{}
	if err := admin.List(ctx, switches, kclient.HasLabels{LabelFabric}); err != nil {
		return nil, fmt.Errorf("listing switches: %w", err)
	}

	targets := []bootTarget{}
	missing := []string{}

	for _, sw := range switches.Items {
		if len(prefixes) > 0 && !slices.ContainsFunc(prefixes, func(p string) bool {
			return strings.HasPrefix(sw.Name, p)
		}) {
			continue
		}

		if sw.Spec.Boot.Serial == "" || sw.Spec.Boot.MAC == "" {
			missing = append(missing, sw.Name)

			continue
		}

		targets = append(targets, bootTarget{name: sw.Name, serial: sw.Spec.Boot.Serial, mac: sw.Spec.Boot.MAC})
	}

	if len(missing) > 0 {
		sort.Strings(missing)

		return nil, fmt.Errorf("%d switches have no boot serial and MAC, e.g. %s; re-run bench init to set them", len(missing), missing[0]) //nolint:err113
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no bench switches found to boot") //nolint:err113
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].name < targets[j].name })

	return targets, nil
}

// bootRunner holds what the switches share: the options, how they dial, and
// the stats they report into.
type bootRunner struct {
	opts   BootsOpts
	dialer *net.Dialer
	client *http.Client
	stats  *bootStats
}

func newBootRunner(opts BootsOpts, switches int) *bootRunner {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if opts.SourceAddr.IsValid() {
		dialer.LocalAddr = &net.TCPAddr{IP: opts.SourceAddr.AsSlice()}
	}

	return &bootRunner{
		opts:   opts,
		dialer: dialer,
		// ONIE fetches the installer with wget, a new connection every time, and
		// takes the bytes as they come.
		client: &http.Client{Transport: &http.Transport{
			DialContext:        dialer.DialContext,
			DisableKeepAlives:  true,
			DisableCompression: true,
		}},
		stats: newBootStats(switches, opts.Rounds),
	}
}

// runRound has every switch install once, all of them released together by
// one barrier rather than in the order their goroutines happened to start, and
// returns when the last of them is done.
func (b *bootRunner) runRound(ctx context.Context, targets []bootTarget, round int) {
	start := make(chan struct{})

	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-start:
			}

			b.installOnce(ctx, target, round)
		})
	}

	b.stats.startRound(round)
	close(start)

	wg.Wait()

	if ctx.Err() == nil {
		b.stats.endRound(round)
	}
}

// installOnce is one switch's round: download until fabric-boot admits it,
// then install.
func (b *bootRunner) installOnce(ctx context.Context, t bootTarget, round int) {
	began := time.Now()

	attempts, ok := b.provision(ctx, t)
	if ctx.Err() != nil {
		return
	}

	if ok {
		b.install(ctx, t)
		if ctx.Err() != nil {
			return
		}
	}

	b.stats.finishRound(t.name, round, ok, attempts, time.Since(began))
}

// provision downloads the installer, retrying the way ONIE does: whatever went
// wrong, wait and try again. Being turned away by the throttle is the expected
// outcome for most of the fleet, so only other failures count towards giving
// up on this round.
func (b *bootRunner) provision(ctx context.Context, t bootTarget) (int, bool) {
	attempts, failures := 0, 0

	for {
		attempts++

		res := b.download(ctx, t)

		if ctx.Err() != nil {
			return attempts, false
		}

		b.stats.recordDownload(res)

		if res.outcome == bootOK {
			return attempts, true
		}
		if res.outcome != bootThrottled {
			failures++
			if failures >= b.opts.MaxFailures {
				return attempts, false
			}
		}

		b.stats.waiting.Add(1)
		select {
		case <-ctx.Done():
		case <-time.After(jitter(b.opts.RetryDelay)):
		}
		b.stats.waiting.Add(-1)

		if ctx.Err() != nil {
			return attempts, false
		}
	}
}

// jitter spreads a delay over half to one and a half times itself, so switches
// turned away together do not all come back together.
func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d))) //nolint:gosec // jitter, not a secret
}

// bootOutcome is how one download attempt ended.
type bootOutcome int

const (
	bootOK bootOutcome = iota
	bootThrottled
	bootNotFound
	bootTruncated
	bootError
)

// downloadResult is one download attempt, all of it timed from the request.
// ttfb runs to the response headers, which is fabric-boot finding the switch
// and reading its Agent and Secret; took runs to the last byte.
type downloadResult struct {
	outcome bootOutcome
	start   time.Time
	ttfb    time.Duration
	took    time.Duration
	bytes   int64
	err     error
}

// download is one ONIE os-install request, reading the whole installer and
// checking that it arrived in one piece. The bytes go nowhere: what is being
// measured is serving them.
func (b *bootRunner) download(ctx context.Context, t bootTarget) downloadResult {
	ctx, cancel := context.WithTimeout(ctx, bootDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.opts.URL+bootONIEPath, nil)
	if err != nil {
		return downloadResult{outcome: bootError, err: fmt.Errorf("building request: %w", err)}
	}

	setONIEHeaders(req, t)

	start := time.Now()

	resp, err := b.client.Do(req)
	if err != nil {
		return downloadResult{outcome: bootError, err: fmt.Errorf("requesting installer: %w", err)}
	}
	defer resp.Body.Close()

	ttfb := time.Since(start)

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return downloadResult{outcome: bootThrottled, ttfb: ttfb}
	case http.StatusNotFound:
		return downloadResult{outcome: bootNotFound, ttfb: ttfb, err: fmt.Errorf("fabric-boot has no switch %s (%s, %s)", t.name, t.serial, t.mac)} //nolint:err113
	default:
		return downloadResult{outcome: bootError, ttfb: ttfb, err: fmt.Errorf("installer request: %s", resp.Status)} //nolint:err113
	}

	tail := &tailWriter{counter: &b.stats.bytes}

	// Only what fabric-boot admitted: a request about to be turned away is
	// not downloading anything.
	b.stats.downloading.Add(1)
	_, err = io.Copy(tail, resp.Body)
	b.stats.downloading.Add(-1)

	took := time.Since(start)

	if err == nil {
		err = tail.check()
	}
	if err != nil {
		return downloadResult{outcome: bootTruncated, start: start, ttfb: ttfb, took: took, bytes: tail.n, err: fmt.Errorf("installer after %d bytes: %w", tail.n, err)}
	}

	return downloadResult{outcome: bootOK, start: start, ttfb: ttfb, took: took, bytes: tail.n}
}

// setONIEHeaders identifies the switch the way ONIE's discovery does. Real ONIE
// always sends both the serial and the MAC, so fabric-boot looks up both.
func setONIEHeaders(req *http.Request, t bootTarget) {
	req.Header.Set("User-Agent", "onie/1.0 (x86_64-"+bootMachine+"-r0; ONIE bench)")
	req.Header.Set("ONIE-SERIAL-NUMBER", t.serial)
	req.Header.Set("ONIE-ETH-ADDR", t.mac)
	req.Header.Set("ONIE-VENDOR-ID", "0")
	req.Header.Set("ONIE-MACHINE", bootMachine)
	req.Header.Set("ONIE-MACHINE-REV", "0")
	req.Header.Set("ONIE-ARCH", "x86_64")
	req.Header.Set("ONIE-OPERATION", "os-install")
	req.Header.Set("ONIE-VERSION", "bench")
}

// tailWriter counts what passes through it and keeps the last bytes, which is
// all it takes to tell a complete installer from one cut off mid-stream.
type tailWriter struct {
	n       int64
	tail    []byte
	counter *atomic.Int64
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	if w.counter != nil {
		w.counter.Add(int64(len(p)))
	}

	w.tail = append(w.tail, p[max(0, len(p)-bootTrailerLen):]...)
	if extra := len(w.tail) - bootTrailerLen; extra > 0 {
		w.tail = append(w.tail[:0], w.tail[extra:]...)
	}

	return len(p), nil
}

var errNoTrailer = errors.New("no installer trailer")

// check verifies the installer ended with its trailer and that the tar it
// declares fits in what was received.
func (w *tailWriter) check() error {
	if len(w.tail) < bootTrailerLen || !bytes.Equal(w.tail[8:], []byte(bootMagic)) {
		return errNoTrailer
	}

	size := binary.BigEndian.Uint64(w.tail[:8])
	if size == 0 || size > uint64(w.n)-uint64(bootTrailerLen) { //nolint:gosec // n counts bytes read, never negative
		return fmt.Errorf("installer declares %d bytes of payload in %d received", size, w.n) //nolint:err113
	}

	return nil
}

// install is the switch running the installer it downloaded: nothing reaches
// fabric-boot but the log lines, posted as the installer would.
//
// The installer logs through slog-webhook, which sends every line from its own
// goroutine and does not wait for it, so a burst of output becomes that many
// concurrent posts. They share the process's keep-alive transport, hence one
// transport per install here, dialled from the same address as everything else.
func (b *bootRunner) install(ctx context.Context, t bootTarget) {
	b.stats.installing.Add(1)
	defer b.stats.installing.Add(-1)

	transport := &http.Transport{DialContext: b.dialer.DialContext}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: bootLogTimeout}

	began := time.Now()

	var wg sync.WaitGroup
	defer wg.Wait()

	for _, line := range installLog(b.opts.LogLines, b.opts.InstallTime) {
		if wait := line.at - time.Since(began); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}

		wg.Go(func() {
			b.postLog(ctx, client, t, line.msg)
		})
	}

	// The install takes its time even when it has little to say.
	if wait := b.opts.InstallTime - time.Since(began); wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// postLog sends one line in slog-webhook's format, which is what fabric-boot
// parses. A lost line is counted, not retried: slog-webhook drops it too.
func (b *bootRunner) postLog(ctx context.Context, client *http.Client, t bootTarget, msg string) {
	body, err := json.Marshal(map[string]any{
		"logger.name":    "samber/slog-webhook",
		"logger.version": "bench",
		"timestamp":      time.Now().UTC(),
		"level":          "INFO",
		"message":        msg,
		"extra":          map[string]string{"serial": t.serial, "ethaddr": t.mac},
	})
	if err != nil {
		b.stats.recordLog(ctx, fmt.Errorf("encoding log line: %w", err))

		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.opts.URL+bootLogPath, bytes.NewReader(body))
	if err != nil {
		b.stats.recordLog(ctx, fmt.Errorf("building log request: %w", err))

		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "samber/slog-webhook")

	resp, err := client.Do(req)
	if err != nil {
		b.stats.recordLog(ctx, fmt.Errorf("posting log line: %w", err))

		return
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		b.stats.recordLog(ctx, fmt.Errorf("posting log line: %s", resp.Status)) //nolint:err113

		return
	}

	b.stats.recordLog(ctx, nil)
}

// logLine is one installer log line and when, into the install, it is posted.
type logLine struct {
	at  time.Duration
	msg string
}

// Messages in the order a SONiC install on a VLAB produced them. The head is
// the installer starting, the tail the agent going in once the NOS is down, and
// between them the NOS installer's own output, most of it one unzip that prints
// in the same instant.
var (
	installLogHead = []string{
		"Sending logs to fabric-boot",
		"ONIE env",
		"Extracting embedded files",
		"Running fabric-nos-install",
		"Embedded files extracted",
		"Executing NOS installer now...",
	}
	installLogNOS = []string{
		"NOS: Verifying image checksum ... OK.",
		"NOS: Preparing image archive ... OK.",
		"NOS: Installing SONiC in ONIE",
		"NOS: ONIE Installer: platform: x86_64-" + bootMachine + "-r0",
		"NOS: deleting partition 3 ...",
		"NOS: The new table will be used at the next reboot.",
		"NOS: Warning: The kernel is still using the old partition table.",
		"NOS: The operation has completed successfully.",
		"NOS: Creating new SONiC-OS partition /dev/sda3 ...",
		"NOS: mke2fs 1.46.3 (27-Jul-2021)",
		"NOS: Creating filesystem with 8388608 4k blocks and 2097152 inodes",
		"NOS: Writing superblocks and filesystem accounting information:   0/256       done",
		"NOS: Installing SONiC to /tmp/tmp.bench/image-4.5.2-Enterprise_Base",
		"NOS: Archive:  fs.zip",
		"NOS: Success: Support tarball created: /tmp/onie-support-" + bootMachine + ".tar.bz2",
		"NOS: Installed SONiC base image SONiC-OS successfully",
	}
	installLogUnzip = []string{
		"boot/initrd.img-5.10.0-39-amd64",
		"boot/vmlinuz-5.10.0-39-amd64",
		"boot/System.map-5.10.0-39-amd64",
		"boot/config-5.10.0-39-amd64",
		"boot/grubx64.efi",
		"boot/shimx64.efi",
		"boot/mmx64.efi",
		"platform/firsttime",
		"platform/x86_64-grub/grub-pc-bin_2.06-3~deb11u6_amd64.deb",
		"installer-migration-hooks/00-cert-migrate",
		"installer-migration-hooks/01-dir-preservation",
		"installer-migration-hooks/01-kdump-pre",
		"installer-migration-hooks/01-local-users-pre",
		"installer-migration-hooks/02-kernel-bootarg-pre",
		"installer-migration-hooks/03-tpcm-version",
		"installer-migration-hooks/08-config-profile-backup",
		"db_migrator.py",
		"sonic_version.yml",
	}
	installLogTail = []string{
		"NOS installer completed",
		"Mounting partition",
		"Installing Fabric Agent binary",
		"Installing Fabric Agent configs",
		"Unmounting partition",
		"Installation complete",
	}
)

// installLogBurstAt is where in the install the unzip lands, and the burst
// share how much of the NOS output it is: 30 of the 58 lines between head and
// tail on the VLAB.
const (
	installLogBurstAt    = 0.4
	installLogBurstLines = 30
	installLogNOSLines   = 58
)

// installLog lays out n log lines over an install of the given length in the
// shape a real one has: the head at once, the NOS output spread across the
// install with the unzip burst in one instant, the tail at the end.
func installLog(n int, total time.Duration) []logLine {
	head := min(n, len(installLogHead))
	tail := min(n-head, len(installLogTail))
	middle := n - head - tail
	burst := middle * installLogBurstLines / installLogNOSLines
	spread := middle - burst

	lines := make([]logLine, 0, n)

	for i := range head {
		lines = append(lines, logLine{at: 0, msg: installLogHead[i]})
	}

	for i := range spread {
		at := time.Duration(float64(total) * float64(i+1) / float64(spread+1))
		lines = append(lines, logLine{at: at, msg: installLogNOS[i%len(installLogNOS)]})
	}

	burstAt := time.Duration(float64(total) * installLogBurstAt)
	for i := range burst {
		lines = append(lines, logLine{at: burstAt, msg: "NOS:   inflating: " + installLogUnzip[i%len(installLogUnzip)]})
	}

	for i := range tail {
		lines = append(lines, logLine{at: total, msg: installLogTail[len(installLogTail)-tail+i]})
	}

	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at < lines[j].at })

	return lines
}
