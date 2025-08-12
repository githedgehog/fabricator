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
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/melbahja/goph"
	"github.com/samber/lo"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	gwintapi "go.githedgehog.com/gateway/api/gwint/v1alpha1"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	coreapi "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const (
	ServerNamePrefix        = "server-"
	HashPolicyL2            = "layer2"
	HashPolicyL2And3        = "layer2+3"
	HashPolicyL3And4        = "layer3+4"
	HashPolicyEncap2And3    = "encap2+3"
	HashPolicyEncap3And4    = "encap3+4"
	HashPolicyVLANAndSrcMAC = "vlan+srcmac"
)

var HashPolicies = []string{
	HashPolicyL2,
	HashPolicyL2And3,
	HashPolicyL3And4,
	HashPolicyEncap2And3,
	HashPolicyEncap3And4,
	HashPolicyVLANAndSrcMAC,
}

var schemeBuilders = []*scheme.Builder{
	wiringapi.SchemeBuilder,
	vpcapi.SchemeBuilder,
	agentapi.SchemeBuilder,
	fabapi.SchemeBuilder,
	gwapi.SchemeBuilder,
	gwintapi.SchemeBuilder,
	{
		GroupVersion:  coreapi.SchemeGroupVersion,
		SchemeBuilder: coreapi.SchemeBuilder,
	},
}

func getKubeClientWithCache(ctx context.Context, workDir string) (context.CancelFunc, client.Client, error) {
	kubeconfig := filepath.Join(workDir, VLABDir, VLABKubeConfig)

	return kubeutil.NewClientWithCache(ctx, kubeconfig, schemeBuilders...)
}

func (c *Config) Wait(ctx context.Context, vlab *VLAB) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	if err := WaitReady(ctx, kube, WaitReadyOpts{}); err != nil {
		return fmt.Errorf("waiting for ready: %w", err)
	}

	return nil
}

type WaitReadyOpts struct {
	AppliedFor   time.Duration
	Timeout      time.Duration
	PollInterval time.Duration
	PrintEvery   int
}

func WaitReady(ctx context.Context, kube client.Reader, opts WaitReadyOpts) error {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	if opts.PollInterval == 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.PrintEvery == 0 {
		opts.PrintEvery = 6 // every ~30 seconds
	}

	start := time.Now()

	slog.Info("Waiting for switches and gateways ready", "appliedFor", opts.AppliedFor, "timeout", opts.Timeout)

	f, _, _, err := fab.GetFabAndNodes(ctx, kube, fab.GetFabAndNodesOpts{AllowNotHydrated: true})
	if err != nil {
		return fmt.Errorf("getting fab: %w", err)
	}

	// fabric controller will set agent version to its own version by default
	expectedSwAgentV := string(f.Status.Versions.Fabric.Controller)
	expectedSwitches := []string{}
	{
		switches := &wiringapi.SwitchList{}
		if err := kube.List(ctx, switches); err != nil {
			return fmt.Errorf("listing switches: %w", err)
		}
		for _, sw := range switches.Items {
			expectedSwitches = append(expectedSwitches, sw.Name)
		}
	}
	if len(expectedSwitches) > 0 {
		slog.Info("Expected switches", "agent", expectedSwAgentV, "switches", expectedSwitches)
	}

	// gateway controller will set agent version to its own version by default
	expectedGwAgentV := string(f.Status.Versions.Gateway.Controller)
	expectedGateways := []string{}
	if f.Spec.Config.Gateway.Enable {
		gateways := &gwapi.GatewayList{}
		if err := kube.List(ctx, gateways); err != nil {
			return fmt.Errorf("listing gateways: %w", err)
		}
		for _, gw := range gateways.Items {
			expectedGateways = append(expectedGateways, gw.Name)
		}
	}
	if len(expectedGateways) > 0 {
		slog.Info("Expected gateways", "agent", expectedGwAgentV, "gateways", expectedGateways)
	}

	swNotReady := []string{}
	gwNotReady := []string{}
	swNotUpdated := []string{}
	gwNotUpdated := []string{}

	for idx := 0; ; idx++ {
		swNotReady = []string{}
		gwNotReady = []string{}
		swNotUpdated = []string{}
		gwNotUpdated = []string{}

		for _, swName := range expectedSwitches {
			ag := &agentapi.Agent{}
			if err := kube.Get(ctx, client.ObjectKey{Name: swName, Namespace: metav1.NamespaceDefault}, ag); err != nil {
				if apierrors.IsNotFound(err) {
					slog.Warn("Switch agent not found", "name", swName)
				} else {
					slog.Warn("Failed to get switch agent", "name", swName, "error", err.Error())
				}

				swNotReady = append(swNotReady, swName)

				continue
			}

			// make sure that desired agent version is the same as we expect (same as controller version, may be different if not reconciled)
			ready := ag.Spec.Version.Default == expectedSwAgentV

			// make sure last applied generation is the same as current generation
			ready = ready && ag.Generation > 0 && ag.Status.LastAppliedGen == ag.Generation

			// make sure last heartbeat is recent enough
			ready = ready && time.Since(ag.Status.LastHeartbeat.Time) < 1*time.Minute

			if opts.AppliedFor > 0 {
				// make sure agent config was applied for long enough
				ready = ready && !ag.Status.LastAppliedTime.IsZero() && time.Since(ag.Status.LastAppliedTime.Time) >= opts.AppliedFor
			}

			if ready {
				if ag.Status.Version == expectedSwAgentV {
					continue
				}

				swNotUpdated = append(swNotUpdated, swName)
			} else {
				swNotReady = append(swNotReady, swName)
			}
		}

		if len(swNotReady) == 0 && len(swNotUpdated) > 0 {
			slices.Sort(swNotUpdated)
			slog.Warn("All switches ready, but some not updated", "notUpdated", swNotUpdated)

			return fmt.Errorf("all switches ready but some not updated")
		} else if idx%opts.PrintEvery == 0 && (len(swNotReady) > 0 || len(swNotUpdated) > 0) {
			slog.Info("Switches status", "notReady", swNotReady, "notUpdated", swNotUpdated)
		}

		for _, gwName := range expectedGateways {
			gwag := &gwintapi.GatewayAgent{}
			if err := kube.Get(ctx, client.ObjectKey{Name: gwName, Namespace: comp.FabNamespace}, gwag); err != nil {
				if apierrors.IsNotFound(err) {
					slog.Warn("Gateway agent not found", "name", gwName)
				} else {
					slog.Warn("Failed to get gateway agent", "name", gwName, "error", err.Error())
				}

				gwNotReady = append(gwNotReady, gwName)

				continue
			}

			// make sure that desired agent version is the same as we expect (same as controller version, may be different if not reconciled)
			ready := gwag.Spec.AgentVersion == expectedGwAgentV

			// make sure last applied generation is the same as current generation
			ready = ready && gwag.Generation > 0 && gwag.Status.LastAppliedGen == gwag.Generation

			// TODO consider adding heartbeats
			// make sure last heartbeat is recent enough
			// ready = ready && time.Since(gwag.Status.LastHeartbeat.Time) < 1*time.Minute

			if opts.AppliedFor > 0 {
				// make sure agent config was applied for long enough
				ready = ready && !gwag.Status.LastAppliedTime.IsZero() && time.Since(gwag.Status.LastAppliedTime.Time) >= opts.AppliedFor
			}

			if ready {
				if gwag.Status.AgentVersion == expectedGwAgentV {
					continue
				}

				gwNotUpdated = append(gwNotUpdated, gwName)
			} else {
				gwNotReady = append(gwNotReady, gwName)
			}
		}
		if len(gwNotReady) == 0 && len(gwNotUpdated) > 0 {
			slices.Sort(gwNotUpdated)
			slog.Warn("All gateways ready, but some not updated", "notUpdated", gwNotUpdated)

			return fmt.Errorf("all gateways ready but some not updated")
		} else if idx%opts.PrintEvery == 0 && (len(gwNotReady) > 0 || len(gwNotUpdated) > 0) {
			slog.Info("Gateways status", "notReady", gwNotReady, "notUpdated", gwNotUpdated)
		}

		if len(swNotReady) == 0 && len(gwNotReady) == 0 {
			slog.Info("All switches and gateways are ready", "took", time.Since(start))

			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for ready: %w", ctx.Err())
		case <-time.After(opts.PollInterval):
		}
	}
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
	HashPolicy        string
	VPCMode           vpcapi.VPCMode
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

func GetServerNetconfCmd(conn *wiringapi.Connection, vlan uint16, hashPolicy string) (string, error) {
	if conn == nil {
		return "", fmt.Errorf("connection is nil")
	}

	var netconfCmd string

	if conn.Spec.Unbundled != nil {
		netconfCmd = fmt.Sprintf("vlan %d %s", vlan, conn.Spec.Unbundled.Link.Server.LocalPortName())
	} else {
		netconfCmd = fmt.Sprintf("bond %d %s", vlan, hashPolicy)

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
			return "", fmt.Errorf("unexpected connection type for conn %q", conn.Name)
		}
	}

	return netconfCmd, nil
}

type ServerAttachState struct {
	ServerName string
	Attached   bool
	ESLAG      bool
	L3VNI      bool
}

func getServerAttachState(ctx context.Context, kube client.Client, server *wiringapi.Server, checkVPCMode bool) (ServerAttachState, error) {
	sa := ServerAttachState{
		ServerName: server.Name,
	}
	if server == nil {
		return sa, fmt.Errorf("server is nil")
	}

	conns := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, conns, wiringapi.MatchingLabelsForListLabelServer(server.Name)); err != nil {
		return sa, fmt.Errorf("listing connections for server %q: %w", server.Name, err)
	}

	if len(conns.Items) == 0 {
		return sa, fmt.Errorf("no connections for server %q", server.Name)
	}

	for _, conn := range conns.Items {
		if conn.Spec.ESLAG != nil {
			sa.ESLAG = true
		}
		// get the VPC this server is attached to
		attaches := &vpcapi.VPCAttachmentList{}
		if err := kube.List(ctx, attaches, kclient.MatchingLabels{wiringapi.LabelConnection: conn.Name}); err != nil {
			return sa, fmt.Errorf("listing VPC attachments for connection %q: %w", conn.Name, err)
		}
		numAttaches := len(attaches.Items)
		if numAttaches == 0 {
			continue
		} else if numAttaches > 1 {
			return sa, fmt.Errorf("expected at most one VPC attachment for connection %q, got %d", conn.Name, numAttaches)
		}
		sa.Attached = true
		if checkVPCMode {
			attach := attaches.Items[0]
			vpcName := attach.Spec.VPCName()
			vpc := &vpcapi.VPC{}
			if err := kube.Get(ctx, client.ObjectKey{Name: vpcName, Namespace: metav1.NamespaceDefault}, vpc); err != nil {
				return sa, fmt.Errorf("getting VPC %q: %w", vpcName, err)
			}
			if vpc.Spec.Mode != vpcapi.VPCModeL2VNI {
				sa.L3VNI = true
			}
		}

		break
	}

	return sa, nil
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
	if !slices.Contains(HashPolicies, opts.HashPolicy) {
		return fmt.Errorf("invalid hash policy %q, must be one of %v", opts.HashPolicy, HashPolicies)
	} else if opts.HashPolicy != HashPolicyL2 && opts.HashPolicy != HashPolicyL2And3 {
		slog.Warn("The selected hash policy is not fully 802.3ad compliant, use layer2 or layer2+3 for full compliance", "hashPolicy", opts.HashPolicy)
	}
	if !slices.Contains(vpcapi.VPCModes, opts.VPCMode) {
		return fmt.Errorf("invalid VPC mode %q, must be one of %v", opts.VPCMode, vpcapi.VPCModes)
	}

	slog.Info("Setting up VPCs and VPCAttachments",
		"mode", opts.VPCMode,
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

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
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
	eslagServers := make(map[string]bool, 0)
	for _, server := range servers.Items {
		if opts.VPCMode != vpcapi.VPCModeL2VNI {
			if sa, err := getServerAttachState(ctx, kube, &server, false); err != nil {
				return fmt.Errorf("checking server %q attachment state: %w", server.Name, err)
			} else if sa.ESLAG {
				eslagServers[server.Name] = true
				slog.Warn("Skipping ESLAG-connected server", "server", server.Name)

				continue
			}
		}
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
					Mode:    opts.VPCMode,
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

		netconfCmd, netconfErr := GetServerNetconfCmd(&conn, vlan, opts.HashPolicy)
		if netconfErr != nil {
			return fmt.Errorf("getting netconf cmd for server %q: %w", server.Name, netconfErr)
		}

		netconfs[server.Name] = netconfCmd
	}

	if opts.WaitSwitchesReady {
		if err := WaitReady(ctx, kube, WaitReadyOpts{AppliedFor: 15 * time.Second, Timeout: 10 * time.Minute}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
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
		// TODO remove it when we can actually know that changes to VPC/VPCAttachment are reflected in agents
		select {
		case <-ctx.Done():
			return fmt.Errorf("sleeping before waiting for ready: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}

		if err := WaitReady(ctx, kube, WaitReadyOpts{AppliedFor: 15 * time.Second, Timeout: 10 * time.Minute}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}
	}

	slog.Info("Configuring networking on servers")

	g := &errgroup.Group{}
	for _, server := range servers.Items {
		if _, ok := eslagServers[server.Name]; ok {
			continue
		}
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
				expectedBits := 0
				switch opts.VPCMode {
				case vpcapi.VPCModeL2VNI:
					expectedBits = expectedSubnet.Bits()
				case vpcapi.VPCModeL3Flat, vpcapi.VPCModeL3VNI:
					expectedBits = 32 // L3 modes always uses /32 for server addresses
				}
				if !expectedSubnet.Contains(prefix.Addr()) || prefix.Bits() != expectedBits {
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

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	if opts.WaitSwitchesReady {
		if err := WaitReady(ctx, kube, WaitReadyOpts{AppliedFor: 15 * time.Second, Timeout: 10 * time.Minute}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}
	}

	vpcsList := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcsList); err != nil {
		return fmt.Errorf("listing VPCs: %w", err)
	}

	vpcs := map[string]*vpcapi.VPC{}
	for _, vpc := range vpcsList.Items {
		vpcs[vpc.Name] = &vpc
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
	gwPeerings := map[string]*gwapi.PeeringSpec{}

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

			remote := ""
			gw := false
			vpc1Subnets := []string{}
			vpc2Subnets := []string{}
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

						remote = switchGroupList.Items[0].Name
					} else {
						remote = optValue
					}
				} else if optName == "gw" || optName == "gateway" {
					gw = true
				} else if optName == "vpc1" || optName == "vpc1-subnets" {
					vpc1Subnets = strings.Split(optValue, ",")
				} else if optName == "vpc2" || optName == "vpc2-subnets" {
					vpc2Subnets = strings.Split(optValue, ",")
				} else {
					return fmt.Errorf("invalid peering option #%d %s", idx, option)
				}
			}

			if !gw {
				vpcPeerings[fmt.Sprintf("%s--%s", vpc1, vpc2)] = &vpcapi.VPCPeeringSpec{
					Permit: []map[string]vpcapi.VPCPeer{
						{
							vpc1: {
								Subnets: vpc1Subnets,
							},
							vpc2: {
								Subnets: vpc2Subnets,
							},
						},
					},
					Remote: remote,
				}
			} else {
				if remote != "" {
					return fmt.Errorf("gateway peering connot be remote")
				}

				vpc1Expose := gwapi.PeeringEntryExpose{}
				if vpc, ok := vpcs[vpc1]; ok {
					for subnetName, subnet := range vpc.Spec.Subnets {
						if len(vpc1Subnets) > 0 && !slices.Contains(vpc1Subnets, subnetName) {
							continue
						}

						vpc1Expose.IPs = append(vpc1Expose.IPs, gwapi.PeeringEntryIP{CIDR: subnet.Subnet})
					}
				}

				vpc2Expose := gwapi.PeeringEntryExpose{}
				if vpc, ok := vpcs[vpc2]; ok {
					for subnetName, subnet := range vpc.Spec.Subnets {
						if len(vpc2Subnets) > 0 && !slices.Contains(vpc2Subnets, subnetName) {
							continue
						}

						vpc2Expose.IPs = append(vpc2Expose.IPs, gwapi.PeeringEntryIP{CIDR: subnet.Subnet})
					}
				}

				gwPeerings[fmt.Sprintf("%s--%s", vpc1, vpc2)] = &gwapi.PeeringSpec{
					Peering: map[string]*gwapi.PeeringEntry{
						vpc1: {
							Expose: []gwapi.PeeringEntryExpose{vpc1Expose},
						},
						vpc2: {
							Expose: []gwapi.PeeringEntryExpose{vpc2Expose},
						},
					},
				}
			}
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

	if err := DoSetupPeerings(ctx, kube, vpcPeerings, externalPeerings, gwPeerings, opts.WaitSwitchesReady); err != nil {
		return err
	}
	slog.Info("VPC and External Peerings setup complete", "took", time.Since(start))

	return nil
}

func DoSetupPeerings(ctx context.Context, kube client.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec, externalPeerings map[string]*vpcapi.ExternalPeeringSpec, gwPeerings map[string]*gwapi.PeeringSpec, waitReady bool) error {
	f, _, _, err := fab.GetFabAndNodes(ctx, kube, fab.GetFabAndNodesOpts{AllowNotHydrated: true})
	if err != nil {
		return fmt.Errorf("getting fab: %w", err)
	}

	if !f.Spec.Config.Gateway.Enable && len(gwPeerings) > 0 {
		return fmt.Errorf("gateway peerings are not supported when gateway is not enabled")
	}

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

		if err := client.IgnoreNotFound(kube.Delete(ctx, &peering)); err != nil {
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

		if err := client.IgnoreNotFound(kube.Delete(ctx, &peering)); err != nil {
			return fmt.Errorf("deleting external peering %s: %w", peering.Name, err)
		}
	}

	gwPeeringList := &gwapi.PeeringList{}
	if f.Spec.Config.Gateway.Enable {
		if err := kube.List(ctx, gwPeeringList); err != nil {
			return fmt.Errorf("listing gateway peerings: %w", err)
		}
		for _, peering := range gwPeeringList.Items {
			if gwPeerings[peering.Name] != nil {
				continue
			}

			slog.Info("Deleting GatewayPeering", "name", peering.Name)
			changed = true

			if err := client.IgnoreNotFound(kube.Delete(ctx, &peering)); err != nil {
				return fmt.Errorf("deleting gateway peering %s: %w", peering.Name, err)
			}
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

	for name, gwPeeringSpec := range gwPeerings {
		vpcs := lo.Keys(gwPeeringSpec.Peering)
		if len(vpcs) != 2 {
			return fmt.Errorf("invalid GatewayPeering %s: expected 2 VPCs, got %d", name, len(vpcs))
		}

		slog.Info("Enforcing GatewayPeering", "name", name, "vpc1", vpcs[0], "vpc2", vpcs[1])

		gwPeering := &gwapi.Peering{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: metav1.NamespaceDefault,
			},
		}
		res, err := ctrlutil.CreateOrUpdate(ctx, kube, gwPeering, func() error {
			gwPeering.Spec = *gwPeeringSpec

			return nil
		})
		if err != nil {
			return fmt.Errorf("error updating gateway peering %s: %w", name, err)
		}

		if res == ctrlutil.OperationResultCreated {
			slog.Info("Created", "gwpeering", name)
			changed = true
		} else if res == ctrlutil.OperationResultUpdated {
			slog.Info("Updated", "gwpeering", name)
			changed = true
		}
	}

	if changed && waitReady {
		// TODO remove it when we can actually know that changes to VPC/VPCAttachment are reflected in agents
		select {
		case <-ctx.Done():
			return fmt.Errorf("sleeping before waiting for ready: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}

		if err := WaitReady(ctx, kube, WaitReadyOpts{AppliedFor: 15 * time.Second, Timeout: 10 * time.Minute}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
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
	IPerfsDSCP        uint8
	IPerfsTOS         uint8
	CurlsCount        int
	CurlsParallel     int64
	Sources           []string
	Destinations      []string
	RequireAllServers bool
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

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
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
		slog.Warn("All switches are virtual, ignoring IPerf min speed")
		opts.IPerfsMinSpeed = 0.01
	}

	if opts.WaitSwitchesReady {
		if err := WaitReady(ctx, kube, WaitReadyOpts{AppliedFor: 15 * time.Second, Timeout: 10 * time.Minute}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
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
		if sa, err := getServerAttachState(ctx, kube, &server, true); err != nil {
			return fmt.Errorf("checking server %q attachment state: %w", server.Name, err)
		} else if !sa.Attached {
			if opts.RequireAllServers {
				return fmt.Errorf("server %q is not attached, but RequireAllServers is set", server.Name)
			}
			slog.Debug("Skipping non-attached server", "server", server.Name)

			continue
		} else if sa.ESLAG && sa.L3VNI {
			slog.Warn("Skipping server attached to an L3VNI VPC via ESLAG", "server", server.Name)

			continue
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

					if fields[0] == "lo" || fields[0] == "enp2s0" || fields[0] == "docker0" {
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
						expectedReachable, err := IsServerReachable(ctx, kube, serverA, serverB, c.Fab.Spec.Config.Gateway.Enable)
						if err != nil {
							return fmt.Errorf("checking if should be reachable: %w", err)
						}

						logArgs := []any{
							"from", serverA,
							"to", serverB,
							"expected", expectedReachable.Reachable,
						}
						if expectedReachable.Reachable {
							logArgs = append(logArgs, "reason", expectedReachable.Reason)
							if expectedReachable.Peering != "" {
								logArgs = append(logArgs, "peering", expectedReachable.Peering)
							}
						}
						slog.Debug("Checking connectivity", logArgs...)

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

						if err := checkPing(ctx, opts, pings, serverA, serverB, clientA, ipB.Addr(), expectedReachable.Reachable); err != nil {
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

					// switching to 1.0.0.1 since the previously used target 8.8.8.8 was giving us issue
					// when curling over virtual external peerings
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

type Reachability struct {
	Reachable bool
	Reason    ReachabilityReason
	Peering   string
}

type ReachabilityReason string

const (
	ReachabilityReasonIntraVPC       ReachabilityReason = "intra-vpc"
	ReachabilityReasonSwitchPeering  ReachabilityReason = "switch-peering"
	ReachabilityReasonGatewayPeering ReachabilityReason = "gateway-peering"
)

func IsServerReachable(ctx context.Context, kube kclient.Reader, sourceServer, destServer string, checkGateway bool) (Reachability, error) {
	sourceSubnets, err := apiutil.GetAttachedSubnets(ctx, kube, sourceServer)
	if err != nil {
		return Reachability{}, fmt.Errorf("getting attached subnets for source server %s: %w", sourceServer, err)
	}

	destSubnets, err := apiutil.GetAttachedSubnets(ctx, kube, destServer)
	if err != nil {
		return Reachability{}, fmt.Errorf("getting attached subnets for dest server %s: %w", destServer, err)
	}

	for sourceSubnetName := range sourceSubnets {
		for destSubnetName := range destSubnets {
			if r, err := IsSubnetReachable(ctx, kube, sourceSubnetName, destSubnetName, checkGateway); err != nil {
				return Reachability{}, err
			} else if r.Reachable {
				return r, nil
			}
		}
	}

	return Reachability{}, nil
}

func IsSubnetReachable(ctx context.Context, kube kclient.Reader, source, dest string, checkGateway bool) (Reachability, error) {
	if !strings.Contains(source, "/") {
		return Reachability{}, fmt.Errorf("source must be full VPC subnet name (<vpc-name>/<subnet-name>)")
	}

	if !strings.Contains(dest, "/") {
		return Reachability{}, fmt.Errorf("dest must be full VPC subnet name (<vpc-name>/<subnet-name>)")
	}

	sourceParts := strings.SplitN(source, "/", 2)
	destParts := strings.SplitN(dest, "/", 2)

	sourceVPC, sourceSubnet := sourceParts[0], sourceParts[1]
	destVPC, destSubnet := destParts[0], destParts[1]

	if sourceVPC == destVPC {
		reacheable, err := apiutil.IsSubnetReachableWithinVPC(ctx, kube, sourceVPC, sourceSubnet, destSubnet)
		if err != nil {
			return Reachability{}, fmt.Errorf("checking if subnets %s and %s are reachable within VPC %s: %w", source, dest, sourceVPC, err)
		}

		return Reachability{
			Reachable: reacheable,
			Reason:    ReachabilityReasonIntraVPC,
		}, nil
	}

	r, err := IsSubnetReachableWithSwitchPeering(ctx, kube, sourceVPC, sourceSubnet, destVPC, destSubnet)
	if err != nil {
		return Reachability{}, fmt.Errorf("checking if subnets %s and %s are reachable through fabric: %w", source, dest, err)
	}
	if r.Reachable {
		return r, nil
	}

	if checkGateway {
		r, err := IsSubnetReachableWithGatewayPeering(ctx, kube, sourceVPC, sourceSubnet, destVPC, destSubnet)
		if err != nil {
			return Reachability{}, fmt.Errorf("checking if subnets %s and %s are reachable through gateway: %w", source, dest, err)
		}

		if r.Reachable {
			return r, nil
		}
	}

	return Reachability{}, nil
}

func IsSubnetReachableWithSwitchPeering(ctx context.Context, kube kclient.Reader, vpc1Name, vpc1Subnet, vpc2Name, vpc2Subnet string) (Reachability, error) {
	if vpc1Name == vpc2Name {
		return Reachability{}, fmt.Errorf("VPCs %s and %s are the same", vpc1Name, vpc2Name)
	}

	vpc1 := vpcapi.VPC{}
	if err := kube.Get(ctx, kclient.ObjectKey{
		Namespace: kmetav1.NamespaceDefault,
		Name:      vpc1Name,
	}, &vpc1); err != nil {
		return Reachability{}, fmt.Errorf("failed to get VPC %s: %w", vpc1Name, err)
	}

	vpc2 := vpcapi.VPC{}
	if err := kube.Get(ctx, kclient.ObjectKey{
		Namespace: kmetav1.NamespaceDefault,
		Name:      vpc2Name,
	}, &vpc2); err != nil {
		return Reachability{}, fmt.Errorf("failed to get VPC %s: %w", vpc2Name, err)
	}

	if vpc1.Spec.Subnets[vpc1Subnet] == nil {
		return Reachability{}, fmt.Errorf("source subnet %s not found in VPC %s", vpc1Subnet, vpc1Name)
	}
	if vpc2.Spec.Subnets[vpc2Subnet] == nil {
		return Reachability{}, fmt.Errorf("destination subnet %s not found in VPC %s", vpc2Subnet, vpc2Name)
	}

	vpcPeerings := vpcapi.VPCPeeringList{}
	if err := kube.List(ctx, &vpcPeerings,
		kclient.InNamespace(kmetav1.NamespaceDefault),
		kclient.MatchingLabels{
			vpcapi.ListLabelVPC(vpc1Name): vpcapi.ListLabelValue,
			vpcapi.ListLabelVPC(vpc2Name): vpcapi.ListLabelValue,
		},
	); err != nil {
		return Reachability{}, fmt.Errorf("failed to list VPC peerings: %w", err)
	}

	for _, vpcPeering := range vpcPeerings.Items {
		if vpcPeering.Spec.Remote != "" {
			notEmpty, err := apiutil.IsVPCPeeringRemoteNotEmpty(ctx, kube, &vpcPeering)
			if err != nil {
				return Reachability{}, fmt.Errorf("failed to check if VPC peering %s has non-empty remote: %w", vpcPeering.Name, err)
			}

			if !notEmpty {
				continue
			}
		}

		for _, permit := range vpcPeering.Spec.Permit {
			vpc1Permit, exist := permit[vpc1Name]
			if !exist {
				continue
			}

			vpc2Permit, exist := permit[vpc2Name]
			if !exist {
				continue
			}

			vpc1SubnetContains := len(vpc1Permit.Subnets) == 0 || slices.Contains(vpc1Permit.Subnets, vpc1Subnet)
			vpc2SubnetContains := len(vpc2Permit.Subnets) == 0 || slices.Contains(vpc2Permit.Subnets, vpc2Subnet)

			if vpc1SubnetContains && vpc2SubnetContains {
				return Reachability{
					Reachable: true,
					Reason:    ReachabilityReasonSwitchPeering,
					Peering:   vpcPeering.Name,
				}, nil
			}
		}
	}

	return Reachability{}, nil
}

// It's just a temporary function for simple check only supporting whole VPC subnet CIDRs
func IsSubnetReachableWithGatewayPeering(ctx context.Context, kube kclient.Reader, vpc1Name, vpc1Subnet, vpc2Name, vpc2Subnet string) (Reachability, error) {
	if vpc1Name == vpc2Name {
		return Reachability{}, fmt.Errorf("VPCs %s and %s are the same", vpc1Name, vpc2Name)
	}

	vpc1 := gwapi.VPCInfo{}
	if err := kube.Get(ctx, kclient.ObjectKey{
		Namespace: kmetav1.NamespaceDefault,
		Name:      vpc1Name,
	}, &vpc1); err != nil {
		return Reachability{}, fmt.Errorf("failed to get VPC %s: %w", vpc1Name, err)
	}

	vpc2 := gwapi.VPCInfo{}
	if err := kube.Get(ctx, kclient.ObjectKey{
		Namespace: kmetav1.NamespaceDefault,
		Name:      vpc2Name,
	}, &vpc2); err != nil {
		return Reachability{}, fmt.Errorf("failed to get VPC %s: %w", vpc2Name, err)
	}

	if vpc1.Spec.Subnets[vpc1Subnet] == nil {
		return Reachability{}, fmt.Errorf("source subnet %s not found in VPC %s", vpc1Subnet, vpc1Name)
	}
	if vpc2.Spec.Subnets[vpc2Subnet] == nil {
		return Reachability{}, fmt.Errorf("destination subnet %s not found in VPC %s", vpc2Subnet, vpc2Name)
	}

	peerings := gwapi.PeeringList{}
	if err := kube.List(ctx, &peerings,
		kclient.InNamespace(kmetav1.NamespaceDefault),
		kclient.MatchingLabels{
			gwapi.ListLabelVPC(vpc1Name): gwapi.ListLabelValue,
			gwapi.ListLabelVPC(vpc2Name): gwapi.ListLabelValue,
		},
	); err != nil {
		return Reachability{}, fmt.Errorf("listing peerings between VPCs %s and %s: %w", vpc1Name, vpc2Name, err)
	}

	for _, peering := range peerings.Items {
		vpc1Peering, ok := peering.Spec.Peering[vpc1Name]
		if !ok {
			continue
		}

		vpc2Peering, ok := peering.Spec.Peering[vpc2Name]
		if !ok {
			continue
		}

		vpc1Found, err := isVPCSubnetPresentInPeering(vpc1Peering, vpc1, vpc1Name, vpc1Subnet)
		if err != nil {
			return Reachability{}, fmt.Errorf("checking if VPC %s subnet %s is present in peering %s: %w", vpc1Name, vpc1Subnet, peering.Name, err)
		}

		vpc2Found, err := isVPCSubnetPresentInPeering(vpc2Peering, vpc2, vpc2Name, vpc2Subnet)
		if err != nil {
			return Reachability{}, fmt.Errorf("checking if VPC %s subnet %s is present in peering %s: %w", vpc2Name, vpc2Subnet, peering.Name, err)
		}

		if vpc1Found && vpc2Found {
			return Reachability{
				Reachable: true,
				Reason:    ReachabilityReasonGatewayPeering,
				Peering:   peering.Name,
			}, nil
		}
	}

	return Reachability{}, nil
}

func isVPCSubnetPresentInPeering(peering *gwapi.PeeringEntry, vpc gwapi.VPCInfo, vpcName string, vpcSubnet string) (bool, error) {
	for _, expose := range peering.Expose {
		if len(expose.As) > 0 {
			return false, fmt.Errorf("expose as %s is not supported yet", expose.As)
		}

		for _, exposeEntry := range expose.IPs {
			// TODO make some helper in the gateway project
			exposeSubnetName := ""
			if exposeEntry.VPCSubnet != "" {
				if _, ok := vpc.Spec.Subnets[exposeEntry.VPCSubnet]; ok {
					exposeSubnetName = exposeEntry.VPCSubnet
				} else {
					return false, fmt.Errorf("subnet %s not found in VPC %s", exposeEntry.VPCSubnet, vpcName)
				}
			} else if exposeEntry.CIDR != "" {
				for subnetName, subnet := range vpc.Spec.Subnets {
					if subnet.CIDR == exposeEntry.CIDR {
						exposeSubnetName = subnetName
					}
				}
			} else {
				return false, fmt.Errorf("only cidr and vpcSubnet are supported as expose entries: %s", exposeEntry)
			}

			if exposeSubnetName == vpcSubnet {
				return true, nil
			}
		}
	}
	return false, nil
}

func retrySSHCmd(ctx context.Context, client *goph.Client, cmd string, target string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("ssh client is nil")
	}
	maxRetries := 3
	var out []byte
	var err error
	for retries := 0; retries < maxRetries; retries++ {
		out, err = client.RunContext(ctx, cmd)
		if err == nil {
			break
		}
		// it doesn't look like the goph client defines error types to check with errors.Is
		if strings.Contains(err.Error(), "ssh:") {
			slog.Debug("cannot ssh to run remote command", "cmd", cmd, "remote target", target, "retry", retries+1, "error", err, "output", string(out))
			if retries < maxRetries-1 {
				// random wait in [1, 5] seconds range
				waitTime := time.Duration(1000+rand.IntN(4000)) * time.Millisecond
				slog.Debug("Retrying after random wait time (between 1 and 5 seconds)", "waitTime", waitTime)
				time.Sleep(waitTime)

				continue
			} else {
				return out, fmt.Errorf("ssh for remote command failed after %d retries: %w: %s", maxRetries, err, string(out))
			}
		} else {
			return out, err
		}
	}

	return out, nil
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
	outR, err := retrySSHCmd(ctx, fromSSH, cmd, from)
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

func checkIPerf(ctx context.Context, opts TestConnectivityOpts, iperfs *semaphore.Weighted, from, to string, fromSSH, toSSH *goph.Client, toIP netip.Addr, reachability Reachability) error {
	if opts.IPerfsSeconds <= 0 || !reachability.Reachable {
		return nil
	}

	iPerfsMinSpeed := opts.IPerfsMinSpeed
	// TODO remove workaround after we have better GW performance
	if reachability.Reason == ReachabilityReasonGatewayPeering {
		iPerfsMinSpeed = 0.01
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
		cmd := fmt.Sprintf("toolbox -q timeout -v %d iperf3 -s -1", opts.IPerfsSeconds+25)
		if _, err := retrySSHCmd(ctx, toSSH, cmd, to); err != nil {
			return fmt.Errorf("running iperf3 server: %w", err)
		}

		return nil
	})

	g.Go(func() error {
		// We could netcat to check if the server is up, but that will make the server shut down if
		// it was started with -1, and if we don't add -1 it will run until the timeout
		time.Sleep(1 * time.Second)
		cmd := fmt.Sprintf("toolbox -q timeout -v %d iperf3 -P 4 -J -c %s -t %d", opts.IPerfsSeconds+25, toIP.String(), opts.IPerfsSeconds)

		// TODO remove workaround after we configure correct MTU on the Gateway ports
		if reachability.Reason == ReachabilityReasonGatewayPeering {
			cmd += " -M 1000"
		}

		if opts.IPerfsDSCP > 0 {
			cmd += fmt.Sprintf(" --dscp %d", opts.IPerfsDSCP)
		}
		if opts.IPerfsTOS > 0 {
			cmd += fmt.Sprintf(" --tos %d", opts.IPerfsTOS)
		}
		outR, err := retrySSHCmd(ctx, fromSSH, cmd, from)
		if err != nil {
			return fmt.Errorf("running iperf3 client: %w", err)
		}

		report, err := parseIPerf3Report(outR)
		if err != nil {
			return fmt.Errorf("parsing iperf3 report: %w", err)
		}

		slog.Debug("IPerf3 result", "from", from, "to", to,
			"sendSpeed", asMbps(report.End.SumSent.BitsPerSecond),
			"receiveSpeed", asMbps(report.End.SumReceived.BitsPerSecond),
			"sent", asMB(float64(report.End.SumSent.Bytes)),
			"received", asMB(float64(report.End.SumReceived.Bytes)),
		)

		if iPerfsMinSpeed > 0 {
			if report.End.SumSent.BitsPerSecond < iPerfsMinSpeed*1_000_000 {
				return fmt.Errorf("iperf3 send speed too low: %s < %s", asMbps(report.End.SumSent.BitsPerSecond), asMbps(iPerfsMinSpeed*1_000_000))
			}
			if report.End.SumReceived.BitsPerSecond < iPerfsMinSpeed*1_000_000 {
				return fmt.Errorf("iperf3 receive speed too low: %s < %s", asMbps(report.End.SumReceived.BitsPerSecond), asMbps(iPerfsMinSpeed*1_000_000))
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
		cmd := fmt.Sprintf("timeout -v 5 curl --insecure --connect-timeout 3 --silent http://%s", toIP)
		outR, err := retrySSHCmd(ctx, fromSSH, cmd, from)
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
	Attempts       int
}

func (c *Config) Inspect(ctx context.Context, vlab *VLAB, opts InspectOpts) error {
	slog.Info("Inspecting fabric")

	opts.Attempts = max(1, opts.Attempts)
	opts.Attempts = min(10, opts.Attempts)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	start := time.Now()

	cacheCancel, kube, err := getKubeClientWithCache(ctx, c.WorkDir)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}
	defer cacheCancel()

	fail := false

	if err := WaitReady(ctx, kube, WaitReadyOpts{AppliedFor: opts.WaitAppliedFor, Timeout: 30 * time.Minute}); err != nil {
		slog.Error("Failed to wait for ready", "err", err)
		fail = true
	}

	lldpIn := inspect.LLDPIn{
		Strict:   opts.Strict,
		Fabric:   true,
		External: true,
		Server:   true,
	}
	var lldpOut inspect.Out[inspect.LLDPIn]
	var lldpErr error

	for attempt := 0; attempt < opts.Attempts; attempt++ {
		if attempt > 0 {
			slog.Info("Retry attempt", "number", attempt+1)
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(10 * time.Second):
			}
		}

		lldpOut, lldpErr = inspect.LLDP(ctx, kube, lldpIn)

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
	} else if renderErr := inspect.Render(time.Now(), inspect.OutputTypeText, os.Stdout, lldpIn, lldpOut); renderErr != nil {
		slog.Warn("Inspecting LLDP reveals some errors", "err", renderErr)

		// LLDP seems to be not very stable lately, so don't fail on errors from it
		// fail = true
	}

	bgpIn := inspect.BGPIn{
		Strict: opts.Strict,
	}

	if bgpOut, err := inspect.BGP(ctx, kube, bgpIn); err != nil {
		slog.Error("Failed to inspect BGP", "err", err)
		fail = true
	} else if renderErr := inspect.Render(time.Now(), inspect.OutputTypeText, os.Stdout, bgpIn, bgpOut); renderErr != nil {
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
	HashPolicy  string
	VPCMode     vpcapi.VPCMode
}

func (c *Config) ReleaseTest(ctx context.Context, opts ReleaseTestOpts) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}
	opts.HhfabBin = self

	if !slices.Contains(HashPolicies, opts.HashPolicy) {
		return fmt.Errorf("invalid hash policy %q, must be one of %v", opts.HashPolicy, HashPolicies)
	} else if opts.HashPolicy != HashPolicyL2 && opts.HashPolicy != HashPolicyL2And3 {
		slog.Warn("The selected hash policy is not fully 802.3ad compliant, use layer2 or layer2+3 for full compliance", "hashPolicy", opts.HashPolicy)
	}
	if !slices.Contains(vpcapi.VPCModes, opts.VPCMode) {
		return fmt.Errorf("invalid VPC mode %q, must be one of %v", opts.VPCMode, vpcapi.VPCModes)
	}

	return RunReleaseTestSuites(ctx, c.WorkDir, c.CacheDir, opts)
}
