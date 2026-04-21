// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/errgroup"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// discoverEndpoints resolves one Endpoint per (server, subnet) attachment for
// the given servers. It issues one SSH call per server to read all configured
// IPv4 addresses and matches each attached subnet to its real IP (for regular
// subnets) or /32 VIP on lo (for HostBGP subnets).
func discoverEndpoints(
	ctx context.Context,
	kube kclient.Reader,
	sshs map[string]*sshutil.Config,
	serverNames []string,
) ([]Endpoint, error) {
	vpcList := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcList, kclient.InNamespace(kmetav1.NamespaceDefault)); err != nil {
		return nil, fmt.Errorf("listing VPCs: %w", err)
	}
	vpcByName := map[string]*vpcapi.VPC{}
	for i := range vpcList.Items {
		v := &vpcList.Items[i]
		vpcByName[v.Name] = v
	}

	type result struct {
		endpoints []Endpoint
		err       error
	}
	results := sync.Map{}

	g, gctx := errgroup.WithContext(ctx)
	for _, name := range serverNames {
		g.Go(func() error {
			ctx, cancel := context.WithTimeout(gctx, 2*time.Minute)
			defer cancel()

			eps, err := discoverServerEndpoints(ctx, kube, sshs[name], name, vpcByName)
			results.Store(name, result{endpoints: eps, err: err})
			if err != nil {
				return fmt.Errorf("discovering endpoints for server %q: %w", name, err)
			}

			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	out := []Endpoint{}
	seen := map[EndpointKey]bool{}
	for _, name := range serverNames {
		r, _ := results.Load(name)
		for _, ep := range r.(result).endpoints {
			key := ep.Key()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ep)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Key().String() < out[j].Key().String()
	})

	return out, nil
}

func discoverServerEndpoints(
	ctx context.Context,
	kube kclient.Reader,
	ssh *sshutil.Config,
	server string,
	vpcByName map[string]*vpcapi.VPC,
) ([]Endpoint, error) {
	if ssh == nil {
		return nil, fmt.Errorf("missing ssh config for %q", server) //nolint:err113
	}

	attached, err := apiutil.GetAttachedSubnets(ctx, kube, server)
	if err != nil {
		return nil, fmt.Errorf("getting attached subnets: %w", err)
	}
	if len(attached) == 0 {
		return nil, nil
	}

	stdout, stderr, err := ssh.Run(ctx, "ip -o -4 addr show | awk '{print $2, $4}'")
	if err != nil {
		return nil, fmt.Errorf("running ip addr show: %w: %s", err, stderr)
	}

	addrs, err := parseInterfaceAddrs(stdout)
	if err != nil {
		return nil, err
	}

	attachments := make([]subnetAttachment, 0, len(attached))
	for subnetName := range attached {
		vpcName, subName, ok := strings.Cut(subnetName, "/")
		if !ok {
			return nil, fmt.Errorf("unexpected subnet name %q (want vpc/subnet)", subnetName) //nolint:err113
		}
		vpc, ok := vpcByName[vpcName]
		if !ok {
			return nil, fmt.Errorf("VPC %q not found for attachment %q", vpcName, subnetName) //nolint:err113
		}
		sub, ok := vpc.Spec.Subnets[subName]
		if !ok || sub == nil {
			return nil, fmt.Errorf("subnet %q not found in VPC %q", subName, vpcName) //nolint:err113
		}
		cidr, err := netip.ParsePrefix(sub.Subnet)
		if err != nil {
			return nil, fmt.Errorf("parsing CIDR %q for subnet %q: %w", sub.Subnet, subnetName, err)
		}
		attachments = append(attachments, subnetAttachment{
			FullName: subnetName,
			CIDR:     cidr,
			HostBGP:  sub.HostBGP,
		})
	}

	return matchEndpointIPs(server, addrs, attachments), nil
}

// ifaceAddr pairs a Linux interface name with one IPv4 prefix.
type ifaceAddr struct {
	iface  string
	prefix netip.Prefix
}

// subnetAttachment is the subset of per-subnet configuration needed to match
// a server's interface addresses to its (server, subnet) endpoint.
type subnetAttachment struct {
	FullName string       // "vpc-1/default"
	CIDR     netip.Prefix // e.g. 10.0.1.0/24
	HostBGP  bool
}

// parseInterfaceAddrs parses stdout from `ip -o -4 addr show | awk '{print $2, $4}'`
// into (iface, prefix) tuples, skipping known management interfaces and
// 127.0.0.1/8. It keeps the loopback interface so HostBGP VIPs remain visible.
func parseInterfaceAddrs(stdout string) ([]ifaceAddr, error) {
	out := []ifaceAddr{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] == "enp2s0" || fields[0] == "docker0" {
			continue
		}
		prefix, err := netip.ParsePrefix(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parsing ip %q on %q: %w", fields[1], fields[0], err)
		}
		if prefix.Addr().IsLoopback() {
			continue
		}
		out = append(out, ifaceAddr{iface: fields[0], prefix: prefix})
	}

	return out, nil
}

// matchEndpointIPs matches each subnet attachment to an interface address.
// For regular subnets the IP must be on a non-loopback interface and contained
// in the subnet CIDR. For HostBGP subnets the IP must be a /32 on `lo` and
// contained in the subnet CIDR.
func matchEndpointIPs(server string, addrs []ifaceAddr, attachments []subnetAttachment) []Endpoint {
	endpoints := []Endpoint{}
	for _, att := range attachments {
		ep := Endpoint{
			Server:  server,
			Subnet:  att.FullName,
			HostBGP: att.HostBGP,
		}

		var matched []ifaceAddr
		for _, a := range addrs {
			if !att.CIDR.Contains(a.prefix.Addr()) {
				continue
			}
			if att.HostBGP {
				if a.iface != "lo" || a.prefix.Bits() != 32 {
					continue
				}
			} else if a.iface == "lo" {
				continue
			}
			matched = append(matched, a)
		}
		if len(matched) == 0 {
			slog.Warn("No IP found for endpoint", "server", server, "subnet", att.FullName, "cidr", att.CIDR.String(), "hostBGP", att.HostBGP)

			continue
		}
		if len(matched) > 1 {
			slog.Warn("Multiple IPs matched endpoint, using first", "server", server, "subnet", att.FullName, "matches", matched)
		}
		ep.IP = matched[0].prefix.Addr()
		ep.Interface = matched[0].iface
		slog.Debug("Discovered endpoint", "server", server, "subnet", att.FullName, "ip", ep.IP.String(), "iface", ep.Interface, "hostBGP", att.HostBGP)
		endpoints = append(endpoints, ep)
	}

	return endpoints
}
