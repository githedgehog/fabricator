// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/binary"
	"fmt"
	"iter"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/melbahja/goph"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	ServerNamePrefix = "server-"
)

type SetupVPCsOpts struct {
	WaitSwitchesReady bool
	ForceCleanup      bool
	VLANNamespace     string
	IPv4Namespace     string
	ServersPerSubnet  int
	SubnetsPerVPC     int
	DNSServers        []string
	TimeServers       []string
}

func (c *Config) SetupVPCs(ctx context.Context, vlab *VLAB, opts SetupVPCsOpts) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	if opts.ServersPerSubnet <= 0 {
		return fmt.Errorf("servers per subnet must be positive")
	}
	if opts.SubnetsPerVPC <= 0 {
		return fmt.Errorf("subnets per VPC must be positive")
	}

	sshPorts := map[string]uint{}
	for _, vm := range vlab.VMs {
		sshPorts[vm.Name] = getSSHPort(vm.ID)
	}

	sshAuth, err := goph.RawKey(vlab.SSHKey, "")
	if err != nil {
		return fmt.Errorf("getting ssh auth: %w", err)
	}

	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}

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
			if len(opts.DNSServers) > 0 || len(opts.TimeServers) > 0 {
				dhcpOpts = &vpcapi.VPCDHCPOptions{
					DNSServers:  opts.DNSServers,
					TimeServers: opts.TimeServers,
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
		slog.Info("Waiting for switches ready")
		if err := waitSwitchesReady(ctx, kube); err != nil {
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
		some := &vpcapi.VPC{ObjectMeta: metav1.ObjectMeta{Name: vpc.Name, Namespace: vpc.Namespace}}
		res, err := ctrlutil.CreateOrUpdate(ctx, kube, some, func() error {
			some.Spec = vpc.Spec
			some.Default()

			return nil
		})
		if err != nil {
			return fmt.Errorf("creating or updating VPC %q: %w", vpc.Name, err)
		}

		switch res {
		case ctrlutil.OperationResultCreated:
			slog.Info("Created", "vpc", vpc.Name, "subnets", len(vpc.Spec.Subnets))
			changed = true
		case ctrlutil.OperationResultUpdated:
			slog.Info("Updated", "vpc", vpc.Name, "subnets", len(vpc.Spec.Subnets))
			changed = true
		}
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
			slog.Info("Created attachment", "attachment", attach.Name)
			changed = true
		case ctrlutil.OperationResultUpdated:
			slog.Info("Updated attachment", "attachment", attach.Name)
			changed = true
		}
	}

	if changed && opts.WaitSwitchesReady {
		slog.Info("Waiting for switches ready after configuring VPCs and VPCAttachments")

		// TODO remove it when we can actually know that changes to VPC/VPCAttachment are reflected in agents
		time.Sleep(15 * time.Second)

		if err := waitSwitchesReady(ctx, kube); err != nil {
			return fmt.Errorf("waiting for switches ready: %w", err)
		}
	}

	slog.Info("Configuring networking on servers")

	g := errgroup.Group{}
	for _, server := range servers.Items {
		g.Go(func() error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
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

	slog.Info("All servers configured and verified")

	return nil
}

func waitSwitchesReady(ctx context.Context, kube client.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for {
		switches := &wiringapi.SwitchList{}
		if err := kube.List(ctx, switches); err != nil {
			return fmt.Errorf("listing switches: %w", err)
		}

		readyList := []string{}
		notReadyList := []string{}

		allReady := true
		for _, sw := range switches.Items {
			ag := &agentapi.Agent{}
			if err := kube.Get(ctx, client.ObjectKey{Name: sw.Name, Namespace: sw.Namespace}, ag); err != nil {
				return fmt.Errorf("getting agent %q: %w", sw.Name, err)
			}

			ready := ag.Status.LastAppliedGen == ag.Generation && time.Since(ag.Status.LastHeartbeat.Time) < 1*time.Minute
			allReady = allReady && ready

			if ready {
				readyList = append(readyList, sw.Name)
			} else {
				notReadyList = append(notReadyList, sw.Name)
			}
		}

		slices.Sort(readyList)
		slices.Sort(notReadyList)

		slog.Info("Switches status", "ready", readyList, "notReady", notReadyList)

		if allReady {
			return nil
		}

		time.Sleep(15 * time.Second)
	}
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
