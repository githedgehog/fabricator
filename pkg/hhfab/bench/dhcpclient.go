// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

// DHCPServerPort is where dhcpd listens and where it sends relayed replies:
// to giaddr on the server port, not the client port.
const DHCPServerPort = 67

// dhcpRecvBuffer is what the relay socket asks for. A round starts with every
// server sending at once, and replies come back as fast as dhcpd produces
// them, so the kernel default would drop most of a burst.
const dhcpRecvBuffer = 64 << 20

// DHCPTransport is the relay's socket: one UDP socket on the relay leaf's
// management IP, port 67, which every simulated server sends through and every
// reply comes back to.
type DHCPTransport struct {
	conn   *net.UDPConn
	server *net.UDPAddr
}

// ListenDHCPRelay binds the relay socket. Port 67 needs root, and the address
// must be on a local interface, which the caller makes sure of.
func ListenDHCPRelay(relay netip.Addr, server netip.AddrPort) (*DHCPTransport, error) {
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(relay, DHCPServerPort)))
	if err != nil {
		return nil, fmt.Errorf("binding the relay socket on %s:%d (needs root and the address on a local interface): %w", relay, DHCPServerPort, err)
	}

	if err := setDHCPBuffers(conn, dhcpRecvBuffer); err != nil {
		slog.Warn("Could not enlarge the relay socket buffers, bursts may be dropped locally", "err", err)
	}

	return &DHCPTransport{conn: conn, server: net.UDPAddrFromAddrPort(server)}, nil
}

func (t *DHCPTransport) Close() error {
	if err := t.conn.Close(); err != nil {
		return fmt.Errorf("closing relay socket: %w", err)
	}

	return nil
}

func (t *DHCPTransport) send(msg *dhcpv4.DHCPv4) error {
	if _, err := t.conn.WriteToUDP(msg.ToBytes(), t.server); err != nil {
		return fmt.Errorf("sending to %s: %w", t.server, err)
	}

	return nil
}

// receive hands every reply to the demux until the socket is closed. Packets
// that don't parse are counted, not fatal: the bench measures, it doesn't
// stop on what dhcpd sends.
func (t *DHCPTransport) receive(demux *dhcpDemux) {
	buf := make([]byte, 64<<10)

	for {
		n, from, err := t.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			continue
		}

		msg, err := dhcpv4.FromBytes(buf[:n])
		if err != nil {
			demux.garbled.Add(1)

			continue
		}

		demux.deliver(msg, from.Addr())
	}
}

// dhcpDemux routes replies to the transaction waiting for them. One receive
// loop serves every simulated server, so it must never block on a slow one:
// each waiter has a small buffer and anything past it is counted and dropped.
type dhcpDemux struct {
	mu      sync.Mutex
	waiting map[dhcpv4.TransactionID]chan *dhcpv4.DHCPv4
	sources map[netip.Addr]int64

	next atomic.Uint32

	late    atomic.Int64
	dropped atomic.Int64
	garbled atomic.Int64
}

func newDHCPDemux() *dhcpDemux {
	d := &dhcpDemux{
		waiting: map[dhcpv4.TransactionID]chan *dhcpv4.DHCPv4{},
		sources: map[netip.Addr]int64{},
	}

	// Transaction IDs come from a counter, so 20k transactions in flight never
	// collide, starting at a random point so runs don't reuse each other's.
	d.next.Store(rand.Uint32()) //nolint:gosec // uniqueness, not secrecy

	return d
}

// open starts a transaction and returns its ID and the channel its replies
// arrive on. Registering before the first send means no reply can race it.
func (d *dhcpDemux) open() (dhcpv4.TransactionID, chan *dhcpv4.DHCPv4) {
	n := d.next.Add(1)
	xid := dhcpv4.TransactionID{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	ch := make(chan *dhcpv4.DHCPv4, 4)

	d.mu.Lock()
	d.waiting[xid] = ch
	d.mu.Unlock()

	return xid, ch
}

func (d *dhcpDemux) close(xid dhcpv4.TransactionID) {
	d.mu.Lock()
	delete(d.waiting, xid)
	d.mu.Unlock()
}

// deliver hands a reply to its transaction. One nobody waits for any more is
// late: an answer that came after the client moved on.
func (d *dhcpDemux) deliver(msg *dhcpv4.DHCPv4, from netip.Addr) {
	d.mu.Lock()
	ch := d.waiting[msg.TransactionID]
	d.sources[from]++
	d.mu.Unlock()

	if ch == nil {
		d.late.Add(1)

		return
	}

	select {
	case ch <- msg:
	default:
		d.dropped.Add(1)
	}
}

// replySources is who answered and how often. dhcpd's source address is
// whatever the kernel picks towards the relay, so it is reported, not checked.
func (d *dhcpDemux) replySources() map[netip.Addr]int64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make(map[netip.Addr]int64, len(d.sources))
	for addr, n := range d.sources {
		out[addr] = n
	}

	return out
}

// dhcpSchedule is a client's retransmit timing: how long to wait for a reply
// after the given attempt, and whether to try again once that runs out.
type dhcpSchedule func(attempt int) (time.Duration, bool)

// osSchedule is RFC 2131 §4.1 as systemd's client does it: 4 s doubling to
// 64 s, ±1 s, and never giving up. A real OS keeps trying until it gets a
// lease, so within a round it does too; the round timeout ends it.
func osSchedule(attempt int) (time.Duration, bool) {
	base := time.Duration(4<<min(attempt, 4)) * time.Second
	fuzz := time.Duration(rand.Int64N(int64(2*time.Second))) - time.Second //nolint:gosec // jitter, not a secret

	return base + fuzz, true
}

// pxeSchedule is EDK2's UEFI PXE (mPxeDhcpTimeout, PXE 2.1 §2.2.2): 4, 8, 16
// and 32 s with no jitter, then the firmware gives up and moves on to the next
// boot entry. DISCOVER and REQUEST both use it.
func pxeSchedule(attempt int) (time.Duration, bool) {
	timeouts := [...]time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}

	return timeouts[attempt], attempt < len(timeouts)-1
}

// repeatSchedule bounds a repeat: it is a probe, so three tries and then it
// counts as unanswered rather than holding the server up.
func repeatSchedule(attempt int) (time.Duration, bool) {
	timeouts := [...]time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second}

	return timeouts[attempt], attempt < len(timeouts)-1
}

var errNoReply = errors.New("no usable reply")

// verdict is what a client makes of a reply: take it, or ignore it and keep
// waiting, as a real client does with one it can't use.
type verdict int

const (
	ignore verdict = iota
	take
)

// dhcpClients is what every simulated server shares: the socket, the demux,
// the relay it pretends to be, and where it records what happened.
type dhcpClients struct {
	send   func(*dhcpv4.DHCPv4) error
	demux  *dhcpDemux
	relay  net.IP
	stats  *dhcpRoundStats
	checks *dhcpChecks

	// Overridable so the state machine can be tested on a fake clock.
	osSched     dhcpSchedule
	pxeSched    dhcpSchedule
	repeatSched dhcpSchedule
}

// exchange sends msg and waits for a reply judge takes, retransmitting on
// sched. A reply for another MAC is counted and ignored, never taken.
func (c *dhcpClients) exchange(ctx context.Context, srv *dhcpServer, msg *dhcpv4.DHCPv4, replies chan *dhcpv4.DHCPv4,
	sched dhcpSchedule, dwell bool, judge func(*dhcpv4.DHCPv4) verdict,
) (*dhcpv4.DHCPv4, error) {
	var taken *dhcpv4.DHCPv4

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			srv.retransmits++
		}

		if err := c.send(msg); err != nil {
			c.stats.sendErrors.Add(1)
		}

		wait, again := sched(attempt)
		timer := time.NewTimer(wait)

	waiting:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()

				return nil, ctx.Err() //nolint:wrapcheck
			case reply := <-replies:
				if !bytes.Equal(reply.ClientHWAddr, srv.mac) {
					c.checks.record(checkMismatchedReply, "%s got a reply for %s", srv.hostname, reply.ClientHWAddr)

					continue
				}

				if judge(reply) != take {
					continue
				}

				// EDK2 collects offers until its first timeout before it
				// requests one, even when an offer came at once.
				if dwell {
					if taken == nil {
						taken = reply
					}

					continue
				}

				timer.Stop()

				return reply, nil
			case <-timer.C:
				break waiting
			}
		}

		if taken != nil {
			return taken, nil
		}
		if !again {
			return nil, errNoReply
		}
	}
}

// dhcpServer is one simulated server's state within a round.
type dhcpServer struct {
	idx      int
	sub      *dhcpSubnet
	mac      net.HardwareAddr
	hostname string

	ip       netip.Addr
	leaseEnd time.Time
	pxeDead  bool

	retransmits int
}

// clientProfile is the client a DORA is done as: the OS, or the PXE firmware
// before it.
type clientProfile struct {
	pxe   bool
	sched dhcpSchedule
}

// message builds a request as the relay would forward it: giaddr is the relay,
// one hop, and option 82 carries the circuit and VRF dhcpd finds the subnet by.
func (c *dhcpClients) message(srv *dhcpServer, mt dhcpv4.MessageType, xid dhcpv4.TransactionID, pxe bool, mods ...dhcpv4.Modifier) (*dhcpv4.DHCPv4, error) {
	base := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithHwAddr(srv.mac),
		dhcpv4.WithTransactionID(xid),
		dhcpv4.WithGatewayIP(c.relay),
		dhcpv4.WithOption(dhcpv4.OptRelayAgentInfo(
			dhcpv4.OptGeneric(dhcpv4.AgentCircuitIDSubOption, []byte(srv.sub.circuitID)),
			dhcpv4.OptGeneric(dhcpv4.VirtualSubnetSelectionSubOption, append([]byte{0}, "VrfV"+srv.sub.vrf...)),
		)),
	}

	if pxe {
		base = append(base,
			dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient:Arch:00007:UNDI:003016")),
			dhcpv4.WithOption(dhcpv4.OptClientArch(iana.EFI_X86_64)),
			dhcpv4.WithGeneric(dhcpv4.OptionClientNetworkInterfaceIdentifier, []byte{1, 3, 16}),
			dhcpv4.WithGeneric(dhcpv4.OptionClientMachineIdentifier, append([]byte{0}, pxeGUID(srv.mac)...)),
			dhcpv4.WithRequestedOptions(
				dhcpv4.OptionSubnetMask, dhcpv4.OptionRouter, dhcpv4.OptionDomainNameServer,
				dhcpv4.OptionTFTPServerName, dhcpv4.OptionBootfileName, dhcpv4.OptionClassIdentifier,
			),
		)
	} else {
		base = append(base,
			dhcpv4.WithOption(dhcpv4.OptHostName(srv.hostname)),
			dhcpv4.WithRequestedOptions(
				dhcpv4.OptionSubnetMask, dhcpv4.OptionRouter, dhcpv4.OptionDomainNameServer,
				dhcpv4.OptionHostName, dhcpv4.OptionDomainName, dhcpv4.OptionInterfaceMTU,
				dhcpv4.OptionNTPServers, dhcpv4.OptionClasslessStaticRoute,
			),
		)
	}

	msg, err := dhcpv4.New(append(base, mods...)...)
	if err != nil {
		return nil, fmt.Errorf("building %s for %s: %w", mt, srv.hostname, err)
	}

	msg.HopCount = 1

	return msg, nil
}

// pxeGUID is the machine identifier PXE firmware sends, derived from the MAC so
// it is stable for a server.
func pxeGUID(mac net.HardwareAddr) []byte {
	out := make([]byte, 16)
	copy(out[10:], mac)

	return out
}

// usableOffer judges an OFFER, counting the ones a client can't use. A 0.0.0.0
// offer is dhcpd failing without saying so: real clients ignore it and keep
// waiting, and so does the bench.
func (c *dhcpClients) judgeOffer(srv *dhcpServer) func(*dhcpv4.DHCPv4) verdict {
	return func(reply *dhcpv4.DHCPv4) verdict {
		if reply.MessageType() != dhcpv4.MessageTypeOffer {
			return ignore
		}

		if reply.YourIPAddr.IsUnspecified() {
			c.stats.zeroOffers.Add(1)
			c.checks.record(checkZeroOffer, "%s got an OFFER for 0.0.0.0", srv.hostname)

			return ignore
		}

		c.checkRequired(srv, reply)

		return take
	}
}

// judgeAck takes an ACK or a NAK. A 0.0.0.0 ACK is ignored the same way as the
// offer, but counted apart: it carries no lease time either, and dhclient
// answers that by blacklisting the relay's address for good.
func (c *dhcpClients) judgeAck(srv *dhcpServer) func(*dhcpv4.DHCPv4) verdict {
	return func(reply *dhcpv4.DHCPv4) verdict {
		switch reply.MessageType() { //nolint:exhaustive // anything else is ignored
		case dhcpv4.MessageTypeNak:
			return take
		case dhcpv4.MessageTypeAck:
			if reply.YourIPAddr.IsUnspecified() {
				c.stats.zeroAcks.Add(1)
				c.checks.record(checkZeroAck, "%s got an ACK for 0.0.0.0", srv.hostname)

				return ignore
			}

			c.checkRequired(srv, reply)

			return take
		default:
			return ignore
		}
	}
}

// checkRequired flags an OFFER or ACK without the options RFC 2131 makes a
// MUST: the lease time and the server identifier.
func (c *dhcpClients) checkRequired(srv *dhcpServer, reply *dhcpv4.DHCPv4) {
	if reply.Options.Get(dhcpv4.OptionIPAddressLeaseTime) == nil {
		c.checks.record(checkNoLeaseTime, "%s got a %s for %s without a lease time", srv.hostname, reply.MessageType(), reply.YourIPAddr)
	}
	if reply.ServerIdentifier() == nil {
		c.checks.record(checkNoServerID, "%s got a %s for %s without a server identifier", srv.hostname, reply.MessageType(), reply.YourIPAddr)
	}
}

// dora gets a lease the way the profile's client does: DISCOVER until a usable
// OFFER, then REQUEST it, starting over on a NAK. It fails only when the
// profile gives up (PXE) or the round ends.
func (c *dhcpClients) dora(ctx context.Context, srv *dhcpServer, prof clientProfile) (*dhcpv4.DHCPv4, error) {
	for {
		xid, replies := c.demux.open()

		discover, err := c.message(srv, dhcpv4.MessageTypeDiscover, xid, prof.pxe)
		if err != nil {
			c.demux.close(xid)

			return nil, err
		}

		offer, err := c.exchange(ctx, srv, discover, replies, prof.sched, prof.pxe, c.judgeOffer(srv))
		if err != nil {
			c.demux.close(xid)

			return nil, err
		}

		offered := offer.YourIPAddr
		serverID := offer.ServerIdentifier()

		mods := []dhcpv4.Modifier{dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(offered))}
		if serverID != nil {
			mods = append(mods, dhcpv4.WithOption(dhcpv4.OptServerIdentifier(serverID)))
		}

		request, err := c.message(srv, dhcpv4.MessageTypeRequest, xid, prof.pxe, mods...)
		if err != nil {
			c.demux.close(xid)

			return nil, err
		}

		judge := c.judgeAck(srv)
		if prof.pxe {
			// EDK2 drops an ACK that doesn't match the offer it requested.
			inner := judge
			judge = func(reply *dhcpv4.DHCPv4) verdict {
				if inner(reply) != take {
					return ignore
				}
				if reply.MessageType() == dhcpv4.MessageTypeAck && (!reply.YourIPAddr.Equal(offered) || !reply.ServerIdentifier().Equal(serverID)) {
					c.compareAck(srv, offered, serverID, reply)

					return ignore
				}

				return take
			}
		}

		ack, err := c.exchange(ctx, srv, request, replies, prof.sched, false, judge)
		c.demux.close(xid)

		if err != nil {
			return nil, err
		}

		if ack.MessageType() == dhcpv4.MessageTypeNak {
			// dhcpd made the offer itself, so it should still hold it.
			c.stats.nakDORA.Add(1)
			c.checks.record(checkOfferNaked, "%s requested %s it was offered, got a NAK", srv.hostname, offered)

			continue
		}

		if !prof.pxe {
			c.compareAck(srv, offered, serverID, ack)
		}

		return ack, nil
	}
}

// compareAck counts an ACK that isn't for the offer that was requested.
func (c *dhcpClients) compareAck(srv *dhcpServer, offered, serverID net.IP, ack *dhcpv4.DHCPv4) {
	if !ack.YourIPAddr.Equal(offered) {
		c.checks.record(checkWrongIP, "%s requested %s, got an ACK for %s", srv.hostname, offered, ack.YourIPAddr)
	}
	if !ack.ServerIdentifier().Equal(serverID) {
		c.checks.record(checkServerIDChanged, "%s was offered by %s, ACKed by %s", srv.hostname, serverID, ack.ServerIdentifier())
	}
}

// renew is the renewal at T1: a REQUEST with ciaddr and no server ID or
// requested IP, as RFC 2131 has it in RENEWING. A real client unicasts it to
// the server ID, where it carries no option 82 and dhcpd can't place it; the
// bench sends it through the relay, which is what dhcpd can answer. A NAK sends
// the client back to the start.
func (c *dhcpClients) renew(ctx context.Context, srv *dhcpServer) (*dhcpv4.DHCPv4, error) {
	xid, replies := c.demux.open()
	defer c.demux.close(xid)

	msg, err := c.message(srv, dhcpv4.MessageTypeRequest, xid, false, dhcpv4.WithClientIP(srv.ip.AsSlice()))
	if err != nil {
		return nil, err
	}

	return c.exchange(ctx, srv, msg, replies, c.osSched, false, c.judgeAck(srv))
}

// repeatKind is what a repeat asks for.
type repeatKind int

const (
	repeatOwn   repeatKind = iota // the server's own IP: should be ACKed with it
	repeatTaken                   // an IP another server holds: should be NAKed
)

// repeatOutcome is how dhcpd answered a repeat.
type repeatOutcome int

const (
	outcomeAckSame  repeatOutcome = iota // ACK for the IP asked for
	outcomeAckOther                      // ACK for some other IP
	outcomeNak
	outcomeZero    // 0.0.0.0
	outcomeNoReply // nothing usable after every retransmit
	repeatOutcomes
)

func (o repeatOutcome) String() string {
	return [...]string{"ack", "ackOther", "nak", "zero", "noReply"}[o]
}

// repeat asks for an IP the way a rebooting client does (INIT-REBOOT: option
// 50, no ciaddr, no server ID) and reports how dhcpd answered. It is a probe:
// whatever comes back, the server keeps the lease it has.
func (c *dhcpClients) repeat(ctx context.Context, srv *dhcpServer, want netip.Addr) (repeatOutcome, *dhcpv4.DHCPv4, error) {
	xid, replies := c.demux.open()
	defer c.demux.close(xid)

	msg, err := c.message(srv, dhcpv4.MessageTypeRequest, xid, false, dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(want.AsSlice())))
	if err != nil {
		return outcomeNoReply, nil, err
	}

	zero := false
	reply, err := c.exchange(ctx, srv, msg, replies, c.repeatSched, false, func(reply *dhcpv4.DHCPv4) verdict {
		switch reply.MessageType() { //nolint:exhaustive // anything else is ignored
		case dhcpv4.MessageTypeNak:
			return take
		case dhcpv4.MessageTypeAck:
			if reply.YourIPAddr.IsUnspecified() {
				zero = true

				return ignore
			}

			return take
		default:
			return ignore
		}
	})

	switch {
	case errors.Is(err, errNoReply) && zero:
		c.stats.zeroAcks.Add(1)
		c.checks.record(checkZeroAck, "%s asked for %s, got an ACK for 0.0.0.0", srv.hostname, want)

		return outcomeZero, nil, nil
	case errors.Is(err, errNoReply):
		return outcomeNoReply, nil, nil
	case err != nil:
		return outcomeNoReply, nil, err
	case reply.MessageType() == dhcpv4.MessageTypeNak:
		return outcomeNak, reply, nil
	}

	got, _ := netip.AddrFromSlice(reply.YourIPAddr.To4())
	if got == want {
		return outcomeAckSame, reply, nil
	}

	return outcomeAckOther, reply, nil
}

// leaseIP is the IP an ACK hands out.
func leaseIP(ack *dhcpv4.DHCPv4) netip.Addr {
	ip, _ := netip.AddrFromSlice(ack.YourIPAddr.To4())

	return ip
}

// leaseDuration is the ACK's lease, or dhcpd's L3VNI first lease if it sent
// none, so a client missing the option still renews rather than stalls.
func leaseDuration(ack *dhcpv4.DHCPv4) time.Duration {
	return ack.IPAddressLeaseTime(10 * time.Second)
}
