// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlekSi/pointer"
	"github.com/melbahja/goph"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	coreapi "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const (
	ServerNamePrefix = "server-"
	VSIPerfSpeed     = 1
)

func (c *Config) Wait(ctx context.Context, vlab *VLAB) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	start := time.Now()

	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	cacheCancel, kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
		fabapi.SchemeBuilder,
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	slog.Info("Waiting for all switches ready")
	if err := WaitSwitchesReady(ctx, kube, 1*time.Minute, 30*time.Minute); err != nil {
		return fmt.Errorf("waiting for switches ready: %w", err)
	}

	slog.Info("All switches are ready", "took", time.Since(start))

	return nil
}

type SetupVPCsOpts struct {
	WaitSwitchesReady bool
	ForceCleanup      bool
	VLANNamespace     string
	IPv4Namespace     string
	ServersPerSubnet  int
	SubnetsPerVPC     int
	DNSServers        []string
	TimeServers       []string
	InterfaceMTU      uint16
}

func CreateOrUpdateVpc(ctx context.Context, kube client.Client, vpc *vpcapi.VPC) (bool, error) {
	var changed bool
	some := &vpcapi.VPC{ObjectMeta: metav1.ObjectMeta{Name: vpc.Name, Namespace: vpc.Namespace}}
	res, err := ctrlutil.CreateOrUpdate(ctx, kube, some, func() error {
		some.Spec = vpc.Spec
		some.Default()

		return nil
	})
	if err != nil {
		return changed, fmt.Errorf("creating or updating VPC %q: %w", vpc.Name, err)
	}

	switch res {
	case ctrlutil.OperationResultCreated:
		slog.Info("Created", "vpc", vpc.Name, "subnets", len(vpc.Spec.Subnets))
		changed = true
	case ctrlutil.OperationResultUpdated:
		slog.Info("Updated", "vpc", vpc.Name, "subnets", len(vpc.Spec.Subnets))
		changed = true
	}

	return changed, nil
}

func GetKubeClientWithCache(ctx context.Context, workDir string) (context.CancelFunc, client.Client, error) {
	kubeconfig := filepath.Join(workDir, VLABDir, VLABKubeConfig)
	return kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
		fabapi.SchemeBuilder,
	)
}

func (c *Config) SetupVPCs(ctx context.Context, vlab *VLAB, opts SetupVPCsOpts) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	start := time.Now()

	if opts.ServersPerSubnet <= 0 {
		return fmt.Errorf("servers per subnet must be positive")
	}
	if opts.SubnetsPerVPC <= 0 {
		return fmt.Errorf("subnets per VPC must be positive")
	}

	slog.Info("Setting up VPCs and VPCAttachments",
		"perSubnet", opts.ServersPerSubnet,
		"perVPC", opts.SubnetsPerVPC,
		"wait", opts.WaitSwitchesReady,
		"cleanup", opts.ForceCleanup,
	)

	sshPorts := map[string]uint{}
	for _, vm := range vlab.VMs {
		sshPorts[vm.Name] = getSSHPort(vm.ID)
	}

	sshAuth, err := goph.RawKey(vlab.SSHKey, "")
	if err != nil {
		return fmt.Errorf("getting ssh auth: %w", err)
	}

	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	cacheCancel, kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
		fabapi.SchemeBuilder,
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	servers := &wiringapi.ServerList{}
	if err := kube.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}

	serverIDs := map[string]uint64{}
	for _, server := range servers.Items {
		if !strings.HasPrefix(server.Name, ServerNamePrefix) {
			return fmt.Errorf("unexpected server name %q, should be %s<number>", server.Name, ServerNamePrefix)
		}

		serverID, err := strconv.ParseUint(server.Name[len(ServerNamePrefix):], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing server id: %w", err)
		}

		serverIDs[server.Name] = serverID
	}

	slices.SortFunc(servers.Items, func(a, b wiringapi.Server) int {
		return int(serverIDs[a.Name]) - int(serverIDs[b.Name])
	})

	vlanNS := &wiringapi.VLANNamespace{}
	if err := kube.Get(ctx, client.ObjectKey{Name: opts.VLANNamespace, Namespace: metav1.NamespaceDefault}, vlanNS); err != nil {
		return fmt.Errorf("getting VLAN namespace %s: %w", opts.VLANNamespace, err)
	}
	nextVLAN, stopVLAN := iter.Pull(VLANsFrom(vlanNS.Spec.Ranges...))
	defer stopVLAN()

	ipNS := &vpcapi.IPv4Namespace{}
	if err := kube.Get(ctx, client.ObjectKey{Name: opts.IPv4Namespace, Namespace: metav1.NamespaceDefault}, ipNS); err != nil {
		return fmt.Errorf("getting IPv4 namespace %s: %w", opts.IPv4Namespace, err)
	}
	prefixes := []netip.Prefix{}
	for _, prefix := range ipNS.Spec.Subnets {
		prefix, err := netip.ParsePrefix(prefix)
		if err != nil {
			return fmt.Errorf("parsing IPv4 namespace %s prefix %q: %w", opts.IPv4Namespace, prefix, err)
		}
		prefixes = append(prefixes, prefix)
	}
	nextPrefix, stopPrefix := iter.Pull(SubPrefixesFrom(24, prefixes...))
	defer stopPrefix()

	if len(servers.Items) > 0 && serverIDs[servers.Items[0].Name] > 0 {
		nextVLAN()
		nextPrefix()
	}

	serverInSubnet := 0
	subnetInVPC := 0
	vpcID := 0
	vpcNames := map[string]bool{}
	vpcs := []*vpcapi.VPC{}
	attachNames := map[string]bool{}
	attaches := []*vpcapi.VPCAttachment{}
	netconfs := map[string]string{}
	expectedSubnets := map[string]netip.Prefix{}
	for _, server := range servers.Items {
		if serverInSubnet >= opts.ServersPerSubnet {
			serverInSubnet = 0
			subnetInVPC++
		}
		if subnetInVPC >= opts.SubnetsPerVPC {
			serverInSubnet = 0
			subnetInVPC = 0
			vpcID++
		}

		vpcName := fmt.Sprintf("vpc-%02d", vpcID+1)
		subnetName := fmt.Sprintf("subnet-%02d", subnetInVPC+1)

		serverInSubnet++

		var vpc *vpcapi.VPC
		if len(vpcs) > 0 && vpcs[len(vpcs)-1].Name == vpcName {
			vpc = vpcs[len(vpcs)-1]
		} else {
			vpc = &vpcapi.VPC{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vpcName,
					Namespace: metav1.NamespaceDefault,
				},
				Spec: vpcapi.VPCSpec{
					Subnets: map[string]*vpcapi.VPCSubnet{},
				},
			}
			vpcNames[vpcName] = true
			vpcs = append(vpcs, vpc)
		}
		if vpc.Spec.Subnets[subnetName] == nil {
			subnet, ok := nextPrefix()
			if !ok {
				return fmt.Errorf("no more subnets available")
			}

			vlan, ok := nextVLAN()
			if !ok {
				return fmt.Errorf("no more vlans available")
			}

			var dhcpOpts *vpcapi.VPCDHCPOptions
			if len(opts.DNSServers) > 0 || len(opts.TimeServers) > 0 || opts.InterfaceMTU > 0 {
				dhcpOpts = &vpcapi.VPCDHCPOptions{
					DNSServers:   opts.DNSServers,
					TimeServers:  opts.TimeServers,
					InterfaceMTU: opts.InterfaceMTU,
				}
			}

			vpc.Spec.Subnets[subnetName] = &vpcapi.VPCSubnet{
				Subnet: subnet.String(),
				VLAN:   vlan,
				DHCP: vpcapi.VPCDHCP{
					Enable:  true,
					Options: dhcpOpts,
				},
			}
		}

		expectedSubnet, err := netip.ParsePrefix(vpc.Spec.Subnets[subnetName].Subnet)
		if err != nil {
			return fmt.Errorf("parsing vpc subnet %s/%s %q: %w", vpcName, subnetName, vpc.Spec.Subnets[subnetName].Subnet, err)
		}
		expectedSubnets[server.Name] = expectedSubnet

		conns := &wiringapi.ConnectionList{}
		if err := kube.List(ctx, conns, wiringapi.MatchingLabelsForListLabelServer(server.Name)); err != nil {
			return fmt.Errorf("listing connections for server %q: %w", server.Name, err)
		}

		if len(conns.Items) == 0 {
			return fmt.Errorf("no connections for server %q", server.Name)
		}
		if len(conns.Items) > 1 {
			return fmt.Errorf("multiple connections for server %q", server.Name)
		}

		conn := conns.Items[0]

		attachName := fmt.Sprintf("%s--%s--%s", conn.Name, vpcName, subnetName)
		attachNames[attachName] = true
		attach := &vpcapi.VPCAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      attachName,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: vpcapi.VPCAttachmentSpec{
				Connection: conn.Name,
				Subnet:     fmt.Sprintf("%s/%s", vpcName, subnetName),
			},
		}
		attaches = append(attaches, attach)

		vlan := uint16(0)
		if !attach.Spec.NativeVLAN {
			vlan = vpc.Spec.Subnets[subnetName].VLAN
		}

		netconfCmd := ""
		if conn.Spec.Unbundled != nil {
			netconfCmd = fmt.Sprintf("vlan %d %s", vlan, conn.Spec.Unbundled.Link.Server.LocalPortName())
		} else {
			netconfCmd = fmt.Sprintf("bond %d", vlan)

			if conn.Spec.Bundled != nil {
				for _, link := range conn.Spec.Bundled.Links {
					netconfCmd += " " + link.Server.LocalPortName()
				}
			} else if conn.Spec.MCLAG != nil {
				for _, link := range conn.Spec.MCLAG.Links {
					netconfCmd += " " + link.Server.LocalPortName()
				}
			} else if conn.Spec.ESLAG != nil {
				for _, link := range conn.Spec.ESLAG.Links {
					netconfCmd += " " + link.Server.LocalPortName()
				}
			} else {
				return fmt.Errorf("unexpected connection type for server %s conn %q", server.Name, conn.Name)
			}
		}

		netconfs[server.Name] = netconfCmd
	}

	if opts.WaitSwitchesReady {
		slog.Info("Waiting for switches ready before configuring VPCs and VPCAttachments")
		if err := WaitSwitchesReady(ctx, kube, 1*time.Minute, 30*time.Minute); err != nil {
			return fmt.Errorf("waiting for switches ready: %w", err)
		}
	}

	slog.Info("Configuring VPCs and VPCAttachments")

	changed := false

	existingAttaches := &vpcapi.VPCAttachmentList{}
	if err := kube.List(ctx, existingAttaches); err != nil {
		return fmt.Errorf("listing existing attachments: %w", err)
	}
	for _, attach := range existingAttaches.Items {
		if opts.ForceCleanup || !attachNames[attach.Name] {
			if err := kube.Delete(ctx, &attach); err != nil {
				return fmt.Errorf("deleting attachment %q: %w", attach.Name, err)
			}
			slog.Info("Deleted", "attachment", attach.Name)
			changed = true
		}
	}

	existingVPCs := &vpcapi.VPCList{}
	if err := kube.List(ctx, existingVPCs); err != nil {
		return fmt.Errorf("listing existing VPCs: %w", err)
	}
	for _, vpc := range existingVPCs.Items {
		if opts.ForceCleanup || !vpcNames[vpc.Name] {
			if err := kube.Delete(ctx, &vpc); err != nil {
				return fmt.Errorf("deleting VPC %q: %w", vpc.Name, err)
			}
			slog.Info("Deleted", "vpc", vpc.Name)
			changed = true
		}
	}

	for _, vpc := range vpcs {
		iterChanged, err := CreateOrUpdateVpc(ctx, kube, vpc)
		if err != nil {
			return fmt.Errorf("creating or updating vpc %q: %w", vpc.Name, err)
		}
		changed = changed || iterChanged
	}

	for _, attach := range attaches {
		some := &vpcapi.VPCAttachment{ObjectMeta: metav1.ObjectMeta{Name: attach.Name, Namespace: attach.Namespace}}
		res, err := ctrlutil.CreateOrUpdate(ctx, kube, some, func() error {
			some.Spec = attach.Spec
			some.Default()

			return nil
		})
		if err != nil {
			return fmt.Errorf("creating or updating vpc attachment %q: %w", attach.Name, err)
		}

		switch res {
		case ctrlutil.OperationResultCreated:
			slog.Info("Created", "vpcattachment", attach.Name)
			changed = true
		case ctrlutil.OperationResultUpdated:
			slog.Info("Updated", "vpcattachment", attach.Name)
			changed = true
		}
	}

	if changed && opts.WaitSwitchesReady {
		slog.Info("Waiting for switches ready after configuring VPCs and VPCAttachments")

		// TODO remove it when we can actually know that changes to VPC/VPCAttachment are reflected in agents
		select {
		case <-ctx.Done():
			return fmt.Errorf("sleeping before waiting for switches ready: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}

		if err := WaitSwitchesReady(ctx, kube, 1*time.Minute, 30*time.Minute); err != nil {
			return fmt.Errorf("waiting for switches ready: %w", err)
		}
	}

	slog.Info("Configuring networking on servers")

	g := &errgroup.Group{}
	for _, server := range servers.Items {
		g.Go(func() error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()

			if err := func() error {
				sshPort, ok := sshPorts[server.Name]
				if !ok {
					return fmt.Errorf("missing ssh port for %q", server.Name)
				}

				client, err := goph.NewConn(&goph.Config{
					User:     "core",
					Addr:     "127.0.0.1",
					Port:     sshPort,
					Auth:     sshAuth,
					Timeout:  10 * time.Second,
					Callback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
				})
				if err != nil {
					return fmt.Errorf("connecting to %q: %w", server.Name, err)
				}
				defer client.Close()

				out, err := client.RunContext(ctx, "toolbox -q hostname")
				if err != nil {
					return fmt.Errorf("running toolbox hostname: %w: %s", err, string(out))
				}
				hostname := strings.TrimSpace(string(out))
				if hostname != server.Name {
					return fmt.Errorf("unexpected hostname %q, expected %q", hostname, server.Name)
				}

				slog.Debug("Verified", "server", server.Name)

				out, err = client.RunContext(ctx, "/opt/bin/hhnet cleanup")
				if err != nil {
					return fmt.Errorf("running hhnet cleanup: %w: out: %s", err, string(out))
				}

				out, err = client.RunContext(ctx, "/opt/bin/hhnet "+netconfs[server.Name])
				if err != nil {
					return fmt.Errorf("running hhnet %q: %w: out: %s", netconfs[server.Name], err, string(out))
				}

				prefix, err := netip.ParsePrefix(strings.TrimSpace(string(out)))
				if err != nil {
					return fmt.Errorf("parsing acquired address %q: %w", string(out), err)
				}

				expectedSubnet := expectedSubnets[server.Name]
				if !expectedSubnet.Contains(prefix.Addr()) || expectedSubnet.Bits() != prefix.Bits() {
					return fmt.Errorf("unexpected acquired address %q, expected from %v", prefix.String(), expectedSubnet.String())
				}

				slog.Info("Configured", "server", server.Name, "addr", prefix.String(), "netconf", netconfs[server.Name])

				return nil
			}(); err != nil {
				return fmt.Errorf("configuring server %q: %w", server.Name, err)
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("configuring servers: %w", err)
	}

	slog.Info("All servers configured and verified", "took", time.Since(start))

	return nil
}

type SetupPeeringsOpts struct {
	WaitSwitchesReady bool
	Requests          []string
}

func (c *Config) SetupPeerings(ctx context.Context, vlab *VLAB, opts SetupPeeringsOpts) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	start := time.Now()

	slog.Info("Setting up VPC and External Peerings", "numRequests", len(opts.Requests))

	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	cacheCancel, kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
		fabapi.SchemeBuilder,
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	if opts.WaitSwitchesReady {
		slog.Info("Waiting for switches ready before configuring VPC and External Peerings")
		if err := WaitSwitchesReady(ctx, kube, 1*time.Minute, 30*time.Minute); err != nil {
			return fmt.Errorf("waiting for switches ready: %w", err)
		}
	}

	externalList := &vpcapi.ExternalList{}
	if err := kube.List(ctx, externalList); err != nil {
		return fmt.Errorf("listing externals: %w", err)
	}

	switchGroupList := &wiringapi.SwitchGroupList{}
	if err := kube.List(ctx, switchGroupList); err != nil {
		return fmt.Errorf("listing switch groups: %w", err)
	}

	vpcPeerings := map[string]*vpcapi.VPCPeeringSpec{}
	externalPeerings := map[string]*vpcapi.ExternalPeeringSpec{}

	reqNames := map[string]bool{}
	for _, req := range opts.Requests {
		parts := strings.Split(req, ":")
		if len(parts) < 1 {
			return fmt.Errorf("invalid request %q format", req)
		}

		reqName := parts[0]
		if reqNames[reqName] {
			return fmt.Errorf("duplicate request %q name %s", req, reqName)
		}
		reqNames[reqName] = true

		slog.Debug("Parsing request", "name", reqName, "options", parts[1:])

		vpMark := strings.Contains(reqName, "+")
		epMark := strings.Contains(reqName, "~")

		if vpMark && !epMark {
			reqNameParts := strings.Split(reqName, "+")
			if len(reqNameParts) != 2 {
				return fmt.Errorf("invalid VPC peering request %s", reqName)
			}

			slices.Sort(reqNameParts)

			vpc1 := reqNameParts[0]
			vpc2 := reqNameParts[1]

			if vpc1 == "" || vpc2 == "" {
				return fmt.Errorf("invalid VPC peering request %s, both VPCs should be non-empty", reqName)
			}

			if !strings.HasPrefix(vpc1, "vpc-") {
				if vpcID, err := strconv.ParseUint(vpc1, 10, 64); err == nil {
					vpc1 = fmt.Sprintf("%02d", vpcID)
				}

				vpc1 = "vpc-" + vpc1
			}
			if !strings.HasPrefix(vpc2, "vpc-") {
				if vpcID, err := strconv.ParseUint(vpc2, 10, 64); err == nil {
					vpc2 = fmt.Sprintf("%02d", vpcID)
				}

				vpc2 = "vpc-" + vpc2
			}

			vpcPeering := &vpcapi.VPCPeeringSpec{
				Permit: []map[string]vpcapi.VPCPeer{
					{
						vpc1: {},
						vpc2: {},
					},
				},
			}

			for idx, option := range parts[1:] {
				parts := strings.Split(option, "=")
				if len(parts) > 2 {
					return fmt.Errorf("invalid VPC peering option #%d %s", idx, option)
				}

				optName := parts[0]
				optValue := ""
				if len(parts) == 2 {
					optValue = parts[1]
				}

				if optName == "r" || optName == "remote" {
					if optValue == "" {
						if len(switchGroupList.Items) != 1 {
							return fmt.Errorf("invalid VPC peering option #%d %s, auto switch group only supported when it's exactly one switch group", idx, option)
						}

						vpcPeering.Remote = switchGroupList.Items[0].Name
					}

					vpcPeering.Remote = optValue
				} else {
					return fmt.Errorf("invalid VPC peering option #%d %s", idx, option)
				}
			}

			vpcPeerings[fmt.Sprintf("%s--%s", vpc1, vpc2)] = vpcPeering
		} else if !vpMark && epMark {
			reqNameParts := strings.Split(reqName, "~")
			if len(reqNameParts) != 2 {
				return fmt.Errorf("invalid external peering request %s", reqName)
			}

			vpc := reqNameParts[0]
			ext := reqNameParts[1]

			if vpc == "" {
				return fmt.Errorf("invalid external peering request %s, VPC should be non-empty", reqName)
			}
			if ext == "" {
				return fmt.Errorf("invalid external peering request %s, external should be non-empty", reqName)
			}

			if !strings.HasPrefix(vpc, "vpc-") {
				if vpcID, err := strconv.ParseUint(vpc, 10, 64); err == nil {
					vpc = fmt.Sprintf("%02d", vpcID)
				}

				vpc = "vpc-" + vpc
			}

			extPeering := &vpcapi.ExternalPeeringSpec{
				Permit: vpcapi.ExternalPeeringSpecPermit{
					VPC: vpcapi.ExternalPeeringSpecVPC{
						Name:    vpc,
						Subnets: []string{},
					},
					External: vpcapi.ExternalPeeringSpecExternal{
						Name:     ext,
						Prefixes: []vpcapi.ExternalPeeringSpecPrefix{},
					},
				},
			}

			for idx, option := range parts[1:] {
				parts := strings.Split(option, "=")
				if len(parts) > 2 {
					return fmt.Errorf("invalid external peering option #%d %s", idx, option)
				}

				optName := parts[0]
				optValue := ""
				if len(parts) == 2 {
					optValue = parts[1]
				}

				if optName == "vpc_subnets" || optName == "subnets" || optName == "s" {
					if optValue == "" {
						return fmt.Errorf("invalid external peering option #%d %s, VPC subnet names should be non-empty", idx, option)
					}

					extPeering.Permit.VPC.Subnets = append(extPeering.Permit.VPC.Subnets, strings.Split(optValue, ",")...)
				} else if optName == "ext_prefixes" || optName == "prefixes" || optName == "p" {
					if optValue == "" {
						return fmt.Errorf("invalid external peering option #%d %s, external prefixes should be non-empty", idx, option)
					}

					for _, rawPrefix := range strings.Split(optValue, ",") {
						prefix := vpcapi.ExternalPeeringSpecPrefix{
							Prefix: rawPrefix,
						}
						if strings.Contains(rawPrefix, "_") {
							prefixParts := strings.Split(rawPrefix, "_")
							if len(prefixParts) > 1 {
								return fmt.Errorf("invalid external peering option #%d %s, external prefix should be in format 1.2.3.4/24", idx, option)
							}

							prefix.Prefix = prefixParts[0]
						}

						extPeering.Permit.External.Prefixes = append(extPeering.Permit.External.Prefixes, prefix)
					}
				} else {
					return fmt.Errorf("invalid external peering option #%d %s", idx, option)
				}
			}

			if len(extPeering.Permit.VPC.Subnets) == 0 {
				extPeering.Permit.VPC.Subnets = []string{"default"}
			}
			slices.Sort(extPeering.Permit.VPC.Subnets)

			if len(extPeering.Permit.External.Prefixes) == 0 {
				extPeering.Permit.External.Prefixes = []vpcapi.ExternalPeeringSpecPrefix{
					{
						Prefix: "0.0.0.0/0",
					},
				}
			}
			slices.SortFunc(extPeering.Permit.External.Prefixes, func(a, b vpcapi.ExternalPeeringSpecPrefix) int {
				return strings.Compare(a.Prefix, b.Prefix)
			})

			externalPeerings[fmt.Sprintf("%s--%s", vpc, ext)] = extPeering
		} else {
			return fmt.Errorf("invalid request name %s", reqName)
		}
	}

	if err := DoSetupPeerings(ctx, kube, vpcPeerings, externalPeerings, opts.WaitSwitchesReady); err != nil {
		return err
	}
	slog.Info("VPC and External Peerings setup complete", "took", time.Since(start))

	return nil
}

func DoSetupPeerings(ctx context.Context, kube client.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec, externalPeerings map[string]*vpcapi.ExternalPeeringSpec, waitReady bool) error {
	var changed bool

	vpcPeeringList := &vpcapi.VPCPeeringList{}
	if err := kube.List(ctx, vpcPeeringList); err != nil {
		return fmt.Errorf("listing VPC peerings: %w", err)
	}
	for _, peering := range vpcPeeringList.Items {
		if vpcPeerings[peering.Name] != nil {
			continue
		}

		slog.Info("Deleting VPCPeering", "name", peering.Name)
		changed = true

		if err := client.IgnoreNotFound(kube.Delete(ctx, pointer.To(peering))); err != nil {
			return fmt.Errorf("deleting VPC peering %s: %w", peering.Name, err)
		}
	}

	externalPeeringList := &vpcapi.ExternalPeeringList{}
	if err := kube.List(ctx, externalPeeringList); err != nil {
		return fmt.Errorf("listing external peerings: %w", err)
	}
	for _, peering := range externalPeeringList.Items {
		if externalPeerings[peering.Name] != nil {
			continue
		}

		slog.Info("Deleting ExternalPeering", "name", peering.Name)
		changed = true

		if err := client.IgnoreNotFound(kube.Delete(ctx, pointer.To(peering))); err != nil {
			return fmt.Errorf("deleting external peering %s: %w", peering.Name, err)
		}
	}

	for name, vpcPeeringSpec := range vpcPeerings {
		vpc1, vpc2, err := vpcPeeringSpec.VPCs()
		if err != nil {
			return fmt.Errorf("error getting VPCs for peering %s: %w", name, err)
		}

		slog.Info("Enforcing VPCPeering", "name", name,
			"vpc1", vpc1, "vpc2", vpc2, "remote", vpcPeeringSpec.Remote)

		vpcPeering := &vpcapi.VPCPeering{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: metav1.NamespaceDefault,
			},
		}
		res, err := ctrlutil.CreateOrUpdate(ctx, kube, vpcPeering, func() error {
			vpcPeering.Spec = *vpcPeeringSpec

			return nil
		})
		if err != nil {
			return fmt.Errorf("error updating VPC peering %s: %w", name, err)
		}

		if res == ctrlutil.OperationResultCreated {
			slog.Info("Created", "vpcpeering", name)
			changed = true
		} else if res == ctrlutil.OperationResultUpdated {
			slog.Info("Updated", "vpcpeering", name)
			changed = true
		}
	}

	for name, extPeeringSpec := range externalPeerings {
		slog.Info("Enforcing External Peering", "name", name,
			"vpc", extPeeringSpec.Permit.VPC.Name, "vpcSubnets", extPeeringSpec.Permit.VPC.Subnets,
			"external", extPeeringSpec.Permit.External.Name, "externalPrefixes", extPeeringSpec.Permit.External.Prefixes)

		extPeering := &vpcapi.ExternalPeering{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: metav1.NamespaceDefault,
			},
		}
		res, err := ctrlutil.CreateOrUpdate(ctx, kube, extPeering, func() error {
			extPeering.Spec = *extPeeringSpec

			return nil
		})
		if err != nil {
			return fmt.Errorf("error updating external peering %s: %w", name, err)
		}

		if res == ctrlutil.OperationResultCreated {
			slog.Info("Created", "externalpeering", name)
			changed = true
		} else if res == ctrlutil.OperationResultUpdated {
			slog.Info("Updated", "externalpeering", name)
			changed = true
		}
	}

	if changed && waitReady {
		slog.Info("Waiting for switches ready after configuring VPC and External Peerings")

		// TODO remove it when we can actually know that changes to VPC/VPCAttachment are reflected in agents
		select {
		case <-ctx.Done():
			return fmt.Errorf("sleeping before waiting for switches ready: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}

		if err := WaitSwitchesReady(ctx, kube, 1*time.Minute, 30*time.Minute); err != nil {
			return fmt.Errorf("waiting for switches ready: %w", err)
		}
	}

	return nil
}

type TestConnectivityOpts struct {
	WaitSwitchesReady bool
	PingsCount        int
	PingsParallel     int64
	IPerfsSeconds     int
	IPerfsMinSpeed    float64
	IPerfsParallel    int64
	CurlsCount        int
	CurlsParallel     int64
	Sources           []string
	Destinations      []string
}

func (c *Config) TestConnectivity(ctx context.Context, vlab *VLAB, opts TestConnectivityOpts) error {
	if opts.PingsCount == 0 && opts.IPerfsSeconds == 0 && opts.CurlsCount == 0 {
		return fmt.Errorf("at least one of pings, iperfs or curls should be enabled")
	}
	start := time.Now()

	if opts.PingsParallel <= 0 {
		opts.PingsParallel = 50
	}
	if opts.IPerfsParallel <= 0 {
		opts.IPerfsParallel = 1
	}
	if opts.CurlsParallel <= 0 {
		opts.CurlsParallel = 50
	}

	slog.Info("Testing server to server and server to external connectivity")

	sshPorts := map[string]uint{}
	for _, vm := range vlab.VMs {
		sshPorts[vm.Name] = getSSHPort(vm.ID)
	}

	sshAuth, err := goph.RawKey(vlab.SSHKey, "")
	if err != nil {
		return fmt.Errorf("getting ssh auth: %w", err)
	}

	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	cacheCancel, kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
		fabapi.SchemeBuilder,
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	switches := &wiringapi.SwitchList{}
	if err := kube.List(ctx, switches); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}
	allVS := len(switches.Items) > 0
	for _, sw := range switches.Items {
		if sw.Spec.Profile != meta.SwitchProfileVS {
			allVS = false
			break
		}
	}
	if allVS {
		if opts.IPerfsMinSpeed > 10*VSIPerfSpeed {
			slog.Warn("Lowering IPerf min speed as all switches are virtual", "speed", VSIPerfSpeed)
			opts.IPerfsMinSpeed = VSIPerfSpeed
		} else if opts.IPerfsMinSpeed > VSIPerfSpeed {
			slog.Warn("IPerf min speed is higher than default virtual switch speed", "speed", VSIPerfSpeed)
		}
	}

	if opts.WaitSwitchesReady {
		slog.Info("Waiting for switches ready before testing connectivity")

		if err := WaitSwitchesReady(ctx, kube, 1*time.Minute, 30*time.Minute); err != nil {
			return fmt.Errorf("waiting for switches ready: %w", err)
		}
	}

	servers := &wiringapi.ServerList{}
	if err := kube.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}

	serverIDs := map[string]uint64{}
	for _, server := range servers.Items {
		// Skip servers not in the list of sources or destinations, if both are specified
		if len(opts.Sources) > 0 && len(opts.Destinations) > 0 {
			if !slices.Contains(opts.Sources, server.Name) && !slices.Contains(opts.Destinations, server.Name) {
				continue
			}
		}
		if !strings.HasPrefix(server.Name, ServerNamePrefix) {
			return fmt.Errorf("unexpected server name %q, should be %s<number>", server.Name, ServerNamePrefix)
		}

		serverID, err := strconv.ParseUint(server.Name[len(ServerNamePrefix):], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing server id: %w", err)
		}

		serverIDs[server.Name] = serverID
	}

	slices.SortFunc(servers.Items, func(a, b wiringapi.Server) int {
		return int(serverIDs[a.Name]) - int(serverIDs[b.Name])
	})

	slog.Info("Discovering server IPs", "servers", len(servers.Items))

	ips := sync.Map{}
	sshs := sync.Map{}
	defer func() {
		sshs.Range(func(key, value any) bool {
			if err := value.(*goph.Client).Close(); err != nil {
				slog.Warn("Closing ssh client", "err", err)
			}

			return true
		})
	}()

	g := &errgroup.Group{}
	for server := range serverIDs {
		g.Go(func() error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			if err := func() error {
				sshPort, ok := sshPorts[server]
				if !ok {
					return fmt.Errorf("missing ssh port for %q", server)
				}

				client, err := goph.NewConn(&goph.Config{
					User:     "core",
					Addr:     "127.0.0.1",
					Port:     sshPort,
					Auth:     sshAuth,
					Timeout:  10 * time.Second,
					Callback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
				})
				if err != nil {
					return fmt.Errorf("connecting to %q: %w", server, err)
				}
				sshs.Store(server, client)

				out, err := client.RunContext(ctx, "ip -o -4 addr show | awk '{print $2, $4}'")
				if err != nil {
					return fmt.Errorf("running ip addr show: %w: out: %s", err, string(out))
				}

				found := false
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				for _, line := range lines {
					fields := strings.Fields(line)
					if len(fields) != 2 {
						return fmt.Errorf("unexpected ip addr line %q", line)
					}

					if fields[0] == "lo" || fields[0] == "enp2s0" {
						continue
					}

					if found {
						return fmt.Errorf("unexpected multiple ip addrs")
					}

					addr, err := netip.ParsePrefix(fields[1])
					if err != nil {
						return fmt.Errorf("parsing ip addr %q: %w", fields[1], err)
					}

					found = true
					ips.Store(server, addr)

					slog.Info("Found", "server", server, "addr", addr.String())
				}

				if !found {
					return fmt.Errorf("no ip addr found")
				}

				return nil
			}(); err != nil {
				return fmt.Errorf("getting server %q IP: %w", server, err)
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("discovering server IPs: %w", err)
	}

	slog.Info("Running pings, iperfs and curls", "servers", len(servers.Items))

	pings := semaphore.NewWeighted(opts.PingsParallel)
	iperfs := semaphore.NewWeighted(opts.IPerfsParallel)
	curls := semaphore.NewWeighted(opts.CurlsParallel)

	errors := sync.Map{}

	g = &errgroup.Group{}
	if len(opts.Sources) == 0 {
		opts.Sources = make([]string, len(serverIDs))
		// copy keys of serverIDs to opts.Sources
		i := 0
		for k := range serverIDs {
			opts.Sources[i] = k
			i++
		}
	}
	if len(opts.Destinations) == 0 {
		opts.Destinations = make([]string, len(serverIDs))
		// copy keys of serverIDs to opts.Destinations
		i := 0
		for k := range serverIDs {
			opts.Destinations[i] = k
			i++
		}
	}
	for _, serverA := range opts.Sources {
		for _, serverB := range opts.Destinations {
			if serverA == serverB {
				continue
			}

			if opts.PingsCount > 0 || opts.IPerfsSeconds > 0 {
				g.Go(func() error {
					if err := func() error {
						expectedReachable, err := apiutil.IsServerReachable(ctx, kube, serverA, serverB)
						if err != nil {
							return fmt.Errorf("checking if should be reachable: %w", err)
						}

						slog.Debug("Checking connectivity", "from", serverA, "to", serverB, "reachable", expectedReachable)

						ipBR, ok := ips.Load(serverB)
						if !ok {
							return fmt.Errorf("missing IP for %q", serverB)
						}
						ipB := ipBR.(netip.Prefix)

						clientAR, ok := sshs.Load(serverA)
						if !ok {
							return fmt.Errorf("missing ssh client for %q", serverA)
						}
						clientA := clientAR.(*goph.Client)

						clientBR, ok := sshs.Load(serverB)
						if !ok {
							return fmt.Errorf("missing ssh client for %q", serverB)
						}
						clientB := clientBR.(*goph.Client)

						if err := checkPing(ctx, opts, pings, serverA, serverB, clientA, ipB.Addr(), expectedReachable); err != nil {
							return fmt.Errorf("checking ping from %s to %s: %w", serverA, serverB, err)
						}

						if err := checkIPerf(ctx, opts, iperfs, serverA, serverB, clientA, clientB, ipB.Addr(), expectedReachable); err != nil {
							return fmt.Errorf("checking iperf from %s to %s: %w", serverA, serverB, err)
						}

						return nil
					}(); err != nil {
						errors.Store("vpcpeer--"+serverA+"--"+serverB, err)

						return fmt.Errorf("testing connectivity from %q to %q: %w", serverA, serverB, err)
					}

					return nil
				})
			}
		}

		if opts.CurlsCount > 0 {
			g.Go(func() error {
				if err := func() error {
					reachable, err := apiutil.IsExternalSubnetReachable(ctx, kube, serverA, "0.0.0.0/0") // TODO test for specific IP
					if err != nil {
						return fmt.Errorf("checking if should be reachable: %w", err)
					}

					slog.Debug("Checking external connectivity", "from", serverA, "reachable", reachable)

					clientR, ok := sshs.Load(serverA)
					if !ok {
						return fmt.Errorf("missing ssh client for %q", serverA)
					}
					client := clientR.(*goph.Client)

					if err := checkCurl(ctx, opts, curls, serverA, client, "1.0.0.1", reachable); err != nil {
						return fmt.Errorf("checking curl from %q: %w", serverA, err)
					}

					return nil
				}(); err != nil {
					errors.Store("extpeer--"+serverA, err)

					return fmt.Errorf("testing connectivity from %q to external: %w", serverA, err)
				}

				return nil
			})
		}
	}

	if err := g.Wait(); err != nil {
		slog.Error("Error(s) during testing connectivity")

		errors.Range(func(key, value any) bool {
			slog.Error("Error", "key", key, "err", value)

			return true
		})

		return fmt.Errorf("testing connectivity: %w", err)
	}

	slog.Info("All connectivity tested successfully", "took", time.Since(start))

	return nil
}

func WaitSwitchesReady(ctx context.Context, kube client.Reader, appliedFor time.Duration, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	f, _, _, err := fab.GetFabAndNodes(ctx, kube, true)
	if err != nil {
		return fmt.Errorf("getting fab: %w", err)
	}

	for {
		switches := &wiringapi.SwitchList{}
		if err := kube.List(ctx, switches); err != nil {
			return fmt.Errorf("listing switches: %w", err)
		}

		readyList := []string{}
		notReadyList := []string{}
		notUpdatedList := []string{}
		updateFailedList := []string{}

		allReady := true
		allUpdated := true
		for _, sw := range switches.Items {
			ready := false
			updated := false

			ag := &agentapi.Agent{}
			err := kube.Get(ctx, client.ObjectKey{Name: sw.Name, Namespace: sw.Namespace}, ag)
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("getting agent %q: %w", sw.Name, err)
			}

			if err == nil {
				ready = ag.Status.LastAppliedGen == ag.Generation && time.Since(ag.Status.LastHeartbeat.Time) < 1*time.Minute

				if appliedFor > 0 {
					ready = ready && time.Since(ag.Status.LastAppliedTime.Time) >= appliedFor
				}

				// controller will expect agent of it's own version by default
				updated = ag.Status.Version == string(f.Status.Versions.Fabric.Controller)
			}

			allReady = allReady && ready
			allUpdated = allUpdated && updated

			if ready {
				readyList = append(readyList, sw.Name)
			} else {
				notReadyList = append(notReadyList, sw.Name)
			}

			if ag.Status.Version != "" {
				if ag.Status.LastAppliedGen != ag.Generation && !updated {
					notUpdatedList = append(notUpdatedList, sw.Name)
				}

				if ag.Status.LastAppliedGen == ag.Generation && !updated {
					updateFailedList = append(updateFailedList, sw.Name)
				}
			}
		}

		slices.Sort(readyList)
		slices.Sort(notReadyList)
		slices.Sort(notUpdatedList)
		slices.Sort(updateFailedList)

		if appliedFor == 0 {
			slog.Info("Switches status", "ready", readyList, "notReady", notReadyList)
		} else {
			slog.Info("Switches status (applied for "+fmt.Sprintf("%.1f minutes", appliedFor.Minutes())+")", "ready", readyList, "notReady", notReadyList)
		}
		if len(notUpdatedList) > 0 || len(updateFailedList) > 0 {
			slog.Info("Switch agents", "notUpdated", notUpdatedList, "updateFailed", updateFailedList)
		}

		if allReady && allUpdated {
			return nil
		}

		if allReady && !allUpdated {
			return fmt.Errorf("all switches ready but some are not up-to-date")
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for switches ready: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}
	}
}

func checkPing(ctx context.Context, opts TestConnectivityOpts, pings *semaphore.Weighted, from, to string, fromSSH *goph.Client, toIP netip.Addr, expected bool) error {
	if opts.PingsCount <= 0 {
		return nil
	}

	if err := pings.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("acquiring ping semaphore: %w", err)
	}
	defer pings.Release(1)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.PingsCount+30)*time.Second)
	defer cancel()

	slog.Debug("Running ping", "from", from, "to", toIP.String())

	cmd := fmt.Sprintf("ping -c %d -W 1 %s", opts.PingsCount, toIP.String()) // TODO wrap with timeout?
	outR, err := fromSSH.RunContext(ctx, cmd)
	out := strings.TrimSpace(string(outR))

	pingOk := err == nil && strings.Contains(out, "0% packet loss")
	pingFail := err != nil && strings.Contains(out, "100% packet loss")

	// TODO better logging and handling of errors
	slog.Debug("Ping result", "from", from, "to", to,
		"expected", expected, "ok", pingOk, "fail", pingFail, "err", err, "out", out)

	if pingOk == pingFail {
		if err != nil {
			return fmt.Errorf("running ping: %w: %s", err, out) // TODO replace with custom error?
		}

		return fmt.Errorf("unexpected ping result (expected %t): %s", expected, out) // TODO replace with custom error?
	}

	if expected && !pingOk {
		return fmt.Errorf("should be reachable but ping failed with output: %s", out) // TODO replace with custom error?
	}

	if !expected && !pingFail {
		return fmt.Errorf("should not be reachable but ping succeeded with output: %s", out) // TODO replace with custom error?
	}

	// TODO other cases to handle?

	return nil
}

func checkIPerf(ctx context.Context, opts TestConnectivityOpts, iperfs *semaphore.Weighted, from, to string, fromSSH, toSSH *goph.Client, toIP netip.Addr, expected bool) error {
	if opts.IPerfsSeconds <= 0 || !expected {
		return nil
	}

	if err := iperfs.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("acquiring iperf3 semaphore: %w", err)
	}
	defer iperfs.Release(1)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.IPerfsSeconds+30)*time.Second)
	defer cancel()

	slog.Debug("Running iperf3", "from", from, "to", to)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		out, err := toSSH.RunContext(ctx, fmt.Sprintf("toolbox -q timeout -v %d iperf3 -s -1", opts.IPerfsSeconds+25))
		if err != nil {
			return fmt.Errorf("running iperf3 server: %w: %s", err, string(out))
		}

		return nil
	})

	g.Go(func() error {
		// We could netcat to check if the server is up, but that will make the server shut down if
		// it was started with -1, and if we don't add -1 it will run until the timeout, so change approach
		time.Sleep(1 * time.Second)
		var err error
		var out []byte
		cmd := fmt.Sprintf("toolbox -q timeout -v %d iperf3 -P 4 -J -c %s -t %d", opts.IPerfsSeconds+25, toIP.String(), opts.IPerfsSeconds)
		maxRetries := 3
		for retries := 0; retries < maxRetries; retries++ {
			// Run iperf3 client
			out, err = fromSSH.RunContext(ctx, cmd)
			if err == nil {
				break
			}
			// it doesn't look like the goph client defines error types to check with errors.Is
			if strings.Contains(err.Error(), "ssh:") {
				slog.Debug("iperf3 server not ready", "server", to, "retry", retries+1, "error", err, "output", string(out))
				if retries < maxRetries-1 {
					slog.Debug("Retrying in 1 second...")
					time.Sleep(1 * time.Second)

					continue
				} else {
					return fmt.Errorf("running iperf3 client: failed after %d retries: %w: %s", maxRetries, err, string(out))
				}
			} else {
				return fmt.Errorf("running iperf3 client: %w: %s", err, string(out))
			}
		}

		report, err := parseIPerf3Report(out)
		if err != nil {
			return fmt.Errorf("parsing iperf3 report: %w", err)
		}

		slog.Debug("IPerf3 result", "from", from, "to", to,
			"sendSpeed", asMbps(report.End.SumSent.BitsPerSecond),
			"receiveSpeed", asMbps(report.End.SumReceived.BitsPerSecond),
			"sent", asMB(float64(report.End.SumSent.Bytes)),
			"received", asMB(float64(report.End.SumReceived.Bytes)),
		)

		if opts.IPerfsMinSpeed > 0 {
			if report.End.SumSent.BitsPerSecond < opts.IPerfsMinSpeed*1_000_000 {
				return fmt.Errorf("iperf3 send speed too low: %s < %s", asMbps(report.End.SumSent.BitsPerSecond), asMbps(opts.IPerfsMinSpeed*1_000_000))
			}
			if report.End.SumReceived.BitsPerSecond < opts.IPerfsMinSpeed*1_000_000 {
				return fmt.Errorf("iperf3 receive speed too low: %s < %s", asMbps(report.End.SumReceived.BitsPerSecond), asMbps(opts.IPerfsMinSpeed*1_000_000))
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("running iperf3: %w", err)
	}

	return nil
}

func checkCurl(ctx context.Context, opts TestConnectivityOpts, curls *semaphore.Weighted, from string, fromSSH *goph.Client, toIP string, expected bool) error {
	if opts.CurlsCount <= 0 {
		return nil
	}

	if err := curls.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("acquiring curl semaphore: %w", err)
	}
	defer curls.Release(1)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(5*opts.CurlsCount+30)*time.Second)
	defer cancel()

	slog.Debug("Running curls", "from", from, "to", toIP, "count", opts.CurlsCount)

	for idx := 0; idx < opts.CurlsCount; idx++ {
		outR, err := fromSSH.RunContext(ctx, "timeout -v 5 curl --insecure --connect-timeout 3 --silent http://"+toIP)
		out := strings.TrimSpace(string(outR))

		curlOk := err == nil && strings.Contains(out, "301 Moved")
		curlFail := err != nil && !strings.Contains(out, "301 Moved")

		slog.Debug("Curl result", "from", from, "to", toIP, "expected", expected, "ok", curlOk, "fail", curlFail, "err", err, "out", out)

		if curlOk == curlFail {
			if err != nil {
				return fmt.Errorf("running curl (expected %t): %w: %s", expected, err, out) // TODO replace with custom error?
			}

			return fmt.Errorf("unexpected curl result (expected %t): %s", expected, out)
		}

		if expected && !curlOk {
			return fmt.Errorf("should be reachable but curl failed with output: %s", out)
		}

		if !expected && !curlFail {
			return fmt.Errorf("should not be reachable but curl succeeded with output: %s", out)
		}

		// TODO better handle other cases?
	}

	return nil
}

type iperf3Report struct {
	Intervals []iperf3ReportInterval `json:"intervals"`
	End       iperf3ReportEnd        `json:"end"`
}

type iperf3ReportInterval struct {
	Sum iperf3ReportSum `json:"sum"`
}

type iperf3ReportEnd struct {
	SumSent     iperf3ReportSum `json:"sum_sent"`
	SumReceived iperf3ReportSum `json:"sum_received"`
}

type iperf3ReportSum struct {
	Bytes         int64   `json:"bytes"`
	BitsPerSecond float64 `json:"bits_per_second"`
}

func parseIPerf3Report(data []byte) (*iperf3Report, error) {
	report := &iperf3Report{}
	if err := json.Unmarshal(data, report); err != nil {
		return nil, fmt.Errorf("unmarshaling iperf3 report: %w", err)
	}

	return report, nil
}

func asMbps(in float64) string {
	return fmt.Sprintf("%.2f Mbps", in/1_000_000)
}

func asMB(in float64) string {
	return fmt.Sprintf("%.2f MB", in/1000/1000)
}

func VLANsFrom(ranges ...meta.VLANRange) iter.Seq[uint16] {
	return func(yield func(uint16) bool) {
		for _, vlanRange := range ranges {
			for vlan := vlanRange.From; vlan <= vlanRange.To; vlan++ {
				if !yield(vlan) {
					return
				}
			}
		}
	}
}

func AddrsFrom(prefixes ...netip.Prefix) iter.Seq[netip.Prefix] {
	return func(yield func(netip.Prefix) bool) {
		for _, prefix := range prefixes {
			for addr := prefix.Masked().Addr(); addr.IsValid() && prefix.Contains(addr); addr = addr.Next() {
				if !yield(netip.PrefixFrom(addr, prefix.Bits())) {
					return
				}
			}
		}
	}
}

func SubPrefixesFrom(bits int, prefixes ...netip.Prefix) iter.Seq[netip.Prefix] {
	return func(yield func(netip.Prefix) bool) {
		for _, prefix := range prefixes {
			if bits < prefix.Bits() || !prefix.Addr().Is4() {
				continue
			}

			addr := prefix.Masked().Addr()
			addrBytes := addr.AsSlice()
			addrUint := binary.BigEndian.Uint32(addrBytes)
			ok := true

			for ok && prefix.Contains(addr) {
				if !yield(netip.PrefixFrom(addr, bits)) {
					return
				}

				addrUint += 1 << uint(32-bits)
				binary.BigEndian.PutUint32(addrBytes, addrUint)
				addr, ok = netip.AddrFromSlice(addrBytes)
			}
		}
	}
}

func CollectN[E any](n int, seq iter.Seq[E]) []E {
	res := make([]E, n)

	idx := 0
	for v := range seq {
		if idx >= n {
			break
		}

		res[idx] = v
		idx++
	}

	if idx == 0 {
		return nil
	}

	return res[:idx:idx]
}

type InspectOpts struct {
	WaitAppliedFor time.Duration
	Strict         bool
}

func (c *Config) Inspect(ctx context.Context, vlab *VLAB, opts InspectOpts) error {
	slog.Info("Inspecting fabric")

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	start := time.Now()

	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	cacheCancel, kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
		fabapi.SchemeBuilder,
		&scheme.Builder{
			GroupVersion:  coreapi.SchemeGroupVersion,
			SchemeBuilder: coreapi.SchemeBuilder,
		},
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	fail := false

	slog.Info("Waiting for switches ready before inspecting")
	if err := WaitSwitchesReady(ctx, kube, opts.WaitAppliedFor, 30*time.Minute); err != nil {
		slog.Error("Failed to wait for switches ready", "err", err)
		fail = true
	}

	var lldpOut inspect.Out
	var lldpErr error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			slog.Info("Retry attempt", "number", attempt+1)
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(10 * time.Second):
			}
		}

		lldpOut, lldpErr = inspect.LLDP(ctx, kube, inspect.LLDPIn{
			Strict:   opts.Strict,
			Fabric:   true,
			External: true,
			Server:   true,
		})

		if lldpErr == nil {
			if withErrors, ok := lldpOut.(inspect.WithErrors); ok {
				if len(withErrors.Errors()) == 0 {
					break
				}
			} else {
				break
			}
		}
	}

	if lldpErr != nil {
		slog.Error("Failed to inspect LLDP", "err", lldpErr)
		fail = true
	} else if renderErr := inspect.Render(inspect.OutputTypeText, os.Stdout, lldpOut); renderErr != nil {
		slog.Error("Inspecting LLDP reveals some errors", "err", renderErr)
		fail = true
	}

	if out, err := inspect.BGP(ctx, kube, inspect.BGPIn{
		Strict: opts.Strict,
	}); err != nil {
		slog.Error("Failed to inspect BGP", "err", err)
		fail = true
	} else if renderErr := inspect.Render(inspect.OutputTypeText, os.Stdout, out); renderErr != nil {
		slog.Error("Inspecting BGP reveals some errors", "err", renderErr)
		fail = true
	}

	if fail {
		slog.Error("Failed to inspect fabric", "took", time.Since(start))

		return fmt.Errorf("failed to inspect fabric")
	}

	slog.Info("Inspect completed", "took", time.Since(start))

	return nil
}

type ReleaseTestOpts struct {
	Regexes     []string
	InvertRegex bool
	ResultsFile string
	HhfabBin    string
	Extended    bool
	FailFast    bool
	PauseOnFail bool
}

func (c *Config) ReleaseTest(ctx context.Context, opts ReleaseTestOpts) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}
	opts.HhfabBin = self

	return RunReleaseTestSuites(ctx, c.WorkDir, c.CacheDir, opts)
}
