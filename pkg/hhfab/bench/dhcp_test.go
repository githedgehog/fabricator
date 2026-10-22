// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/stretchr/testify/require"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	fmeta "go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/api/valid"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A namespace or VPC with a bench name that the bench didn't create stops the
// round before anything is deleted or applied, so it is never taken over and
// later removed by bench clean. The bench's own objects from an earlier run
// are fine.
func TestResetDHCPObjectsRefusesForeignObjects(t *testing.T) {
	t.Parallel()

	plan, err := planDHCP(DHCPLayout{Servers: 4, Subnets: 2, VPCs: 1})
	require.NoError(t, err)

	objs := dhcpObjects(plan, "")

	scheme, err := benchScheme()
	require.NoError(t, err)

	ours := func() kclient.Object {
		return &vpcapi.VPC{ObjectMeta: objMeta("dhcp-0001", map[string]string{LabelFabric: DHCPLabel})}
	}
	require.NoError(t, checkDHCPOwnership(t.Context(), fake.NewClientBuilder().WithScheme(scheme).WithObjects(ours()).Build(), objs))

	for name, existing := range map[string][]kclient.Object{
		// The bench's own VPC would be deleted for the round, but isn't.
		"namespace": {&vpcapi.IPv4Namespace{ObjectMeta: objMeta(DHCPNamespace, nil)}, ours()},
		"vpc":       {&vpcapi.VPC{ObjectMeta: objMeta("dhcp-0001", map[string]string{LabelFabric: "other"})}},
	} {
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing...).Build()

		err := resetDHCPObjects(t.Context(), kube, plan, objs)
		require.ErrorContains(t, err, "isn't the bench's", name)

		left := &vpcapi.VPCList{}
		require.NoError(t, kube.List(t.Context(), left))
		require.Len(t, left.Items, 1, "%s: nothing deleted", name)
	}
}

// Reading dhcpd's record after the storm waits out a struggling apiserver
// rather than losing the round.
func TestStoredLeasesRetries(t *testing.T) {
	t.Parallel()

	plan, err := planDHCP(DHCPLayout{Servers: 2, Subnets: 1, VPCs: 1})
	require.NoError(t, err)

	scheme, err := benchScheme()
	require.NoError(t, err)

	stored := &dhcpapi.DHCPSubnet{
		ObjectMeta: objMeta(plan[0].dhcpSubnetName(), nil),
		Status:     dhcpapi.DHCPSubnetStatus{Allocated: map[string]dhcpapi.DHCPAllocated{"02:dc:00:00:00:00": {IP: "10.0.0.2"}}},
	}

	synctest.Test(t, func(t *testing.T) {
		fails := 2
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, kube kclient.WithWatch, key kclient.ObjectKey, obj kclient.Object, opts ...kclient.GetOption) error {
				if fails > 0 {
					fails--

					return errors.New("read: connection reset by peer") //nolint:err113
				}

				return kube.Get(ctx, key, obj, opts...)
			},
		}).Build()

		start := time.Now()
		got, err := storedLeases(t.Context(), kube, plan)
		require.NoError(t, err)
		require.Equal(t, 2*time.Second, time.Since(start), "retried every second")
		require.Equal(t, "10.0.0.2", got[0]["02:dc:00:00:00:00"].IP)
	})

	// Still unreadable once the wait is over: the checks against it didn't run,
	// and the round must not pass as clean.
	synctest.Test(t, func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, kclient.WithWatch, kclient.ObjectKey, kclient.Object, ...kclient.GetOption) error {
				return errors.New("read: connection reset by peer") //nolint:err113
			},
		}).Build()

		checks := newDHCPChecks()
		checkStoredLeases(t.Context(), kube, checks, plan, newDHCPServers(plan))

		require.Equal(t, 1, checks.failed())
		require.Equal(t, int64(1), checks.count(checkLeasesUnread))
	})
}

// Cleanup waits only for the DHCPSubnets of the bench's own VPCs: another
// VPC's, however it is named, never holds it up.
func TestWaitDHCPGoneOnlyWaitsForOurs(t *testing.T) {
	t.Parallel()

	scheme, err := benchScheme()
	require.NoError(t, err)

	ours := map[string]bool{"dhcp-0001": true}
	subnet := func(name string) kclient.Object {
		return &dhcpapi.DHCPSubnet{ObjectMeta: objMeta(name, nil)}
	}

	synctest.Test(t, func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(subnet("dhcp-prod--s01")).Build()

		start := time.Now()
		require.NoError(t, waitDHCPGone(t.Context(), kube, ours))
		require.Zero(t, time.Since(start))
	})

	synctest.Test(t, func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(subnet("dhcp-0001--s01")).Build()

		require.ErrorIs(t, waitDHCPGone(t.Context(), kube, ours), context.DeadlineExceeded)
	})
}

// The default fleet is 20k servers in subnets of at most 1000: 10 VPCs of 2
// subnets of 1000 servers, /22s each with room for the servers and the probe,
// on their own VLANs, carved in order and never overlapping.
func TestPlanDHCPDefaults(t *testing.T) {
	t.Parallel()

	plan, err := planDHCP(DHCPLayout{
		Servers: DefaultDHCPServers, Subnets: DefaultDHCPSubnets, VPCs: DefaultDHCPVPCs, MaxSubnetServers: DefaultDHCPMaxSubnetServers,
	})
	require.NoError(t, err)
	require.Len(t, plan, 20)
	require.Equal(t, "1000", dhcpServersPerSubnet(plan))

	first := plan[0]
	require.Equal(t, "dhcp-0001", first.vpc)
	require.Equal(t, "s01", first.name)
	require.Equal(t, "dhcp-0001--s01", first.dhcpSubnetName())
	require.Equal(t, uint16(DHCPVLANFrom), first.vlan)
	require.Equal(t, "10.0.0.0/22", first.prefix.String())
	require.Equal(t, "10.0.0.1", first.gateway.String())
	require.Equal(t, "10.0.0.2", first.start.String())
	require.Equal(t, "10.0.3.254", first.end.String())

	last := plan[len(plan)-1]
	require.Equal(t, "dhcp-0010", last.vpc)
	require.Equal(t, "s02", last.name)
	require.Equal(t, uint16(DHCPVLANFrom+19), last.vlan)
	require.Equal(t, DefaultDHCPServers, last.first+last.servers)

	for idx, sub := range plan {
		require.Equal(t, idx, sub.index)
		require.Equal(t, idx*1000, sub.first)
		require.GreaterOrEqual(t, rangeSize(sub), sub.servers+1, "room for every server and the probe in %s", sub.dhcpSubnetName())
		require.False(t, sub.usable(sub.prefix.Addr()), "network address")
		require.False(t, sub.usable(sub.gateway), "gateway")

		if idx > 0 {
			require.False(t, sub.prefix.Overlaps(plan[idx-1].prefix))
		}
	}
}

func rangeSize(sub *dhcpSubnet) int {
	n := 1
	for ip := sub.start; ip != sub.end; ip = ip.Next() {
		n++
	}

	return n
}

// The prefix grows exactly when the servers plus network, gateway, broadcast and
// probe no longer fit.
func TestPlanDHCPPrefixSizing(t *testing.T) {
	t.Parallel()

	for servers, bits := range map[int]int{1: 29, 4: 29, 5: 28, 1020: 22, 1021: 21, 8000: 19} {
		plan, err := planDHCP(DHCPLayout{VPCs: 1, Subnets: 1, Servers: servers})
		require.NoError(t, err)
		require.Equal(t, bits, plan[0].prefix.Bits(), "%d servers", servers)
		require.GreaterOrEqual(t, rangeSize(plan[0]), servers+1, "%d servers", servers)
	}
}

// Without --vpcs there are just enough VPCs for the max servers per subnet,
// and servers that don't divide evenly go one more to the first subnets.
func TestPlanDHCPSpreadsServers(t *testing.T) {
	t.Parallel()

	plan, err := planDHCP(DHCPLayout{Servers: 2500, Subnets: 2, MaxSubnetServers: 500})
	require.NoError(t, err)
	require.Len(t, plan, 6)
	require.Equal(t, "dhcp-0003", plan[5].vpc)
	require.Equal(t, "416-417", dhcpServersPerSubnet(plan))

	first := 0
	for idx, sub := range plan {
		want := 416
		if idx < 4 {
			want = 417
		}

		require.Equal(t, want, sub.servers, sub.dhcpSubnetName())
		require.Equal(t, first, sub.first, sub.dhcpSubnetName())
		first += sub.servers
	}
	require.Equal(t, 2500, first)

	servers := newDHCPServers(plan)
	require.Len(t, servers, 2500)
	for idx, srv := range servers {
		require.Equal(t, idx, srv.idx)
	}

	// --vpcs wins over the max.
	plan, err = planDHCP(DHCPLayout{Servers: 2500, Subnets: 2, VPCs: 1, MaxSubnetServers: 500})
	require.NoError(t, err)
	require.Len(t, plan, 2)
	require.Equal(t, "1250", dhcpServersPerSubnet(plan))
}

func TestPlanDHCPRefusesWhatCannotExist(t *testing.T) {
	t.Parallel()

	for name, layout := range map[string]DHCPLayout{
		"no vpcs nor max":            {Servers: 1, Subnets: 1},
		"negative vpcs":              {Servers: 1, Subnets: 1, VPCs: -1},
		"too many subnets":           {Servers: 100, Subnets: MaxDHCPSubnetsPerVPC + 1, VPCs: 1},
		"no servers":                 {Servers: 0, Subnets: 1, VPCs: 1},
		"fewer servers than subnets": {Servers: 1, Subnets: 2, MaxSubnetServers: 1000},
		"out of VLANs":               {Servers: 3000, Subnets: 20, VPCs: 101},
		"out of addresses":           {Servers: 16_000_000, Subnets: 20, VPCs: 10},
		"too many servers":           {Servers: maxDHCPServers + 1, Subnets: 1, VPCs: 1},
		"too many vpc names":         {Servers: 20_000, Subnets: 1, MaxSubnetServers: 1},
	} {
		_, err := planDHCP(layout)
		require.Error(t, err, name)
	}
}

// The bench's own objects pass the admission webhooks' defaulting and
// validation against Fabric/default, with and without a PXE URL.
func TestDHCPObjectsValidate(t *testing.T) {
	t.Parallel()

	for _, pxeURL := range []string{"", "http://172.30.0.1/bench/pxe.efi"} {
		ctx := t.Context()

		f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}
		f.Default()

		cfg, err := fabric.GetFabricConfig(f)
		require.NoError(t, err)

		l := apiutil.NewLoader()
		require.NoError(t, l.Add(ctx, &wiringapi.Fabric{
			TypeMeta:   kmetav1.TypeMeta{Kind: wiringapi.KindFabric, APIVersion: wiringapi.GroupVersion.String()},
			ObjectMeta: kmetav1.ObjectMeta{Name: wiringapi.DefaultFabric, Namespace: kmetav1.NamespaceDefault},
			Spec:       wiringapi.DefaultFabricSpec(cfg),
		}))

		plan, err := planDHCP(DHCPLayout{Servers: 6000, Subnets: MaxDHCPSubnetsPerVPC, VPCs: 3})
		require.NoError(t, err)

		for _, obj := range dhcpObjects(plan, pxeURL) {
			if defaulted, ok := obj.(interface{ Default() }); ok {
				defaulted.Default()
			}
			require.NoError(t, l.Add(ctx, obj))
		}

		cfg, err = cfg.Init(fmeta.ExtraValidators{Peering: valid.Peering})
		require.NoError(t, err)
		require.NoError(t, apiutil.ValidateFabricGateway(ctx, l, cfg), "pxe %q", pxeURL)
	}
}

// A relayed request carries what dhcpd keys the subnet on, in the encoding it
// strips: circuit-id as is, and the VSS sub-option as a type byte then VrfV and
// the VRF.
func TestDHCPMessageCarriesRelayInfo(t *testing.T) {
	t.Parallel()

	sub := &dhcpSubnet{vpc: "dhcp-0001", name: "s01", vrf: "dhcp-0001", circuitID: "vlan1000"}
	srv := &dhcpServer{sub: sub, mac: dhcpServerMAC(7), hostname: dhcpHostname(sub, 7)}
	c := &dhcpClients{relay: net.ParseIP("172.30.0.38")}

	msg, err := c.message(srv, dhcpv4.MessageTypeDiscover, dhcpv4.TransactionID{1, 2, 3, 4}, false)
	require.NoError(t, err)

	parsed, err := dhcpv4.FromBytes(msg.ToBytes())
	require.NoError(t, err)

	require.Equal(t, "172.30.0.38", parsed.GatewayIPAddr.String())
	require.Equal(t, uint8(1), parsed.HopCount)
	require.Equal(t, "02:dc:00:00:00:07", parsed.ClientHWAddr.String())
	require.Equal(t, "dhcp-0001-s01-00008", parsed.HostName())

	rai := parsed.RelayAgentInfo()
	require.NotNil(t, rai)
	require.Equal(t, "vlan1000", string(rai.Get(dhcpv4.AgentCircuitIDSubOption)))

	// What dhcpd does with it (pkg/dhcp/handlers.go): lowercase, trim, drop the
	// type byte, drop the vrfv prefix.
	vss := strings.ToLower(strings.TrimSpace(string(rai.Get(dhcpv4.VirtualSubnetSelectionSubOption))))
	require.Equal(t, "dhcp-0001", strings.TrimPrefix(vss[1:], "vrfv"))

	pxe, err := c.message(srv, dhcpv4.MessageTypeDiscover, dhcpv4.TransactionID{1, 2, 3, 5}, true)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(pxe.ClassIdentifier(), "PXEClient:"))
	require.True(t, pxe.IsOptionRequested(dhcpv4.OptionBootfileName))
}

// fakeDHCPD answers like a correct dhcpd, with switches to misbehave the ways
// the checks look for.
type fakeDHCPD struct {
	demux    *dhcpDemux
	plan     []*dhcpSubnet
	serverID net.IP

	mu     sync.Mutex
	leases map[string]netip.Addr      // MAC → IP
	offers map[string]netip.Addr      // MAC → IP offered, not yet requested
	owners map[netip.Addr]string      // IP → MAC
	next   map[*dhcpSubnet]netip.Addr // next free IP

	silent     bool            // never answer, like an unreachable dhcpd
	deaf       map[string]bool // never answer these MACs
	zeroFirst  bool            // answer each MAC's first DISCOVER with 0.0.0.0
	nakFirst   int             // NAK each MAC's first so many REQUESTs
	deafFirst  int             // ignore each MAC's first so many REQUESTs
	wrongAck   bool            // ACK each MAC's first REQUEST with another IP than offered
	nakRenewal bool            // NAK each MAC's first renewal
	zeroAck    bool            // answer each MAC's first REQUEST with an ACK for 0.0.0.0
	zeroRepeat bool            // answer each MAC's first repeat with an ACK for 0.0.0.0
	deafRepeat bool            // never answer a repeat
	grantTaken bool            // ACK a REQUEST for another MAC's IP
	ackOwn     bool            // ACK a REQUEST for another MAC's IP with the requester's own, like today's dhcpd
	ackStray   bool            // ACK a REQUEST for another MAC's IP with a free one
	shareIP    bool            // hand every server in a subnet the same IP
	discovers  map[string]int
	requests   map[string]int
	renewals   map[string]int
}

func newFakeDHCPD(demux *dhcpDemux, plan []*dhcpSubnet) *fakeDHCPD {
	f := &fakeDHCPD{
		demux:     demux,
		plan:      plan,
		serverID:  net.ParseIP("172.30.0.1"),
		leases:    map[string]netip.Addr{},
		offers:    map[string]netip.Addr{},
		owners:    map[netip.Addr]string{},
		next:      map[*dhcpSubnet]netip.Addr{},
		discovers: map[string]int{},
		requests:  map[string]int{},
		renewals:  map[string]int{},
	}
	for _, sub := range plan {
		f.next[sub] = sub.start
	}

	return f
}

func (f *fakeDHCPD) subnet(msg *dhcpv4.DHCPv4) *dhcpSubnet {
	circuit := string(msg.RelayAgentInfo().Get(dhcpv4.AgentCircuitIDSubOption))
	for _, sub := range f.plan {
		if sub.circuitID == circuit {
			return sub
		}
	}

	return nil
}

func (f *fakeDHCPD) send(msg *dhcpv4.DHCPv4) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	mac := msg.ClientHWAddr.String()
	if f.silent || f.deaf[mac] {
		return nil
	}

	sub := f.subnet(msg)

	reply := func(mt dhcpv4.MessageType, ip netip.Addr, lease time.Duration) {
		mods := []dhcpv4.Modifier{dhcpv4.WithMessageType(mt)}
		switch {
		case ip.IsValid():
			mods = append(mods,
				dhcpv4.WithYourIP(ip.AsSlice()),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(f.serverID)),
				dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(lease)))
		case mt == dhcpv4.MessageTypeNak:
			// RFC 2131 Table 3: a NAK carries the server identifier too.
			mods = append(mods, dhcpv4.WithOption(dhcpv4.OptServerIdentifier(f.serverID)))
		}

		resp, err := dhcpv4.NewReplyFromRequest(msg, mods...)
		if err != nil {
			panic(err)
		}

		f.demux.deliver(resp, netip.MustParseAddr("172.30.0.5"))
	}

	switch msg.MessageType() { //nolint:exhaustive // a fake answers what the bench sends
	case dhcpv4.MessageTypeDiscover:
		f.discovers[mac]++
		if f.zeroFirst && f.discovers[mac] == 1 {
			reply(dhcpv4.MessageTypeOffer, netip.Addr{}, 0)

			return nil
		}

		ip, ok := f.leases[mac]
		if !ok {
			ip, ok = f.offers[mac]
		}
		if !ok {
			ip = f.next[sub]
			if !f.shareIP {
				f.next[sub] = ip.Next()
			}
			f.offers[mac] = ip
		}

		reply(dhcpv4.MessageTypeOffer, ip, 10*time.Second)
	case dhcpv4.MessageTypeRequest:
		f.requests[mac]++

		if ciaddr, _ := netip.AddrFromSlice(msg.ClientIPAddr.To4()); ciaddr.IsValid() && !ciaddr.IsUnspecified() {
			f.renewals[mac]++
			if f.nakRenewal && f.renewals[mac] == 1 {
				reply(dhcpv4.MessageTypeNak, netip.Addr{}, 0)

				return nil
			}

			reply(dhcpv4.MessageTypeAck, ciaddr, time.Hour)

			return nil
		}

		// The DORA's REQUEST is the first, the renewal the second, the repeats
		// come after.
		if (f.zeroAck && f.requests[mac] == 1) || (f.zeroRepeat && f.requests[mac] == 3) {
			reply(dhcpv4.MessageTypeAck, netip.Addr{}, 0)

			return nil
		}

		if f.requests[mac] <= f.deafFirst || (f.deafRepeat && f.requests[mac] >= 3) {
			return nil
		}

		if f.wrongAck && f.requests[mac] == 1 {
			reply(dhcpv4.MessageTypeAck, f.next[sub], 10*time.Second)
			f.next[sub] = f.next[sub].Next()

			return nil
		}

		if f.requests[mac] <= f.nakFirst {
			reply(dhcpv4.MessageTypeNak, netip.Addr{}, 0)

			return nil
		}

		want, _ := netip.AddrFromSlice(msg.RequestedIPAddress().To4())
		if owner, ok := f.owners[want]; ok && owner != mac && !f.grantTaken && !f.shareIP {
			switch {
			case f.ackOwn:
				reply(dhcpv4.MessageTypeAck, f.leases[mac], 10*time.Second)
			case f.ackStray:
				reply(dhcpv4.MessageTypeAck, f.next[sub], 10*time.Second)
				f.next[sub] = f.next[sub].Next()
			default:
				reply(dhcpv4.MessageTypeNak, netip.Addr{}, 0)
			}

			return nil
		}

		f.leases[mac] = want
		f.owners[want] = mac
		reply(dhcpv4.MessageTypeAck, want, 10*time.Second)
	}

	return nil
}

// storm runs a round's storm against the fake on a fake clock.
func storm(t *testing.T, layout DHCPLayout, opts DHCPOpts, setup func(*fakeDHCPD)) (*dhcpRoundResult, []*dhcpServer, *fakeDHCPD) {
	t.Helper()

	plan, err := planDHCP(layout)
	require.NoError(t, err)

	for _, sub := range plan {
		sub.vrf, sub.circuitID = sub.vpc, fmt.Sprintf("vlan%d", sub.vlan)
	}

	demux := newDHCPDemux()
	fake := newFakeDHCPD(demux, plan)
	if setup != nil {
		setup(fake)
	}

	stats := newDHCPRoundStats(0, 1, layout.Servers)
	checks := newDHCPChecks()
	clients := &dhcpClients{
		send: fake.send, demux: demux, relay: net.ParseIP("172.30.0.38"), stats: stats, checks: checks,
		osSched: osSchedule, pxeSched: pxeSchedule, repeatSched: repeatSchedule,
	}

	opts.Layout = layout
	if opts.RoundTimeout == 0 {
		opts.RoundTimeout = time.Hour
	}

	held := newDHCPHeld(plan)
	servers := newDHCPServers(plan)
	took := runDHCPStorm(t.Context(), clients, servers, held, opts, stats)

	return stats.result(0, took, checks, servers), servers, fake
}

func failedChecks(res *dhcpRoundResult) map[checkName]int64 {
	out := map[checkName]int64{}
	for _, name := range checkOrder {
		if n := res.checks.count(name); n > 0 {
			out[name] = n
		}
	}

	return out
}

// Against a correct dhcpd every server binds, renews, gets its own IP back and
// a NAK for another's, and nothing is flagged.
func TestDHCPStormClean(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, servers, _ := storm(t, DHCPLayout{VPCs: 2, Subnets: 2, Servers: 20}, DHCPOpts{Repeats: 4, RepeatInterval: time.Second}, nil)

		require.Equal(t, int64(20), res.bound)
		require.Zero(t, res.timedOut)
		require.Empty(t, failedChecks(res))
		require.Len(t, res.doraLat, 20)
		require.Len(t, res.renewLat, 20*3, "the first renewal and one after each own repeat's ACK")

		require.Equal(t, int64(40), res.repeats[repeatOwn][outcomeAckSame])
		require.Equal(t, int64(40), res.repeats[repeatTaken][outcomeNak])

		for _, srv := range servers {
			require.True(t, srv.sub.usable(srv.ip), "%s got %s", srv.hostname, srv.ip)
			require.False(t, srv.leaseEnd.IsZero())
			require.Zero(t, srv.retransmits)
		}
	})
}

// A 0.0.0.0 offer is counted and ignored; the client retransmits on its
// schedule and binds on the next try.
func TestDHCPStormZeroReplyRetransmits(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, servers, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{}, func(f *fakeDHCPD) { f.zeroFirst = true })

		require.Equal(t, int64(3), res.bound)
		require.Equal(t, int64(3), res.zeroOffer)
		require.Zero(t, res.zeroAck)
		require.Equal(t, map[checkName]int64{checkZeroOffer: 3}, failedChecks(res))

		for _, srv := range servers {
			require.Equal(t, 1, srv.retransmits)
		}
	})
}

// A 0.0.0.0 ACK is counted apart from a 0.0.0.0 offer; the client keeps
// retransmitting its REQUEST and binds on the next answer.
func TestDHCPStormZeroAckRetransmits(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, servers, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{}, func(f *fakeDHCPD) { f.zeroAck = true })

		require.Equal(t, int64(3), res.bound)
		require.Zero(t, res.zeroOffer)
		require.Equal(t, int64(3), res.zeroAck)
		require.Equal(t, map[checkName]int64{checkZeroAck: 3}, failedChecks(res))

		for _, srv := range servers {
			require.Equal(t, 1, srv.retransmits)
		}
	})
}

// A NAK for the IP dhcpd has just offered is flagged, and sends the client back
// to DISCOVER.
func TestDHCPStormNakRestarts(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, _, fake := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{}, func(f *fakeDHCPD) { f.nakFirst = 1 })

		require.Equal(t, int64(3), res.bound)
		require.Equal(t, int64(3), res.nakDORA)
		require.Equal(t, map[checkName]int64{checkOfferNaked: 3}, failedChecks(res))

		for mac, n := range fake.discovers {
			require.Equal(t, 2, n, "%s started over once", mac)
		}
	})

	// Like systemd, a client NAKed again and again waits before each new
	// start, twice as long each time, instead of coming straight back.
	synctest.Test(t, func(t *testing.T) {
		res, _, fake := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{}, func(f *fakeDHCPD) { f.nakFirst = 3 })

		require.Equal(t, int64(3), res.bound)
		require.Equal(t, int64(9), res.nakDORA)
		for _, lat := range res.doraLat {
			require.Equal(t, (1+2+4)*time.Second, lat)
		}
		for mac, n := range fake.discovers {
			require.Equal(t, 4, n, "%s started over three times", mac)
		}
	})
}

// RFC 2131 §4.4.1: a REQUEST that gets neither an ACK nor a NAK after the
// retransmits sends the client back to INIT, as systemd does after five; it
// doesn't retry the same REQUEST for the rest of the round. The restart is
// counted, not flagged: an overloaded dhcpd may drop requests.
func TestDHCPStormUnansweredRequestRestarts(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, servers, fake := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{}, func(f *fakeDHCPD) { f.deafFirst = dhcpMaxRequestAttempts })

		require.Equal(t, int64(3), res.bound)
		require.Equal(t, int64(3), res.restarts)
		require.Empty(t, failedChecks(res))
		for mac, n := range fake.discovers {
			require.Equal(t, 2, n, "%s started over once", mac)
		}
		for _, srv := range servers {
			require.Equal(t, dhcpMaxRequestAttempts-1, srv.retransmits)
		}
	})
}

// EDK2 ends its PXE session on a NAK and on an ACK for another IP than it
// was offered, and the server is out for the round like one whose firmware
// gave up waiting.
func TestDHCPStormPXEAborts(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		setup func(*fakeDHCPD)
		check checkName
	}{
		"nak":      {func(f *fakeDHCPD) { f.nakFirst = 1 }, checkOfferNaked},
		"wrong ip": {func(f *fakeDHCPD) { f.wrongAck = true }, checkWrongIP},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				res, servers, fake := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 2}, DHCPOpts{PXE: true}, tc.setup)

				require.Equal(t, int64(2), res.pxeDead)
				require.Zero(t, res.bound)
				require.Equal(t, map[checkName]int64{tc.check: 2, checkUnbound: 2}, failedChecks(res))
				for _, srv := range servers {
					require.True(t, srv.pxeDead)
				}
				for mac, n := range fake.discovers {
					require.Equal(t, 1, n, "%s didn't start over", mac)
				}
			})
		})
	}
}

// Granting another server's IP to a taken repeat is the misbehaviour; handing
// two servers the same IP is caught as it happens.
func TestDHCPStormFlagsTakenAndShared(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 4}, DHCPOpts{Repeats: 2, RepeatInterval: time.Second},
			func(f *fakeDHCPD) { f.grantTaken = true })

		require.Equal(t, int64(4), res.repeats[repeatTaken][outcomeAckSame])
		require.Equal(t, int64(4), failedChecks(res)[checkTakenGranted])
	})

	synctest.Test(t, func(t *testing.T) {
		res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{}, func(f *fakeDHCPD) { f.shareIP = true })

		require.Equal(t, int64(2), failedChecks(res)[checkOverlap])
	})
}

// Waiting out the apiserver: errors it fails to serve with are retried until the
// check is done, while a rejection ends the wait at once.
func TestPollUntilRetriesUnavailable(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		calls := 0
		err := pollUntil(t.Context(), time.Minute, "test", func(context.Context) (bool, error) {
			calls++
			if calls <= 3 {
				return false, errors.New("read: connection reset by peer") //nolint:err113
			}

			return true, nil
		})

		require.NoError(t, err)
		require.Equal(t, 4, calls)
	})

	synctest.Test(t, func(t *testing.T) {
		calls := 0
		err := pollUntil(t.Context(), time.Minute, "test", func(context.Context) (bool, error) {
			calls++

			return false, kapierrors.NewBadRequest("no")
		})

		require.True(t, kapierrors.IsBadRequest(err))
		require.Equal(t, 1, calls)
	})

	synctest.Test(t, func(t *testing.T) {
		err := pollUntil(t.Context(), 5*time.Second, "test", func(context.Context) (bool, error) {
			return false, errors.New("read: connection reset by peer") //nolint:err113
		})

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorContains(t, err, "connection reset by peer")
	})

	// A request that hangs ends with the timeout rather than outlasting it.
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		err := pollUntil(t.Context(), 5*time.Second, "test", func(ctx context.Context) (bool, error) {
			<-ctx.Done()

			return false, ctx.Err()
		})

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, 5*time.Second, time.Since(start))
	})
}

// A taken repeat asks for an IP another server holds, and RFC 2131 wants a NAK
// for it. An ACK for any other IP is flagged, whether it is the asking server's
// own IP, as today's dhcpd answers, or a free one.
func TestDHCPStormTakenNotNaked(t *testing.T) {
	t.Parallel()

	for name, setup := range map[string]func(*fakeDHCPD){
		"own":   func(f *fakeDHCPD) { f.ackOwn = true },
		"stray": func(f *fakeDHCPD) { f.ackStray = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 4}, DHCPOpts{Repeats: 2, RepeatInterval: time.Second}, setup)

				require.Equal(t, int64(4), res.repeats[repeatTaken][outcomeAckOther])
				require.Equal(t, map[checkName]int64{checkTakenNotNaked: 4}, failedChecks(res))
			})
		})
	}
}

// A NAK for a lease dhcpd has just ACKed is flagged; the client starts over and
// binds. Each DORA is measured on its own, not from the first one's start.
func TestDHCPStormRenewalNaked(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{}, func(f *fakeDHCPD) { f.nakRenewal = true })

		require.Equal(t, int64(3), res.bound)
		require.Equal(t, int64(3), res.nakRenewal)
		require.Equal(t, map[checkName]int64{checkLeaseNaked: 3}, failedChecks(res))

		require.Len(t, res.doraLat, 6)
		for _, lat := range res.doraLat {
			require.Zero(t, lat, "a DORA answered at once")
		}
	})
}

// A server alone in its subnet has no other server's IP to ask for, so it
// skips the taken repeats instead of waiting the round out for one.
func TestDHCPStormLoneServer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 2, Servers: 2}, DHCPOpts{Repeats: 4, RepeatInterval: time.Second}, nil)

		require.Less(t, time.Since(start), time.Minute)
		require.Equal(t, int64(2), res.bound)
		require.Equal(t, int64(4), res.repeats[repeatOwn][outcomeAckSame])
		require.Equal(t, [repeatOutcomes]int64{}, res.repeats[repeatTaken])
		require.Empty(t, failedChecks(res))
	})
}

// When the only other server in a subnet gives up in PXE, no one is left who
// could hold an IP to ask for, so the taken repeats are skipped once it does
// instead of waiting the round out.
func TestDHCPStormTakenWithoutPeers(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		res, servers, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 2}, DHCPOpts{PXE: true, Repeats: 4, RepeatInterval: time.Second},
			func(f *fakeDHCPD) { f.deaf = map[string]bool{dhcpServerMAC(1).String(): true} })

		require.Less(t, time.Since(start), 5*time.Minute)
		require.True(t, servers[1].pxeDead)
		require.Equal(t, int64(1), res.bound)
		require.Equal(t, int64(2), res.repeats[repeatOwn][outcomeAckSame])
		require.Equal(t, [repeatOutcomes]int64{}, res.repeats[repeatTaken])
		require.Equal(t, map[checkName]int64{checkUnbound: 1}, failedChecks(res))
	})
}

// dhcpd holds a lease for a server that repeats its REQUEST, so RFC 2131
// 4.3.2 wants an answer, ACK or NAK; silence through every retransmit is
// flagged, not just counted.
func TestDHCPStormRepeatUnanswered(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{Repeats: 2, RepeatInterval: time.Second},
			func(f *fakeDHCPD) { f.deafRepeat = true })

		require.Equal(t, int64(3), res.bound)
		require.Equal(t, int64(3), res.repeats[repeatOwn][outcomeNoReply])
		require.Equal(t, int64(3), res.repeats[repeatTaken][outcomeNoReply])
		require.Equal(t, map[checkName]int64{checkRepeatUnanswered: 6}, failedChecks(res))
	})
}

// The lease a server holds is the one in the newest ACK: an own repeat ACKed
// with dhcpd's 10s first lease is renewed at its T1, as a rebooted client
// does, so the server ends up with the renewal's hour rather than a lease that
// ran out while the bench still counted the IP as held.
func TestDHCPStormLeaseFollowsNewestAck(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, servers, fake := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{Repeats: 1, RepeatInterval: time.Second}, nil)

		require.Equal(t, int64(3), res.repeats[repeatOwn][outcomeAckSame])
		require.Empty(t, failedChecks(res))
		for _, srv := range servers {
			require.Equal(t, 2, fake.renewals[srv.mac.String()], "the first renewal and one after the repeat")
			require.WithinDuration(t, time.Now().Add(time.Hour), srv.leaseEnd, 2*time.Second, srv.hostname)
		}
	})
}

// Repeat latency is how fast dhcpd answered: a repeat that got nothing or only
// 0.0.0.0 is counted but has no latency, since all it measured is its own
// retransmits.
func TestRepeatLatencyOnlyForAnswers(t *testing.T) {
	t.Parallel()

	stats := newDHCPRoundStats(0, 1, 1)
	stats.repeat(repeatOwn, outcomeAckSame, time.Millisecond)
	stats.repeat(repeatOwn, outcomeZero, 28*time.Second)
	stats.repeat(repeatOwn, outcomeNoReply, 28*time.Second)
	stats.repeat(repeatTaken, outcomeNak, 2*time.Millisecond)

	require.Equal(t, [repeatOutcomes]int64{outcomeAckSame: 1, outcomeZero: 1, outcomeNoReply: 1}, stats.repeats[repeatOwn])
	require.Equal(t, []time.Duration{time.Millisecond}, stats.repeatLat[repeatOwn])
	require.Equal(t, []time.Duration{2 * time.Millisecond}, stats.repeatLat[repeatTaken])
}

// A 0.0.0.0 ACK to a repeat is flagged when it comes, even though the
// retransmit then gets the right answer.
func TestDHCPStormZeroAckRepeat(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 3}, DHCPOpts{Repeats: 1, RepeatInterval: time.Second},
			func(f *fakeDHCPD) { f.zeroRepeat = true })

		require.Equal(t, int64(3), res.zeroAck)
		require.Equal(t, int64(3), res.repeats[repeatOwn][outcomeAckSame])
		require.Equal(t, map[checkName]int64{checkZeroAck: 3}, failedChecks(res))
	})
}

// PXE firmware that never hears back gives up after 4, 8, 16 and 32 s and
// stops for the round; nothing else of the server runs.
func TestDHCPStormPXEGivesUp(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		res, servers, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 2}, DHCPOpts{PXE: true}, func(f *fakeDHCPD) { f.silent = true })

		require.Equal(t, 60*time.Second, time.Since(start))
		require.Equal(t, int64(2), res.pxeDead)
		require.Zero(t, res.bound)
		require.Zero(t, res.timedOut, "dead in PXE is not timed out")
		require.Equal(t, int64(2), failedChecks(res)[checkUnbound])

		for _, srv := range servers {
			require.True(t, srv.pxeDead)
			require.Equal(t, 3, srv.retransmits)
		}
	})
}

// With PXE the firmware's DORA comes first and dwells for its first timeout
// before requesting, then the OS binds as usual.
func TestDHCPStormPXEThenOS(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, _, fake := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 2}, DHCPOpts{PXE: true}, nil)

		require.Equal(t, int64(2), res.bound)
		require.Len(t, res.pxeLat, 2)
		for _, lat := range res.pxeLat {
			require.Equal(t, 4*time.Second, lat, "EDK2 waits out its first timeout before requesting")
		}
		require.Empty(t, failedChecks(res))

		for mac, n := range fake.discovers {
			require.Equal(t, 2, n, "%s: one DISCOVER from the firmware, one from the OS", mac)
		}
	})
}

// A server that never hears back runs out of round and is counted unbound.
func TestDHCPStormRoundTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		res, _, _ := storm(t, DHCPLayout{VPCs: 1, Subnets: 1, Servers: 2}, DHCPOpts{RoundTimeout: 5 * time.Minute},
			func(f *fakeDHCPD) { f.silent = true })

		require.Zero(t, res.bound)
		require.Equal(t, 2, res.timedOut)
		require.Equal(t, int64(2), failedChecks(res)[checkUnbound])
	})
}

// A reply for another MAC is never taken.
func TestDHCPExchangeIgnoresMismatchedReply(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		demux := newDHCPDemux()
		checks := newDHCPChecks()
		c := &dhcpClients{demux: demux, checks: checks, stats: newDHCPRoundStats(0, 1, 1)}

		srv := &dhcpServer{mac: dhcpServerMAC(1), hostname: "a"}
		other := dhcpServerMAC(2)

		xid, replies := demux.open()
		c.send = func(msg *dhcpv4.DHCPv4) error {
			wrong, err := dhcpv4.New(dhcpv4.WithTransactionID(xid), dhcpv4.WithHwAddr(other), dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer))
			require.NoError(t, err)
			demux.deliver(wrong, netip.Addr{})

			return nil
		}

		msg, err := dhcpv4.New(dhcpv4.WithTransactionID(xid), dhcpv4.WithHwAddr(srv.mac))
		require.NoError(t, err)

		_, err = c.exchange(t.Context(), srv, msg, replies, repeatSchedule, false, func(*dhcpv4.DHCPv4) verdict { return take })
		require.ErrorIs(t, err, errNoReply)
		require.Equal(t, int64(3), checks.count(checkMismatchedReply))
	})
}

// The taken-IP picker only ever returns an IP another server in the same
// subnet holds, and waits while there is none.
func TestDHCPHeldPicksOthersOnly(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		plan, err := planDHCP(DHCPLayout{VPCs: 1, Subnets: 2, Servers: 6})
		require.NoError(t, err)

		held := newDHCPHeld(plan)
		servers := newDHCPServers(plan)

		mine := servers[0]
		mine.ip = plan[0].start
		require.Nil(t, held.hold(mine))

		// Another subnet's server holds an IP; still nothing for mine.
		elsewhere := servers[3]
		elsewhere.ip = plan[1].start
		require.Nil(t, held.hold(elsewhere))

		got := make(chan netip.Addr)
		go func() {
			ip, ok := held.pickOther(t.Context(), mine)
			require.True(t, ok)
			got <- ip
		}()

		synctest.Wait()
		select {
		case <-got:
			t.Fatal("picked while no other server in the subnet held an IP")
		default:
		}

		neighbour := servers[1]
		neighbour.ip = plan[0].start.Next()
		require.Nil(t, held.hold(neighbour))
		require.Equal(t, neighbour.ip, <-got)

		for range 20 {
			ip, ok := held.pickOther(t.Context(), mine)
			require.True(t, ok)
			require.Equal(t, neighbour.ip, ip)
		}

		// Two servers on one IP: the second is reported, the first keeps it.
		dup := servers[2]
		dup.ip = neighbour.ip
		require.Equal(t, neighbour, held.hold(dup))
	})
}

// The stored-state checks fire on what they are for and on nothing else; the
// probe's leftover offer is expected.
func TestCheckStored(t *testing.T) {
	t.Parallel()

	plan, err := planDHCP(DHCPLayout{VPCs: 1, Subnets: 1, Servers: 4})
	require.NoError(t, err)

	sub := plan[0]
	servers := newDHCPServers(plan)
	now := time.Now()

	ip := sub.start
	for _, srv := range servers {
		srv.ip = ip
		srv.leaseEnd = now.Add(time.Hour)
		ip = ip.Next()
	}

	lease := func(ip netip.Addr, expiry time.Time) dhcpapi.DHCPAllocated {
		return dhcpapi.DHCPAllocated{IP: ip.String(), Expiry: kmetav1.NewTime(expiry)}
	}

	// The probe's offer is never requested, so it may stay an offer.
	probe := lease(ip, now.Add(time.Minute))
	probe.Discover = true

	clean := map[string]dhcpapi.DHCPAllocated{dhcpProbeMAC(sub.index).String(): probe}
	for _, srv := range servers {
		// Padding on top is fine, and so is clock skew within a minute.
		clean[srv.mac.String()] = lease(srv.ip, srv.leaseEnd.Add(-30*time.Second))
	}

	checks := newDHCPChecks()
	checkStored(checks, plan, servers, map[int]map[string]dhcpapi.DHCPAllocated{0: clean})
	require.Zero(t, checks.failed())

	offered := lease(servers[0].ip, now.Add(time.Hour))
	offered.Discover = true

	broken := map[string]dhcpapi.DHCPAllocated{
		servers[0].mac.String(): offered, // bound, but only its offer is stored
		// servers[1] missing
		servers[2].mac.String(): lease(servers[0].ip, now.Add(time.Hour)),   // another's IP: overlap and wrong
		servers[3].mac.String(): lease(servers[3].ip, now.Add(time.Minute)), // expires under it
		"02:aa:00:00:00:01":     lease(ip.Next(), now),
	}

	checks = newDHCPChecks()
	checkStored(checks, plan, servers, map[int]map[string]dhcpapi.DHCPAllocated{0: broken})

	require.Equal(t, int64(1), checks.count(checkOverlapLease))
	require.Equal(t, int64(3), checks.count(checkMissingLease))
	require.Equal(t, int64(1), checks.count(checkShortLease))
	require.Equal(t, int64(1), checks.count(checkUnknownLease))
}
