// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// dhcpProbeTimeout is how long a round waits for dhcpd to answer the probe for
// a subnet it has just been given, which takes as long as its watch does.
const dhcpProbeTimeout = 2 * time.Minute

// DHCPOpts configures the DHCP storm.
type DHCPOpts struct {
	Layout         DHCPLayout
	Rounds         int
	Repeats        int
	RepeatInterval time.Duration
	Spread         time.Duration
	RoundTimeout   time.Duration
	PXE            bool

	// Relay is the leaf whose management IP the bench relays from, and Server
	// where dhcpd listens: the control VIP.
	Relay  DHCPRelay
	Server netip.AddrPort
}

func (o DHCPOpts) validate() error {
	switch {
	case o.Rounds < 1:
		return fmt.Errorf("rounds must be at least 1, got %d", o.Rounds) //nolint:err113
	case o.Repeats < 0:
		return fmt.Errorf("repeats must not be negative, got %d", o.Repeats) //nolint:err113
	case o.Repeats > 0 && o.RepeatInterval <= 0:
		return fmt.Errorf("repeat interval must be positive, got %s", o.RepeatInterval) //nolint:err113
	case o.Spread < 0:
		return fmt.Errorf("spread must not be negative, got %s", o.Spread) //nolint:err113
	case o.RoundTimeout <= 0:
		return fmt.Errorf("round timeout must be positive, got %s", o.RoundTimeout) //nolint:err113
	case o.Spread >= o.RoundTimeout:
		// Servers that would start after the round ends would count as unbound.
		return fmt.Errorf("spread %s must be shorter than the round timeout %s", o.Spread, o.RoundTimeout) //nolint:err113
	}

	return nil
}

// RunDHCP runs the rounds. Each one starts from an empty dhcpd, recreating the
// bench DHCP VPCs, then boots every server at once through DORA, the first
// renewal and the repeats, and checks what dhcpd stored. A round's failures are
// counted, never fatal; only the bench being unable to set a round up stops it.
func RunDHCP(ctx context.Context, kube kclient.Client, transport *DHCPTransport, opts DHCPOpts) error {
	if err := opts.validate(); err != nil {
		return err
	}

	plan, err := planDHCP(opts.Layout)
	if err != nil {
		return err
	}

	pxeURL := ""
	if opts.PXE {
		// http keeps option 54 intact; dhcpd's tftp path overwrites it.
		pxeURL = fmt.Sprintf("http://%s/bench/pxe.efi", opts.Server.Addr())
	}

	objs := dhcpObjects(plan, pxeURL)

	slog.Info("Preparing DHCP storm", "servers", opts.Layout.Servers, "vpcs", opts.Layout.vpcs(), "subnetsPerVPC", opts.Layout.Subnets,
		"serversPerSubnet", dhcpServersPerSubnet(plan), "prefix", plan[0].prefix.Bits(),
		"rounds", opts.Rounds, "spread", opts.Spread, "repeats", opts.Repeats, "pxe", opts.PXE, "relay", opts.Relay.Name, "giaddr", opts.Relay.IP.Addr(),
		"server", opts.Server)

	demux := newDHCPDemux()

	go transport.receive(demux)

	summary := newDHCPSummary()

	// A round that can't be set up, or is interrupted, ends the run, but the
	// rounds that finished are still summed up before that's reported.
	var runErr error

	for round := range opts.Rounds {
		// An interrupted round comes back with what it found, to be summed up
		// with the rest.
		res, err := runDHCPRound(ctx, kube, transport.send, demux, plan, objs, opts, round)
		if res != nil {
			summary.add(res)
		}

		if err != nil {
			if ctx.Err() == nil {
				runErr = fmt.Errorf("round %d: %w", round+1, err)
			}

			break
		}
	}

	summary.report(opts, demux.totals())

	return runErr
}

// runDHCPRound is one round: reset, probe, storm, check.
func runDHCPRound(ctx context.Context, kube kclient.Client, send func(*dhcpv4.DHCPv4) error, demux *dhcpDemux,
	plan []*dhcpSubnet, objs []kclient.Object, opts DHCPOpts, round int,
) (*dhcpRoundResult, error) {
	cleanupStart := time.Now()

	clearDHCPKeys(plan)
	if err := resetDHCPObjects(ctx, kube, plan, objs); err != nil {
		return nil, err
	}

	stats := newDHCPRoundStats(round, opts.Rounds, opts.Layout.Servers)
	checks := newDHCPChecks()

	clients := &dhcpClients{
		send:        send,
		demux:       demux,
		relay:       opts.Relay.IP.Addr().AsSlice(),
		stats:       stats,
		checks:      checks,
		osSched:     osSchedule,
		pxeSched:    pxeSchedule,
		repeatSched: repeatSchedule,
	}

	if err := probeDHCP(ctx, clients, plan); err != nil {
		return nil, err
	}

	cleanup := time.Since(cleanupStart)

	// The demux outlives rounds, so its counters are read as this round's
	// share.
	late, dropped, garbled := demux.late.Load(), demux.dropped.Load(), demux.garbled.Load()
	overflowed, readErrors := demux.overflowed.Load(), demux.readErrors.Load()

	held := newDHCPHeld(plan)
	servers := newDHCPServers(plan)

	reportCtx, stopReport := context.WithCancel(ctx)

	var reportWG sync.WaitGroup
	reportWG.Go(func() { stats.report(reportCtx) })

	storm := runDHCPStorm(ctx, clients, servers, held, opts, stats)

	stopReport()
	reportWG.Wait()

	// A storm cut short keeps what it found, but its unbound servers didn't
	// fail to bind, they were stopped, and dhcpd's record can't be read.
	interrupted := ctx.Err() != nil
	if !interrupted {
		checkStoredLeases(ctx, kube, checks, plan, servers)
	}

	res := stats.result(cleanup, storm, checks, servers, interrupted)
	res.late = demux.late.Load() - late
	res.dropped = demux.dropped.Load() - dropped
	res.garbled = demux.garbled.Load() - garbled
	res.overflowed = demux.overflowed.Load() - overflowed
	res.readErrors = demux.readErrors.Load() - readErrors
	res.log()

	if interrupted {
		return res, fmt.Errorf("storm interrupted: %w", ctx.Err())
	}

	return res, nil
}

// probeDHCP waits until dhcpd answers each subnet, which proves it has the
// DHCPSubnet the controller just created. The probe uses a MAC no server has,
// whose offer is left to expire; subnets are sized so it never starves one.
func probeDHCP(ctx context.Context, clients *dhcpClients, plan []*dhcpSubnet) error {
	for _, sub := range plan {
		if err := probeDHCPSubnet(ctx, clients, sub); err != nil {
			return err
		}
	}

	return nil
}

// probeDHCPSubnet probes one subnet, with a timeout of its own: after a storm
// dhcpd may be slow to see the new subnets, and the first ones shouldn't use
// up the wait of the rest.
func probeDHCPSubnet(ctx context.Context, clients *dhcpClients, sub *dhcpSubnet) error {
	ctx, cancel := context.WithTimeout(ctx, dhcpProbeTimeout)
	defer cancel()

	probe := func(int) (time.Duration, bool) { return 2 * time.Second, true }
	srv := &dhcpServer{idx: -1, sub: sub, mac: dhcpProbeMAC(sub.index), hostname: "probe-" + sub.dhcpSubnetName()}

	xid, replies := clients.demux.open()
	defer clients.demux.close(xid)

	msg, err := clients.message(srv, dhcpv4.MessageTypeDiscover, xid, false)
	if err != nil {
		return err
	}

	if _, err := clients.exchange(ctx, srv, msg, replies, probe, false, func(reply *dhcpv4.DHCPv4) verdict {
		if reply.MessageType() == dhcpv4.MessageTypeOffer && !reply.YourIPAddr.IsUnspecified() {
			return take
		}

		return ignore
	}); err != nil {
		return fmt.Errorf("dhcpd never offered an address in %s (circuit %s, vrf %s) within %s: %w",
			sub.dhcpSubnetName(), sub.circuitID, sub.vrf, dhcpProbeTimeout, err)
	}

	return nil
}

// newDHCPServers numbers the fleet's servers in plan order, so each subnet's
// are servers[sub.first:sub.first+sub.servers].
func newDHCPServers(plan []*dhcpSubnet) []*dhcpServer {
	last := plan[len(plan)-1]
	out := make([]*dhcpServer, 0, last.first+last.servers)

	for _, sub := range plan {
		for idx := range sub.servers {
			global := sub.first + idx
			out = append(out, &dhcpServer{
				idx:      global,
				sub:      sub,
				mac:      dhcpServerMAC(global),
				hostname: dhcpHostname(sub, idx),
			})
		}
	}

	return out
}

// runDHCPStorm releases every server at once, or spread over opts.Spread, and
// waits for all of them or the round timeout.
func runDHCPStorm(ctx context.Context, clients *dhcpClients, servers []*dhcpServer, held *dhcpHeld, opts DHCPOpts, stats *dhcpRoundStats) time.Duration {
	ctx, cancel := context.WithTimeout(ctx, opts.RoundTimeout)
	defer cancel()

	start := make(chan struct{})

	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-start:
			}

			if opts.Spread > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(rand.Int64N(int64(opts.Spread)))): //nolint:gosec // spread, not a secret
				}
			}

			clients.boot(ctx, srv, held, opts)
		})
	}

	began := time.Now()
	stats.begin(began)
	close(start)

	wg.Wait()

	return time.Since(began)
}

// boot is one server's round: the PXE firmware first if asked for, the OS's
// DORA and first renewal, then the repeats.
func (c *dhcpClients) boot(ctx context.Context, srv *dhcpServer, held *dhcpHeld, opts DHCPOpts) {
	defer c.stats.finish(srv)
	defer held.done(srv)

	if opts.PXE {
		c.stats.enter(phasePXE)

		start := time.Now()
		_, err := c.dora(ctx, srv, clientProfile{pxe: true, sched: c.pxeSched})

		c.stats.leave(phasePXE)

		switch {
		case errors.Is(err, errNoReply), errors.Is(err, errPXEAborted):
			srv.pxeDead = true
			c.stats.pxeDead.Add(1)
			c.checks.recordf(checkUnbound, "%s: PXE firmware gave up: %v", srv.hostname, err)

			return
		case err != nil:
			return
		}

		c.stats.observe(&c.stats.pxeLat, time.Since(start))
	}

	if !c.bind(ctx, srv, held) {
		return
	}

	c.stats.bound.Add(1)

	c.repeats(ctx, srv, held, opts)
	srv.finished = ctx.Err() == nil
}

// bind runs DORA and the first renewal until the server holds a renewed lease,
// starting over whenever dhcpd NAKs it.
func (c *dhcpClients) bind(ctx context.Context, srv *dhcpServer, held *dhcpHeld) bool {
	for {
		start := time.Now()

		c.stats.enter(phaseDORA)
		ack, err := c.dora(ctx, srv, clientProfile{sched: c.osSched})
		c.stats.leave(phaseDORA)

		if err != nil {
			return false
		}

		// How long the server waited for a lease, restarts and the waits
		// before them included: what dhcpd's NAKs and silence cost a client.
		c.stats.observe(&c.stats.doraLat, time.Since(start))

		srv.ip = leaseIP(ack)
		c.checkAssigned(srv, held)

		switch c.renewLease(ctx, srv, held, ack) {
		case renewalStopped:
			return false
		case renewalNaked:
			if !srv.restart(ctx) {
				return false
			}

			continue
		case renewed:
		}

		srv.restartDelay = 0

		return true
	}
}

// renewal is how renewing a lease went.
type renewal int

const (
	renewed        renewal = iota
	renewalNaked           // the lease is lost and the server must start over
	renewalStopped         // the round ended first
)

// renewLease renews the lease ack granted at its T1, as a client holding it
// does: a NAK for a lease dhcpd just granted is flagged and the lease given
// up, an ACK makes the renewed lease the one the server holds.
func (c *dhcpClients) renewLease(ctx context.Context, srv *dhcpServer, held *dhcpHeld, ack *dhcpv4.DHCPv4) renewal {
	c.stats.enter(phaseRenew)
	defer c.stats.leave(phaseRenew)

	select {
	case <-ctx.Done():
		return renewalStopped
	case <-time.After(renewalWait(ack)):
	}

	renewStart := time.Now()

	reply, err := c.renew(ctx, srv)
	if err != nil {
		return renewalStopped
	}

	if reply.MessageType() == dhcpv4.MessageTypeNak {
		c.stats.nakRenewal.Add(1)
		c.checks.recordf(checkLeaseNaked, "%s renewed %s it was ACKed, got a NAK", srv.hostname, srv.ip)
		held.release(srv)

		return renewalNaked
	}

	c.stats.observe(&c.stats.renewLat, time.Since(renewStart))

	if got := leaseIP(reply); got != srv.ip {
		c.checks.recordf(checkRenewalChangedIP, "%s renewed %s, got %s", srv.hostname, srv.ip, got)
		held.release(srv)
		srv.ip = got
		c.checkAssigned(srv, held)
	}

	srv.leaseEnd = renewStart.Add(leaseDuration(reply))

	return renewed
}

// dhcpMaxRenewalWait caps the wait for T1. In L3VNI dhcpd's first lease is
// 10 s, so T1 is 5 s; a subnet handing out its full lease at once would
// otherwise hold every server for half an hour before its renewal.
const dhcpMaxRenewalWait = 30 * time.Second

func renewalWait(ack *dhcpv4.DHCPv4) time.Duration {
	return min(leaseDuration(ack)/2, dhcpMaxRenewalWait)
}

// checkAssigned records the server as holding its IP, flagging an IP outside
// the subnet's range or already held by another server.
func (c *dhcpClients) checkAssigned(srv *dhcpServer, held *dhcpHeld) {
	if !srv.sub.usable(srv.ip) {
		c.checks.recordf(checkOutOfRange, "%s got %s, outside %s-%s of %s", srv.hostname, srv.ip, srv.sub.start, srv.sub.end, srv.sub.prefix)
	}

	if other := held.hold(srv); other != nil {
		c.checks.recordf(checkOverlap, "%s and %s both hold %s", srv.hostname, other.hostname, srv.ip)
	}
}

// repeats sends the server's repeat REQUESTs, alternating its own IP and one
// another server holds, each after a jittered pause. A server alone in its
// subnet has no one to take an IP from, so it only asks for its own.
func (c *dhcpClients) repeats(ctx context.Context, srv *dhcpServer, held *dhcpHeld, opts DHCPOpts) {
	c.stats.enter(phaseRepeat)
	defer c.stats.leave(phaseRepeat)

	for idx := range opts.Repeats {
		taken := idx%2 == 1
		if taken && srv.sub.servers < 2 {
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(opts.RepeatInterval)):
		}

		kind, want := repeatOwn, srv.ip
		if taken {
			kind = repeatTaken

			var ok bool
			if want, ok = held.pickOther(ctx, srv); !ok {
				if ctx.Err() != nil {
					return
				}

				continue
			}
		}

		start := time.Now()

		outcome, reply, err := c.repeat(ctx, srv, want)
		if err != nil {
			return
		}

		c.stats.repeat(kind, outcome, time.Since(start))

		// RFC 2131 4.3.2: a REQUEST in INIT-REBOOT is ACKed with the IP asked
		// for when it is the client's, and NAKed when it is not. dhcpd may only
		// stay silent for a client it has no record of, and it has this one's.
		switch {
		case outcome == outcomeNoReply:
			c.checks.recordf(checkRepeatUnanswered, "%s asked for %s (%s) and got no answer to any retransmit",
				srv.hostname, want, repeatKindNames[kind])
		case kind == repeatOwn && outcome == outcomeNak:
			c.checks.recordf(checkLeaseNaked, "%s asked again for its %s, got a NAK", srv.hostname, want)
		case kind == repeatOwn && outcome == outcomeAckOther:
			c.checks.recordf(checkWrongIP, "%s asked again for its %s, got an ACK for %s", srv.hostname, want, leaseIP(reply))
		case kind == repeatTaken && outcome == outcomeAckSame:
			c.checks.recordf(checkTakenGranted, "%s was granted %s, which another server holds", srv.hostname, want)
		case kind == repeatTaken && outcome == outcomeAckOther:
			c.checks.recordf(checkTakenNotNaked, "%s asked for %s, which another server holds, got an ACK for %s (its own is %s)",
				srv.hostname, want, leaseIP(reply), srv.ip)
		}

		// An ACK for the server's own IP is the lease it holds now (RFC 2131
		// 4.4.1), however short: dhcpd's first lease is 10s. Like a rebooted
		// client it renews that at T1 rather than let it lapse while the bench
		// still counts the IP as held.
		if reply == nil || reply.MessageType() != dhcpv4.MessageTypeAck || leaseIP(reply) != srv.ip {
			continue
		}

		srv.leaseEnd = start.Add(leaseDuration(reply))

		switch c.renewLease(ctx, srv, held, reply) {
		case renewalStopped:
			return
		case renewalNaked:
			// The lease is lost: back to the start, as a client would.
			if !srv.restart(ctx) || !c.bind(ctx, srv, held) {
				return
			}
		case renewed:
		}
	}
}

// dhcpHeld is who holds which IP in each subnet, from the ACKs the bench has
// seen. It flags an IP handed to two servers, and gives taken repeats an IP
// that really is held by someone else, so a NAK is the only right answer.
type dhcpHeld struct {
	mu      sync.Mutex
	changed *sync.Cond
	subnets map[int]*heldSubnet

	// Servers of each subnet still in their round, bound or not: until none
	// is left but the one asking, someone may yet hold an IP to ask for.
	running map[int]int
}

type heldSubnet struct {
	owner map[netip.Addr]*dhcpServer
	ips   []netip.Addr
}

func newDHCPHeld(plan []*dhcpSubnet) *dhcpHeld {
	h := &dhcpHeld{subnets: make(map[int]*heldSubnet, len(plan)), running: make(map[int]int, len(plan))}
	h.changed = sync.NewCond(&h.mu)

	for _, sub := range plan {
		h.subnets[sub.index] = &heldSubnet{owner: map[netip.Addr]*dhcpServer{}}
		h.running[sub.index] = sub.servers
	}

	return h
}

// done records srv leaving its round. What it holds stays held: its lease
// outlives the round.
func (h *dhcpHeld) done(srv *dhcpServer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.running[srv.sub.index]--
	h.changed.Broadcast()
}

// hold records srv as holding srv.ip and returns another server already
// holding it, if any. The first holder keeps the record.
func (h *dhcpHeld) hold(srv *dhcpServer) *dhcpServer {
	h.mu.Lock()
	defer h.mu.Unlock()

	sub := h.subnets[srv.sub.index]
	if other, ok := sub.owner[srv.ip]; ok && other != srv {
		return other
	}

	if _, ok := sub.owner[srv.ip]; !ok {
		sub.owner[srv.ip] = srv
		sub.ips = append(sub.ips, srv.ip)
		h.changed.Broadcast()
	}

	return nil
}

func (h *dhcpHeld) release(srv *dhcpServer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	sub := h.subnets[srv.sub.index]
	if sub.owner[srv.ip] != srv {
		return
	}

	delete(sub.owner, srv.ip)
	for idx, ip := range sub.ips {
		if ip == srv.ip {
			sub.ips = append(sub.ips[:idx], sub.ips[idx+1:]...)

			break
		}
	}
}

// pickOther returns an IP another server in srv's subnet holds right now,
// waiting until there is one. It reports false if the round ends first, or
// once no other server holds one and none is left that still could, such as
// when every other one gave up in PXE.
func (h *dhcpHeld) pickOther(ctx context.Context, srv *dhcpServer) (netip.Addr, bool) {
	stop := context.AfterFunc(ctx, func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		h.changed.Broadcast()
	})
	defer stop()

	h.mu.Lock()
	defer h.mu.Unlock()

	sub := h.subnets[srv.sub.index]

	for {
		if ctx.Err() != nil {
			return netip.Addr{}, false
		}

		// Everyone but srv: at most one entry is its own, so a pick that hits
		// it moves to the next one.
		if n := len(sub.ips); n > 1 || (n == 1 && sub.owner[sub.ips[0]] != srv) {
			idx := rand.IntN(n) //nolint:gosec // selection, not a secret
			if sub.owner[sub.ips[idx]] == srv {
				idx = (idx + 1) % n
			}

			return sub.ips[idx], true
		}

		if h.running[srv.sub.index] <= 1 {
			return netip.Addr{}, false
		}

		h.changed.Wait()
	}
}
