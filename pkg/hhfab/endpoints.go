// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/errgroup"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// SSHResolver returns the SSH config for a server. The collector and per-
// server refresh helpers take one of these instead of a pre-built
// map[string]*sshutil.Config so callers can share whatever SSH plumbing
// they already have (SetupVPCs builds a map keyed by VM name; rt_* tests
// resolve lazily through testCtx.getSSH).
type SSHResolver func(server string) (*sshutil.Config, error)

// SSHResolverFromMap adapts a pre-built map into an SSHResolver.
func SSHResolverFromMap(m map[string]*sshutil.Config) SSHResolver {
	return func(server string) (*sshutil.Config, error) {
		cfg, ok := m[server]
		if !ok {
			return nil, fmt.Errorf("no ssh config for server %q", server) //nolint:goerr113
		}

		return cfg, nil
	}
}

// discoveredIP pairs a server-side interface name with the address found
// on it. The interface tells us whether the address is a hostBGP /32 VIP
// (on `lo`) or a regular subnet address (on the bond/VLAN interface).
type discoveredIP struct {
	iface  string
	prefix netip.Prefix
}

// discoverServerIPs returns every eligible IPv4 address configured on the
// server, paired with the interface it lives on. The management interface
// (enp2s0), the docker bridge (docker0), and the loopback 127.0.0.1/8 entry
// are skipped; other lo addresses (hostBGP /32 VIPs) are kept.
func discoverServerIPs(ctx context.Context, sshCfg *sshutil.Config, server string) ([]discoveredIP, error) {
	stdout, stderr, err := sshCfg.Run(ctx, "ip -o -4 addr show | awk '{print $2, $4}'")
	if err != nil {
		return nil, fmt.Errorf("running ip addr show on %s: %w: %s", server, err, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	out := make([]discoveredIP, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if (fields[0] == "lo" && fields[1] == "127.0.0.1/8") || fields[0] == "enp2s0" || fields[0] == "docker0" {
			continue
		}
		prefix, err := netip.ParsePrefix(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parsing %q on %s: %w", fields[1], server, err)
		}
		out = append(out, discoveredIP{iface: fields[0], prefix: prefix})
	}

	return out, nil
}

// serverAttachment is the (vpc, subnet) information the collector resolves
// from a VPCAttachment + its referenced VPC CRD.
type serverAttachment struct {
	vpcName    string
	subnetName string
	subnetCIDR netip.Prefix
	hostBGP    bool
	attachName string // for diagnostics
}

// CollectServerEndpoints observes the live cluster and produces one
// *Endpoint per (server, vpc, subnet) attachment for each server in the
// `servers` filter (nil → all servers attached to at least one VPC).
//
// Algorithm: list VPCAttachments → group by server (resolved via each
// attachment's Connection); for each candidate server, SSH it and read all
// IPv4 addresses; match each address to one of its attachments by CIDR
// containment (narrowest prefix wins on ties). HostBGP is derived from
// VPCSubnet.HostBGP. An attachment with no matching configured address is
// dropped with a warning; a server with no configured addresses contributes
// nothing — this matches today's SetupVPCs behavior, where ESLAG servers
// in L3VNI mode are skipped before hhnet runs and therefore have no IPs.
//
// Returns an error only when SSH itself fails on a queried server or when a
// VPCAttachment cannot be resolved to a Connection.
func CollectServerEndpoints(ctx context.Context, kube kclient.Client, ssh SSHResolver, servers []string) ([]*Endpoint, error) {
	attaches := &vpcapi.VPCAttachmentList{}
	if err := kube.List(ctx, attaches); err != nil {
		return nil, fmt.Errorf("listing VPCAttachments: %w", err)
	}

	connCache := map[string]*wiringapi.Connection{}
	getConn := func(name string) (*wiringapi.Connection, error) {
		if c, ok := connCache[name]; ok {
			return c, nil
		}
		c := &wiringapi.Connection{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, c); err != nil {
			return nil, fmt.Errorf("getting connection %q: %w", name, err)
		}
		connCache[name] = c

		return c, nil
	}

	vpcCache := map[string]*vpcapi.VPC{}
	getVPC := func(name string) (*vpcapi.VPC, error) {
		if v, ok := vpcCache[name]; ok {
			return v, nil
		}
		v := &vpcapi.VPC{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, v); err != nil {
			return nil, fmt.Errorf("getting VPC %q: %w", name, err)
		}
		vpcCache[name] = v

		return v, nil
	}

	want := map[string]bool{}
	for _, s := range servers {
		want[s] = true
	}

	serverAttachments := map[string][]serverAttachment{}
	for _, attach := range attaches.Items {
		conn, err := getConn(attach.Spec.Connection)
		if err != nil {
			return nil, fmt.Errorf("resolving attachment %q: %w", attach.Name, err)
		}
		_, srvs, _, _, err := conn.Spec.Endpoints()
		if err != nil {
			return nil, fmt.Errorf("getting endpoints of connection %q: %w", conn.Name, err)
		}
		if len(srvs) != 1 {
			// VPCAttachments only reference server-facing connections; if a
			// connection has no server endpoint we skip it as a malformed
			// attachment rather than erroring out, since fabric webhooks
			// already gate this.
			continue
		}
		serverName := srvs[0]
		if len(want) > 0 && !want[serverName] {
			continue
		}

		vpc, err := getVPC(attach.Spec.VPCName())
		if err != nil {
			return nil, fmt.Errorf("resolving attachment %q: %w", attach.Name, err)
		}
		subnetName := attach.Spec.SubnetName()
		subnet, ok := vpc.Spec.Subnets[subnetName]
		if !ok {
			return nil, fmt.Errorf("attachment %q references missing subnet %s/%s", attach.Name, vpc.Name, subnetName) //nolint:goerr113
		}
		cidr, err := netip.ParsePrefix(subnet.Subnet)
		if err != nil {
			return nil, fmt.Errorf("parsing VPC %s/%s subnet CIDR %q: %w", vpc.Name, subnetName, subnet.Subnet, err)
		}

		serverAttachments[serverName] = append(serverAttachments[serverName], serverAttachment{
			vpcName:    vpc.Name,
			subnetName: subnetName,
			subnetCIDR: cidr,
			hostBGP:    subnet.HostBGP,
			attachName: attach.Name,
		})
	}

	// Probe every candidate server in parallel; errgroup mirrors what
	// SetupVPCs does for the hhnet config loop.
	type collected struct {
		serverName string
		ips        []discoveredIP
	}
	var (
		mu       sync.Mutex
		probed   []collected
		eg, ectx = errgroup.WithContext(ctx)
	)
	names := make([]string, 0, len(serverAttachments))
	for name := range serverAttachments {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		eg.Go(func() error {
			cfg, err := ssh(name)
			if err != nil {
				return fmt.Errorf("getting ssh config for %s: %w", name, err)
			}
			ips, err := discoverServerIPs(ectx, cfg, name)
			if err != nil {
				return fmt.Errorf("discovering IPs on %s: %w", name, err)
			}
			mu.Lock()
			probed = append(probed, collected{serverName: name, ips: ips})
			mu.Unlock()

			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, fmt.Errorf("probing servers for IPs: %w", err)
	}

	// Build endpoints by matching each discovered IP against the server's
	// candidate attachments (already narrowed by VPCAttachment list). When
	// multiple attachments contain the IP, the narrowest-prefix attachment
	// wins (handles the P2P /31 case where a /31 sits inside the parent
	// /24).
	slices.SortFunc(probed, func(a, b collected) int { return strings.Compare(a.serverName, b.serverName) })
	out := []*Endpoint{}
	for _, p := range probed {
		atts := serverAttachments[p.serverName]
		used := make([]bool, len(atts))
		if len(p.ips) == 0 {
			slog.Warn("Server has no configured IPs, skipping endpoints", "server", p.serverName, "attachments", len(atts))

			continue
		}
		for _, ip := range p.ips {
			bestIdx := -1
			bestBits := -1
			for i, att := range atts {
				if !att.subnetCIDR.Contains(ip.prefix.Addr()) {
					continue
				}
				if att.subnetCIDR.Bits() > bestBits {
					bestBits = att.subnetCIDR.Bits()
					bestIdx = i
				}
			}
			if bestIdx < 0 {
				slog.Warn("Server IP does not match any attachment subnet", "server", p.serverName, "iface", ip.iface, "addr", ip.prefix.String())

				continue
			}
			if used[bestIdx] {
				slog.Warn("Multiple server IPs match the same attachment, keeping the first one",
					"server", p.serverName, "iface", ip.iface, "addr", ip.prefix.String(),
					"vpc", atts[bestIdx].vpcName, "subnet", atts[bestIdx].subnetName)

				continue
			}

			att := atts[bestIdx]
			used[bestIdx] = true
			out = append(out, &Endpoint{
				Server: &ServerEndpoint{
					Name:    p.serverName,
					VPC:     att.vpcName,
					Subnet:  att.subnetName,
					HostBGP: att.hostBGP,
					IP:      ip.prefix.Addr(),
				},
			})
		}
		for i, att := range atts {
			if !used[i] {
				slog.Warn("Attachment has no matching IP on server, dropping endpoint",
					"server", p.serverName, "vpc", att.vpcName, "subnet", att.subnetName, "attachment", att.attachName)
			}
		}
	}

	return out, nil
}

// ReplaceServerEndpoints reconciles the matrix's endpoints for one
// server against newEPs:
//
//   - Existing endpoints whose (vpc, subnet) match an entry in newEPs
//     are updated in place — the IP and HostBGP fields are copied
//     across, but the *Endpoint pointer stays the same. Matrix entries
//     keyed on that pointer remain valid, so suite setups that don't
//     wipe between tests keep their verdicts.
//   - Existing endpoints with no (vpc, subnet) match in newEPs are
//     dropped from AllEndpoints; entries referencing them are deleted
//     (the topology those verdicts assumed no longer holds).
//   - newEPs that don't match any existing endpoint are appended; the
//     matrix has no entries for them until the next Repopulate or
//     overlay.
//
// Use after a runtime topology change (server moved to a different VPC,
// or DHCP lease refreshed) to bring the matrix back in sync with the
// cluster while preserving entries for attachments that didn't change.
//
// Entries in newEPs whose Server.Name != name are appended as-is, so
// mixing External endpoints into the slice is a no-op for the matching
// pass.
func (m *ConnectivityMatrix) ReplaceServerEndpoints(name string, newEPs []*Endpoint) {
	if m == nil {
		return
	}

	type vsKey struct{ vpc, subnet string }
	byVS := map[vsKey]*Endpoint{}
	for _, ep := range newEPs {
		if ep == nil || ep.Server == nil || ep.Server.Name != name {
			continue
		}
		byVS[vsKey{ep.Server.VPC, ep.Server.Subnet}] = ep
	}

	matched := map[*Endpoint]bool{}
	stale := map[*Endpoint]struct{}{}
	kept := make([]*Endpoint, 0, len(m.AllEndpoints)+len(newEPs))
	for _, ep := range m.AllEndpoints {
		if ep == nil || ep.Server == nil || ep.Server.Name != name {
			kept = append(kept, ep)

			continue
		}
		key := vsKey{ep.Server.VPC, ep.Server.Subnet}
		if newEp, ok := byVS[key]; ok {
			ep.Server.IP = newEp.Server.IP
			ep.Server.HostBGP = newEp.Server.HostBGP
			matched[newEp] = true
			kept = append(kept, ep)

			continue
		}
		stale[ep] = struct{}{}
	}
	for _, ep := range newEPs {
		if ep == nil || matched[ep] {
			continue
		}
		kept = append(kept, ep)
	}
	m.AllEndpoints = kept

	if len(stale) == 0 || len(m.entries) == 0 {
		return
	}
	for pair := range m.entries {
		if _, ok := stale[pair.Source]; ok {
			delete(m.entries, pair)

			continue
		}
		if _, ok := stale[pair.Destination]; ok {
			delete(m.entries, pair)
		}
	}
}
