// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package dhcpload

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// topology

type subnet struct {
	idx       int
	vlan      int
	circuitID string
	prefix    netip.Prefix
}

func (c *Config) NumSubnets() int {
	switch c.Layout {
	case LayoutFlat:
		return 1
	case LayoutLeaf:
		return c.Leaves * c.NICs
	default:
		return c.NICs
	}
}

func (c *Config) subnet(idx int) subnet {
	base := binary.BigEndian.Uint32(c.CIDRBase.AsSlice()) + uint32(idx)*4096 //nolint:gosec
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], base)

	return subnet{
		idx:       idx,
		vlan:      c.VLANBase + idx,
		circuitID: fmt.Sprintf("Vlan%d", c.VLANBase+idx),
		prefix:    netip.PrefixFrom(netip.AddrFrom4(b), 20),
	}
}

func (c *Config) relayIP(leaf int) netip.Addr {
	ip := c.RelayBase
	for range leaf {
		ip = ip.Next()
	}

	return ip
}

// client index -> leaf and subnet
func (c *Config) placement(clientIdx int) (int, int) {
	nic := clientIdx % c.NICs
	leaf := clientIdx % c.Leaves
	switch c.Layout {
	case LayoutFlat:
		return leaf, 0
	case LayoutLeaf:
		return leaf, leaf*c.NICs + nic
	default:
		return leaf, nic
	}
}

// openEndpoints opens one UDP socket per emulated leaf (relay mode) or one VLAN interface per subnet (access mode), the
// slice is returned even on error so that what was opened can be closed
func (c *Config) openEndpoints(ctx context.Context, st *stats) ([]endpoint, error) {
	if c.Mode == ModeAccess {
		return c.openAccessEndpoints(ctx, st)
	}

	serverAddr := net.UDPAddrFromAddrPort(c.Server)
	xids := &sync.Map{}
	eps := make([]endpoint, c.Leaves)
	for i := range c.Leaves {
		ip := c.relayIP(i)
		conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(c.RelayPort)))) //nolint:gosec
		if err != nil {
			return eps, fmt.Errorf("binding relay %s:%d (is the IP configured locally, are you root?): %w", ip, c.RelayPort, err)
		}
		if err := conn.SetReadBuffer(8 << 20); err != nil {
			slog.Warn("Setting socket read buffer", "err", err)
		}
		r := &relay{ip: ip, conn: conn, server: serverAddr, xids: xids}
		eps[i] = r
		go r.serve(ctx, st)
	}

	return eps, nil
}

// relays: one UDP socket per emulated leaf, bound to giaddr:67; replies are routed to clients by xid

// endpoint is where clients send from and receive replies at: an emulated leaf relay or a VLAN interface of a server
type endpoint interface {
	name() string
	pending() *sync.Map
	send(cl *client, msg *dhcpv4.DHCPv4) error
	close()
}

type relay struct {
	ip     netip.Addr
	conn   *net.UDPConn
	server *net.UDPAddr
	xids   *sync.Map // xid -> chan *dhcpv4.DHCPv4
}

func (r *relay) name() string       { return r.ip.String() }
func (r *relay) pending() *sync.Map { return r.xids }
func (r *relay) close()             { r.conn.Close() }

func (r *relay) serve(ctx context.Context, stats *stats) {
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Relay read failed", "relay", r.ip, "err", err)
			}

			continue
		}
		deliver(r.xids, buf[:n], stats)
	}
}

// deliver hands a received DHCP payload to the client waiting for its xid
func deliver(xids *sync.Map, payload []byte, stats *stats) {
	msg, err := dhcpv4.FromBytes(payload)
	if err != nil {
		stats.badReplies.Add(1)

		return
	}
	ch, ok := xids.Load(msg.TransactionID)
	if !ok {
		stats.lateReplies.Add(1) // retransmit answered after we moved on, or not ours

		return
	}
	select {
	case ch.(chan *dhcpv4.DHCPv4) <- msg:
	default:
	}
}

func (r *relay) send(_ *client, msg *dhcpv4.DHCPv4) error {
	_, err := r.conn.WriteToUDP(msg.ToBytes(), r.server)

	return err //nolint:wrapcheck
}

// stats

type stats struct {
	m sync.Mutex

	offerLat []time.Duration // DISCOVER sent -> OFFER (first attempt of the exchange)
	ackLat   []time.Duration // REQUEST sent -> ACK
	doraLat  []time.Duration // first DISCOVER -> ACK
	renewLat []time.Duration

	started, bound, failed                    atomic.Int64
	sent, retransmits, naks                   atomic.Int64
	renewsOK, renewsFailed, ipChanges, leases atomic.Int64
	lateReplies, badReplies                   atomic.Int64
}

func (s *stats) add(dst *[]time.Duration, d time.Duration) {
	s.m.Lock()
	*dst = append(*dst, d)
	s.m.Unlock()
}

func pct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	i := int(p / 100 * float64(len(s)-1))

	return s[i]
}

func (s *stats) latLine(name string, ds []time.Duration) string {
	if len(ds) == 0 {
		return fmt.Sprintf("%-6s n=0", name)
	}

	return fmt.Sprintf("%-6s n=%-5d p50=%-9s p95=%-9s p99=%-9s max=%s", name, len(ds),
		pct(ds, 50).Round(time.Millisecond), pct(ds, 95).Round(time.Millisecond),
		pct(ds, 99).Round(time.Millisecond), pct(ds, 100).Round(time.Millisecond))
}

// clients

type clientState string

const (
	stateWaiting clientState = "waiting"
	stateInit    clientState = "init"
	stateBound   clientState = "bound"
	stateFailed  clientState = "failed"
)

type client struct {
	idx      int
	mac      net.HardwareAddr
	hostname string
	ep       endpoint
	subnet   subnet

	// results, written by the client goroutine only, read after it exits
	state          clientState
	ip             net.IP
	serverID       net.IP
	discoverTries  int
	requestTries   int
	dora           time.Duration
	renewsOK       int
	renewsFailed   int
	ipChanges      int
	naks           int
	lastErr        string
	boundAt        time.Time
	lease, renewAt time.Duration
}

type runner struct {
	cfg      *Config
	stats    *stats
	schedule []time.Duration
	rnd      *rand.Rand
	rndM     sync.Mutex

	total      int64
	begin      time.Time
	allBoundAt atomic.Int64
}

var errNoReply = errors.New("no reply")

func (r *runner) jitter() time.Duration {
	if r.cfg.Retries != RetriesRFC {
		return 0
	}
	r.rndM.Lock()
	defer r.rndM.Unlock()

	return time.Duration(r.rnd.Int64N(int64(2*time.Second))) - time.Second
}

func (r *runner) baseMsg(cl *client, mt dhcpv4.MessageType, xid dhcpv4.TransactionID, mods ...dhcpv4.Modifier) (*dhcpv4.DHCPv4, error) {
	rai := []dhcpv4.Option{
		dhcpv4.OptGeneric(dhcpv4.AgentCircuitIDSubOption, []byte(cl.subnet.circuitID)),
		// VSS type 0 (NVT ASCII VPN identifier) + VRF name as SONiC names it
		dhcpv4.OptGeneric(dhcpv4.VirtualSubnetSelectionSubOption, append([]byte{0}, "VrfV"+r.cfg.VPCName()...)),
	}
	if r.cfg.RemoteID != "" {
		rai = append(rai, dhcpv4.OptGeneric(dhcpv4.AgentRemoteIDSubOption, []byte(r.cfg.RemoteID)))
	}
	reqOpts := []dhcpv4.OptionCode{
		dhcpv4.OptionSubnetMask, dhcpv4.OptionRouter, dhcpv4.OptionDomainNameServer, dhcpv4.OptionDomainName,
		dhcpv4.OptionIPAddressLeaseTime, dhcpv4.OptionServerIdentifier, dhcpv4.OptionRenewTimeValue,
		dhcpv4.OptionRebindingTimeValue, dhcpv4.OptionInterfaceMTU, dhcpv4.OptionClasslessStaticRoute,
	}
	if r.cfg.PXE {
		reqOpts = append(reqOpts, dhcpv4.OptionTFTPServerName, dhcpv4.OptionBootfileName)
	}

	all := []dhcpv4.Modifier{
		dhcpv4.WithTransactionID(xid),
		dhcpv4.WithHwAddr(cl.mac),
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithOption(dhcpv4.OptHostName(cl.hostname)),
		dhcpv4.WithOption(dhcpv4.OptParameterRequestList(reqOpts...)),
	}
	if r.cfg.Mode == ModeAccess {
		// a real leaf relays this, adding giaddr and option 82 itself; replies come back as broadcasts
		all = append(all, dhcpv4.WithBroadcast(true))
	} else {
		all = append(all, dhcpv4.WithGatewayIP(cl.ep.(*relay).ip.AsSlice()))
	}
	if r.cfg.VendorClass != "" {
		all = append(all, dhcpv4.WithOption(dhcpv4.OptClassIdentifier(r.cfg.VendorClass)))
	}
	all = append(all, mods...)
	if r.cfg.Mode != ModeAccess {
		// relay agent info must be the last option before END
		all = append(all, dhcpv4.WithOption(dhcpv4.OptRelayAgentInfo(rai...)))
	}

	msg, err := dhcpv4.New(all...)
	if err != nil {
		return nil, fmt.Errorf("building %s: %w", mt, err)
	}
	if r.cfg.Mode != ModeAccess {
		msg.HopCount = 1
	}

	return msg, nil
}

// exchange sends msg (retransmitting per schedule) until a reply of one of the wanted types arrives.
// Returns the reply, the number of transmissions and the latency from the transmission it answers.
func (r *runner) exchange(ctx context.Context, cl *client, build func(xid dhcpv4.TransactionID) (*dhcpv4.DHCPv4, error),
	want ...dhcpv4.MessageType,
) (*dhcpv4.DHCPv4, int, time.Duration, error) {
	xid, err := dhcpv4.GenerateTransactionID()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("generating xid: %w", err)
	}
	ch := make(chan *dhcpv4.DHCPv4, 4)
	cl.ep.pending().Store(xid, ch)
	defer cl.ep.pending().Delete(xid)

	msg, err := build(xid)
	if err != nil {
		return nil, 0, 0, err
	}

	start := time.Now()
	attempts := r.cfg.MaxAttempts
	if attempts <= 0 {
		attempts = len(r.schedule)
	}
	for attempt := range attempts {
		msg.NumSeconds = uint16(min(time.Since(start)/time.Second, 0xffff)) //nolint:gosec
		sentAt := time.Now()
		if err := cl.ep.send(cl, msg); err != nil {
			return nil, attempt + 1, 0, fmt.Errorf("sending %s: %w", msg.MessageType(), err)
		}
		r.stats.sent.Add(1)
		if attempt > 0 {
			r.stats.retransmits.Add(1)
		}

		wait := r.schedule[min(attempt, len(r.schedule)-1)] + r.jitter()
		timer := time.NewTimer(wait)
	recv:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()

				return nil, attempt + 1, 0, ctx.Err() //nolint:wrapcheck
			case <-timer.C:
				break recv
			case resp := <-ch:
				if slices.Contains(want, resp.MessageType()) {
					timer.Stop()

					return resp, attempt + 1, time.Since(sentAt), nil
				}
			}
		}
	}

	return nil, attempts, 0, errNoReply
}

func (r *runner) dora(ctx context.Context, cl *client) error {
	start := time.Now()

	offer, tries, lat, err := r.exchange(ctx, cl, func(xid dhcpv4.TransactionID) (*dhcpv4.DHCPv4, error) {
		return r.baseMsg(cl, dhcpv4.MessageTypeDiscover, xid)
	}, dhcpv4.MessageTypeOffer)
	cl.discoverTries += tries
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	r.stats.add(&r.stats.offerLat, lat)

	ack, tries, lat, err := r.exchange(ctx, cl, func(xid dhcpv4.TransactionID) (*dhcpv4.DHCPv4, error) {
		return r.baseMsg(cl, dhcpv4.MessageTypeRequest, xid,
			dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(offer.YourIPAddr)),
			dhcpv4.WithOption(dhcpv4.OptServerIdentifier(offer.ServerIdentifier())))
	}, dhcpv4.MessageTypeAck, dhcpv4.MessageTypeNak)
	cl.requestTries += tries
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	if ack.MessageType() == dhcpv4.MessageTypeNak {
		cl.naks++
		r.stats.naks.Add(1)

		return errors.New("request: NAK") //nolint:err113
	}
	r.stats.add(&r.stats.ackLat, lat)

	cl.dora = time.Since(start)
	r.stats.add(&r.stats.doraLat, cl.dora)
	r.bind(cl, ack)

	return nil
}

func (r *runner) bind(cl *client, ack *dhcpv4.DHCPv4) {
	if cl.ip != nil && !cl.ip.Equal(ack.YourIPAddr) {
		cl.ipChanges++
		r.stats.ipChanges.Add(1)
	}
	cl.ip = ack.YourIPAddr
	cl.serverID = ack.ServerIdentifier()
	cl.boundAt = time.Now()
	cl.lease = ack.IPAddressLeaseTime(time.Hour)
	cl.renewAt = ack.IPAddressRenewalTime(cl.lease / 2)
	if r.cfg.RenewEvery > 0 {
		cl.renewAt = r.cfg.RenewEvery
	}
	r.stats.leases.Add(1)
}

// renew sends a REQUEST with ciaddr through the relay, as a leaf relaying a renewal would.
func (r *runner) renew(ctx context.Context, cl *client) error {
	ack, _, lat, err := r.exchange(ctx, cl, func(xid dhcpv4.TransactionID) (*dhcpv4.DHCPv4, error) {
		return r.baseMsg(cl, dhcpv4.MessageTypeRequest, xid, dhcpv4.WithClientIP(cl.ip))
	}, dhcpv4.MessageTypeAck, dhcpv4.MessageTypeNak)
	if err != nil {
		return err
	}
	if ack.MessageType() == dhcpv4.MessageTypeNak {
		cl.naks++
		r.stats.naks.Add(1)

		return errors.New("NAK") //nolint:err113
	}
	r.stats.add(&r.stats.renewLat, lat)
	r.bind(cl, ack)

	return nil
}

func (r *runner) runClient(ctx context.Context, cl *client, startAfter time.Duration, keepRenewing bool) {
	cl.state = stateWaiting
	select {
	case <-ctx.Done():
		return
	case <-time.After(startAfter):
	}
	cl.state = stateInit
	r.stats.started.Add(1)

	if err := r.dora(ctx, cl); err != nil {
		if ctx.Err() == nil {
			cl.state = stateFailed
			cl.lastErr = err.Error()
			r.stats.failed.Add(1)
		}

		return
	}
	cl.state = stateBound
	if r.stats.bound.Add(1) == r.total {
		r.allBoundAt.Store(int64(time.Since(r.begin)))
	}

	for keepRenewing {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(cl.boundAt.Add(cl.renewAt))):
		}
		if err := r.renew(ctx, cl); err != nil {
			if ctx.Err() != nil {
				return
			}
			cl.renewsFailed++
			cl.lastErr = "renew: " + err.Error()
			r.stats.renewsFailed.Add(1)
			// lease gone: start over, a real client would also rebind first
			if time.Since(cl.boundAt) >= cl.lease {
				if err := r.dora(ctx, cl); err != nil && ctx.Err() == nil {
					cl.lastErr = err.Error()
				}
			}

			continue
		}
		cl.renewsOK++
		r.stats.renewsOK.Add(1)
	}
}

func (r *runner) releaseAll(clients []*client) {
	for _, cl := range clients {
		if cl.ip == nil {
			continue
		}
		xid, err := dhcpv4.GenerateTransactionID()
		if err != nil {
			continue
		}
		msg, err := r.baseMsg(cl, dhcpv4.MessageTypeRelease, xid,
			dhcpv4.WithClientIP(cl.ip), dhcpv4.WithOption(dhcpv4.OptServerIdentifier(cl.serverID)))
		if err != nil {
			continue
		}
		if err := cl.ep.send(cl, msg); err != nil {
			slog.Warn("Release failed", "mac", cl.mac, "err", err)
		}
	}
}

func writeCSV(path string, clients []*client) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	_ = w.Write([]string{"idx", "mac", "hostname", "relay", "circuit_id", "state", "ip", "discover_tries", "request_tries",
		"dora_ms", "renews_ok", "renews_failed", "ip_changes", "naks", "last_err"})
	for _, cl := range clients {
		ip := ""
		if cl.ip != nil {
			ip = cl.ip.String()
		}
		_ = w.Write([]string{
			strconv.Itoa(cl.idx), cl.mac.String(), cl.hostname, cl.ep.name(), cl.subnet.circuitID, string(cl.state), ip,
			strconv.Itoa(cl.discoverTries), strconv.Itoa(cl.requestTries), strconv.FormatInt(cl.dora.Milliseconds(), 10),
			strconv.Itoa(cl.renewsOK), strconv.Itoa(cl.renewsFailed), strconv.Itoa(cl.ipChanges), strconv.Itoa(cl.naks), cl.lastErr,
		})
	}
	w.Flush()

	return w.Error() //nolint:wrapcheck
}

// Run starts all clients, waits until they are done (or Duration elapses) and prints a summary to c.Summary.
func Run(ctx context.Context, c *Config) (*Result, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	w := c.Summary
	if w == nil {
		w = os.Stdout
	}

	st := &stats{}
	r := &runner{cfg: c, stats: st, rnd: rand.New(rand.NewPCG(c.Seed, c.Seed^0x9e3779b97f4a7c15))} //nolint:gosec
	if c.Retries == RetriesRFC {
		r.schedule = []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 64 * time.Second}
	} else {
		r.schedule = []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}
	}

	rctx, rcancel := context.WithCancel(context.WithoutCancel(ctx))
	eps, err := c.openEndpoints(rctx, st)
	defer func() {
		rcancel()
		for _, ep := range eps {
			if ep != nil {
				ep.close()
			}
		}
	}()
	if err != nil {
		return nil, err
	}

	total := c.Servers * c.NICs
	clients := make([]*client, total)
	for i := range total {
		leaf, sub := c.placement(i)
		epIdx := leaf
		if c.Mode == ModeAccess {
			epIdx = sub
		}
		mac := net.HardwareAddr{0x02, 0x4c, 0x54, byte(i >> 16), byte(i >> 8), byte(i)}
		clients[i] = &client{
			idx:      i,
			mac:      mac,
			hostname: fmt.Sprintf("gpu-%04d-nic%d", i/c.NICs, i%c.NICs),
			ep:       eps[epIdx],
			subnet:   c.subnet(sub),
		}
	}

	slog.Info("Starting", "clients", total, "servers", c.Servers, "nics", c.NICs, "leaves", c.Leaves,
		"subnets", c.NumSubnets(), "layout", c.Layout, "ramp", c.Ramp, "retries", c.Retries, "duration", c.Duration)

	runCtx := ctx
	if c.Duration > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, c.Duration)
		defer cancel()
	}

	begin := time.Now()
	r.begin, r.total = begin, int64(total)
	wg := sync.WaitGroup{}
	for _, cl := range clients {
		var offset time.Duration
		if c.Ramp > 0 {
			offset = time.Duration(r.rnd.Int64N(int64(c.Ramp)))
		}
		wg.Go(func() {
			r.runClient(runCtx, cl, offset, c.Duration > 0)
		})
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	tick := time.NewTicker(c.Progress)
	defer tick.Stop()
	report := func() {
		st.m.Lock()
		dora := st.latLine("dora", st.doraLat)
		st.m.Unlock()
		slog.Info("Progress", "t", time.Since(begin).Round(time.Second), "started", st.started.Load(), "bound", st.bound.Load(),
			"failed", st.failed.Load(), "sent", st.sent.Load(), "retx", st.retransmits.Load(), "naks", st.naks.Load(),
			"renewOK", st.renewsOK.Load(), "renewFail", st.renewsFailed.Load(), "late", st.lateReplies.Load())
		slog.Info("Latency " + dora)
	}
loop:
	for {
		select {
		case <-done:
			break loop
		case <-tick.C:
			report()
		}
	}
	report()

	if c.Release {
		r.releaseAll(clients)
	}

	// summary
	st.m.Lock()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "== Summary ==")
	fmt.Fprintf(w, "clients=%d bound=%d failed=%d never_started=%d elapsed=%s\n", total, st.bound.Load(), st.failed.Load(),
		int64(total)-st.started.Load(), time.Since(begin).Round(time.Millisecond))
	if at := r.allBoundAt.Load(); at > 0 {
		fmt.Fprintf(w, "all clients bound after %s\n", time.Duration(at).Round(time.Millisecond))
	}
	fmt.Fprintf(w, "sent=%d retransmits=%d naks=%d late_replies=%d bad_replies=%d\n", st.sent.Load(), st.retransmits.Load(),
		st.naks.Load(), st.lateReplies.Load(), st.badReplies.Load())
	fmt.Fprintf(w, "renews_ok=%d renews_failed=%d ip_changes=%d\n", st.renewsOK.Load(), st.renewsFailed.Load(), st.ipChanges.Load())
	fmt.Fprintln(w, st.latLine("offer", st.offerLat))
	fmt.Fprintln(w, st.latLine("ack", st.ackLat))
	fmt.Fprintln(w, st.latLine("dora", st.doraLat))
	fmt.Fprintln(w, st.latLine("renew", st.renewLat))
	st.m.Unlock()

	dups := 0
	seen := map[string]int{}
	for _, cl := range clients {
		if cl.ip == nil {
			continue
		}
		key := cl.subnet.circuitID + "/" + cl.ip.String()
		if other, ok := seen[key]; ok {
			dups++
			fmt.Fprintf(w, "DUPLICATE IP %s: %s and %s\n", key, clients[other].mac, cl.mac)
		} else {
			seen[key] = cl.idx
		}
	}
	fmt.Fprintf(w, "duplicate_ips=%d\n", dups)

	if c.CSVPath != "" {
		if err := writeCSV(c.CSVPath, clients); err != nil {
			return nil, err
		}
		fmt.Fprintf(w, "per-client results written to %s\n", c.CSVPath)
	}

	res := &Result{
		Clients:      total,
		Bound:        int(st.bound.Load()),
		Failed:       int(st.failed.Load()),
		RenewsFailed: int(st.renewsFailed.Load()),
		DuplicateIPs: dups,
	}
	res.OK = res.DuplicateIPs == 0 && res.Failed == 0 && res.Bound == total && res.RenewsFailed == 0

	return res, nil
}
