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
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	ServerNamePrefix = "server-"
	VSIPerfSpeed     = 3
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
		slog.Info("Waiting for switches ready before configuring VPCs and VPCAttachments")
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

	g := &errgroup.Group{}
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

type TestConnectivityOpts struct {
	WaitSwitchesReady bool
	PingsCount        int
	PingsParallel     int64
	IPerfsSeconds     int
	IPerfsMinSpeed    float64
	IPerfsParallel    int64
	CurlsCount        int
	CurlsParallel     int64
}

func (c *Config) TestConnectivity(ctx context.Context, vlab *VLAB, opts TestConnectivityOpts) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()

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
	kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig,
		wiringapi.SchemeBuilder,
		vpcapi.SchemeBuilder,
		agentapi.SchemeBuilder,
	)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}

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

		if err := waitSwitchesReady(ctx, kube); err != nil {
			return fmt.Errorf("waiting for switches ready: %w", err)
		}
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

	slog.Info("Discovering server IPs", "servers", len(servers.Items))

	ips := map[string]netip.Prefix{}
	sshs := map[string]*goph.Client{}
	defer func() {
		for _, client := range sshs {
			if err := client.Close(); err != nil {
				slog.Warn("Closing ssh client", "err", err)
			}
		}
	}()

	g := &errgroup.Group{}
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
				sshs[server.Name] = client

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
					ips[server.Name] = addr

					slog.Info("Found", "server", server.Name, "addr", addr.String())
				}

				if !found {
					return fmt.Errorf("no ip addr found")
				}

				return nil
			}(); err != nil {
				return fmt.Errorf("getting server %q IP: %w", server.Name, err)
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

	g = &errgroup.Group{}
	for _, serverA := range servers.Items {
		for _, serverB := range servers.Items {
			if serverA.Name == serverB.Name {
				continue
			}

			g.Go(func() error {
				if err := func() error {
					expectedReachable, err := apiutil.IsServerReachable(ctx, kube, serverA.Name, serverB.Name)
					if err != nil {
						return fmt.Errorf("checking if should be reachable: %w", err)
					}

					slog.Debug("Checking connectivity", "from", serverA.Name, "to", serverB.Name, "reachable", expectedReachable)

					ipB := ips[serverB.Name]
					clientA, clientB := sshs[serverA.Name], sshs[serverB.Name]

					if err := checkPing(ctx, opts, pings, serverA.Name, serverB.Name, clientA, ipB.Addr(), expectedReachable); err != nil {
						return fmt.Errorf("checking ping from %s to %s: %w", serverA.Name, serverB.Name, err)
					}

					if err := checkIPerf(ctx, opts, iperfs, serverA.Name, serverB.Name, clientA, clientB, ipB.Addr(), expectedReachable); err != nil {
						return fmt.Errorf("checking iperf from %s to %s: %w", serverA.Name, serverB.Name, err)
					}

					return nil
				}(); err != nil {
					return fmt.Errorf("testing connectivity from %q to %q: %w", serverA.Name, serverB.Name, err)
				}

				return nil
			})
		}

		g.Go(func() error {
			if err := func() error {
				reachable, err := apiutil.IsExternalSubnetReachable(ctx, kube, serverA.Name, "0.0.0.0/0") // TODO test for specific IP
				if err != nil {
					return fmt.Errorf("checking if should be reachable: %w", err)
				}

				slog.Debug("Checking external connectivity", "from", serverA.Name, "reachable", reachable)

				client, err := goph.NewConn(&goph.Config{
					User:     "core",
					Addr:     "127.0.0.1",
					Port:     sshPorts[serverA.Name],
					Auth:     sshAuth,
					Timeout:  10 * time.Second,
					Callback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
				})
				if err != nil {
					return fmt.Errorf("connecting to %q: %w", serverA.Name, err)
				}
				defer client.Close()

				if err := checkCurl(ctx, opts, curls, serverA.Name, client, "8.8.8.8", reachable); err != nil {
					return fmt.Errorf("checking curl from %q: %w", serverA.Name, err)
				}

				return nil
			}(); err != nil {
				return fmt.Errorf("testing connectivity from %q to external: %w", serverA.Name, err)
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("testing connectivity: %w", err)
	}

	slog.Info("All connectivity tested")

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

func checkPing(ctx context.Context, opts TestConnectivityOpts, pings *semaphore.Weighted, from, to string, fromSSH *goph.Client, toIP netip.Addr, expected bool) error {
	if opts.PingsCount <= 0 {
		return nil
	}

	if err := pings.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("acquiring ping semaphore: %w", err)
	}
	defer pings.Release(1)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.PingsCount+10)*time.Second)
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
		return fmt.Errorf("unexpected ping result: %s", out) // TODO replace with custom error?
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
	if opts.PingsCount <= 0 || !expected {
		return nil
	}

	if err := iperfs.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("acquiring iperf semaphore: %w", err)
	}
	defer iperfs.Release(1)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.IPerfsSeconds+10)*time.Second)
	defer cancel()

	slog.Debug("Running iperf", "from", from, "to", to)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		out, err := toSSH.RunContext(ctx, "toolbox -q iperf3 -s -1")
		if err != nil {
			return fmt.Errorf("running iperf server: %w: %s", err, string(out))
		}

		return nil
	})

	g.Go(func() error {
		time.Sleep(1 * time.Second) // TODO think about more reliable way to wait for server to start

		cmd := fmt.Sprintf("toolbox -q iperf3 -J -c %s -t %d", toIP.String(), opts.IPerfsSeconds)
		out, err := fromSSH.RunContext(ctx, cmd)
		if err != nil {
			return fmt.Errorf("running iperf client: %w: %s", err, string(out))
		}

		report, err := parseIPerf3Report(out)
		if err != nil {
			return fmt.Errorf("parsing iperf report: %w", err)
		}

		slog.Debug("IPerf3 result", "from", from, "to", to,
			"sendSpeed", asMbps(report.End.SumSent.BitsPerSecond),
			"receiveSpeed", asMbps(report.End.SumReceived.BitsPerSecond),
			"sent", asMB(float64(report.End.SumSent.Bytes)),
			"received", asMB(float64(report.End.SumReceived.Bytes)),
		)

		if opts.IPerfsMinSpeed > 0 {
			if report.End.SumSent.BitsPerSecond < opts.IPerfsMinSpeed*1_000_000 {
				return fmt.Errorf("iperf send speed too low: %s < %s", asMbps(report.End.SumSent.BitsPerSecond), asMbps(opts.IPerfsMinSpeed*1_000_000))
			}
			if report.End.SumReceived.BitsPerSecond < opts.IPerfsMinSpeed*1_000_000 {
				return fmt.Errorf("iperf receive speed too low: %s < %s", asMbps(report.End.SumReceived.BitsPerSecond), asMbps(opts.IPerfsMinSpeed*1_000_000))
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("running iperf: %w", err)
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

	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.CurlsCount+10)*time.Second)
	defer cancel()

	slog.Debug("Running curl", "from", from, "to", toIP)

	outR, err := fromSSH.RunContext(ctx, "timeout -v 5 curl --insecure https://"+toIP)
	out := strings.TrimSpace(string(outR))

	curlOk := err == nil && strings.Contains(out, "302 Moved")
	curlFail := err != nil && !strings.Contains(out, "302 Moved")

	if curlOk == curlFail {
		return fmt.Errorf("unexpected curl result: %s", out)
	}

	if expected && !curlOk {
		return fmt.Errorf("should be reachable but curl failed with output: %s", out)
	}

	if !expected && !curlFail {
		return fmt.Errorf("should not be reachable but curl succeeded with output: %s", out)
	}

	// TODO better handle other cases?

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
