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

	type ifaceAddr struct {
		iface  string
		prefix netip.Prefix
	}
	addrs := []ifaceAddr{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// Keep lo so HostBGP /32 VIPs are discoverable; filter only true management interfaces.
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
		addrs = append(addrs, ifaceAddr{iface: fields[0], prefix: prefix})
	}

	endpoints := []Endpoint{}
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

		ep := Endpoint{
			Server:  server,
			Subnet:  subnetName,
			HostBGP: sub.HostBGP,
		}

		var matched []ifaceAddr
		for _, a := range addrs {
			if !cidr.Contains(a.prefix.Addr()) {
				continue
			}
			if sub.HostBGP {
				// HostBGP VIPs live as /32 on the loopback.
				if a.iface != "lo" || a.prefix.Bits() != 32 {
					continue
				}
			} else if a.iface == "lo" {
				continue
			}
			matched = append(matched, a)
		}
		if len(matched) == 0 {
			slog.Warn("No IP found for endpoint", "server", server, "subnet", subnetName, "cidr", cidr.String(), "hostBGP", sub.HostBGP)

			continue
		}
		if len(matched) > 1 {
			slog.Warn("Multiple IPs matched endpoint, using first", "server", server, "subnet", subnetName, "matches", matched)
		}
		ep.IP = matched[0].prefix.Addr()
		slog.Debug("Discovered endpoint", "server", server, "subnet", subnetName, "ip", ep.IP.String(), "hostBGP", sub.HostBGP)
		endpoints = append(endpoints, ep)
	}

	return endpoints, nil
}
