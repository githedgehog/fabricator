// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// The bench measures load; these are the few things plain enough on the wire or
// in dhcpd's own record to call misbehaviour. They hold dhcpd to RFC 2131, not
// to what today's dhcpd does. None fails a run, every one is counted and shown
// with examples.
type checkName string

const (
	// On the wire.
	checkOverlap          checkName = "overlapping assignment"
	checkOutOfRange       checkName = "out of range"
	checkZeroOffer        checkName = "zero offer"
	checkZeroAck          checkName = "zero ack"
	checkWrongIP          checkName = "wrong IP"
	checkTakenGranted     checkName = "taken IP granted"
	checkTakenNotNaked    checkName = "taken IP not NAKed"
	checkOfferNaked       checkName = "offer NAKed"
	checkLeaseNaked       checkName = "lease NAKed"
	checkRenewalChangedIP checkName = "renewal changed IP"
	checkMismatchedReply  checkName = "mismatched reply"
	checkServerIDChanged  checkName = "server ID changed"
	checkNoLeaseTime      checkName = "no lease time"
	checkNoServerID       checkName = "no server ID"
	checkUnbound          checkName = "unbound"

	// Against what dhcpd stored. When it can't be read the others can't run,
	// which is no proof of misbehaviour but never a clean round either.
	checkLeasesUnread checkName = "stored leases unread"
	checkOverlapLease checkName = "overlapping lease"
	checkMissingLease checkName = "missing lease"
	checkUnknownLease checkName = "unknown lease"
	checkShortLease   checkName = "short lease"
)

// checkOrder is the order checks are reported in.
var checkOrder = []checkName{
	checkOverlap, checkOutOfRange, checkZeroOffer, checkZeroAck, checkWrongIP, checkTakenGranted, checkTakenNotNaked,
	checkOfferNaked, checkLeaseNaked, checkRenewalChangedIP, checkMismatchedReply, checkServerIDChanged, checkNoLeaseTime, checkNoServerID, checkUnbound,
	checkLeasesUnread, checkOverlapLease, checkMissingLease, checkUnknownLease, checkShortLease,
}

// maxCheckExamples is how many instances of a check are kept to show.
const maxCheckExamples = 5

// dhcpLeaseSkew is how far dhcpd's clock and the bench's may disagree before a
// stored expiry counts as short. dhcpd stamps the lease when it sends the ACK,
// the bench when it reads it, and the two run on different machines.
const dhcpLeaseSkew = time.Minute

type dhcpChecks struct {
	mu       sync.Mutex
	counts   map[checkName]int64
	examples map[checkName][]string
}

func newDHCPChecks() *dhcpChecks {
	return &dhcpChecks{counts: map[checkName]int64{}, examples: map[checkName][]string{}}
}

// record counts one instance of a check, keeping the first few as examples.
func (c *dhcpChecks) record(name checkName, format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.counts[name]++
	if len(c.examples[name]) < maxCheckExamples {
		c.examples[name] = append(c.examples[name], fmt.Sprintf(format, args...))
	}
}

func (c *dhcpChecks) count(name checkName) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.counts[name]
}

// merge folds another round's checks in, for the summary.
func (c *dhcpChecks) merge(other *dhcpChecks) {
	other.mu.Lock()
	defer other.mu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	for name, n := range other.counts {
		c.counts[name] += n
	}
	for name, examples := range other.examples {
		room := maxCheckExamples - len(c.examples[name])
		c.examples[name] = append(c.examples[name], examples[:min(room, len(examples))]...)
	}
}

// failed is how many checks fired at least once.
func (c *dhcpChecks) failed() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0
	for _, count := range c.counts {
		if count > 0 {
			n++
		}
	}

	return n
}

// log prints every check that fired, at WRN, with its examples, and a closing
// line that says whether anything did.
func (c *dhcpChecks) log(scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	failed := 0

	for _, name := range checkOrder {
		count := c.counts[name]
		if count == 0 {
			continue
		}

		failed++

		slog.Warn("  Misbehaviour: "+string(name), "scope", scope, "count", count)
		for _, example := range c.examples[name] {
			slog.Warn("    " + example)
		}
	}

	if failed == 0 {
		slog.Info("  No misbehaviour seen", "scope", scope)
	} else {
		slog.Warn(fmt.Sprintf("  %d checks failed", failed), "scope", scope)
	}
}

// checkStoredLeases reads what dhcpd stored and checks the round against it.
// When it can't be read, that is recorded as a failed check: the round's
// figures are still worth keeping, but it must not pass as clean.
func checkStoredLeases(ctx context.Context, kube kclient.Reader, checks *dhcpChecks, plan []*dhcpSubnet, servers []*dhcpServer) {
	stored, err := storedLeases(ctx, kube, plan)
	if err != nil {
		checks.record(checkLeasesUnread, "the checks against dhcpd's record didn't run: %v", err)

		return
	}

	checkStored(checks, plan, servers, stored)
}

// checkStored compares the round with what dhcpd stored: no IP held by two
// MACs, every bound server's lease present with the IP it was ACKed and an
// expiry no earlier than the lease it was given, and nothing for MACs the
// bench never used. The probe's leftover offer is the one expected stranger.
func checkStored(checks *dhcpChecks, plan []*dhcpSubnet, servers []*dhcpServer, stored map[int]map[string]dhcpapi.DHCPAllocated) {
	known := make(map[string]*dhcpServer, len(servers))
	for _, srv := range servers {
		known[srv.mac.String()] = srv
	}

	for _, sub := range plan {
		allocated := stored[sub.index]

		byIP := map[netip.Addr][]string{}
		for mac, lease := range allocated {
			if ip, err := netip.ParseAddr(lease.IP); err == nil {
				byIP[ip] = append(byIP[ip], mac)
			}

			srv, ok := known[mac]
			switch {
			case ok && srv.sub == sub:
			case mac == dhcpProbeMAC(sub.index).String():
			default:
				checks.record(checkUnknownLease, "%s has a lease for unknown MAC %s (%s)", sub.dhcpSubnetName(), mac, lease.IP)
			}
		}

		for ip, macs := range byIP {
			if len(macs) > 1 {
				slices.Sort(macs)
				checks.record(checkOverlapLease, "%s stores %s for %v", sub.dhcpSubnetName(), ip, macs)
			}
		}

		for _, srv := range servers[sub.first : sub.first+sub.servers] {
			if srv.leaseEnd.IsZero() {
				continue
			}

			lease, ok := allocated[srv.mac.String()]
			switch {
			case !ok:
				checks.record(checkMissingLease, "%s holds %s, %s has no lease for it", srv.hostname, srv.ip, sub.dhcpSubnetName())
			case lease.IP != srv.ip.String():
				checks.record(checkMissingLease, "%s holds %s, %s stores %s for it", srv.hostname, srv.ip, sub.dhcpSubnetName(), lease.IP)
			case lease.Discover:
				// What dhcpd keeps for an offer: the ACKed lease never replaced it.
				checks.record(checkMissingLease, "%s holds %s, %s only stores an offer for it", srv.hostname, srv.ip, sub.dhcpSubnetName())
			case lease.Expiry.Add(dhcpLeaseSkew).Before(srv.leaseEnd):
				checks.record(checkShortLease, "%s's lease runs to %s, %s stores an expiry of %s",
					srv.hostname, srv.leaseEnd.UTC().Format(time.RFC3339), sub.dhcpSubnetName(), lease.Expiry.UTC().Format(time.RFC3339))
			}
		}
	}
}
