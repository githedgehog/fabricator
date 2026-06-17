// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// dhcpIPChangeRecoveryThreshold is the maximum time a cross-leaf peer should take to
	// regain connectivity to a host after that host changes its DHCP IP within an L3VNI
	// anycast-gateway subnet. Healthy EVPN type-5 convergence is well under this. The
	// threshold sits between healthy convergence and the failure floor; calibrate it
	// against a known-good build for the target platform.
	dhcpIPChangeRecoveryThreshold = 30 * time.Second
	// dhcpIPChangeMaxWait bounds how long the test polls for recovery before declaring a
	// hard failure.
	dhcpIPChangeMaxWait = 4 * time.Minute
	// dhcpIPChangePrimingPings is the number of pings sent to the not-yet-assigned target
	// IP so the prober's leaf installs a failed neighbor on the SVI before the host appears.
	dhcpIPChangePrimingPings = 3
	// dhcpIPChangeStaticOffset is added to the subnet base address to pick an unused IP.
	dhcpIPChangeStaticOffset = 111
)

var (
	errNotL3VNI        = errors.New("test requires L3VNI VPC mode")
	errNoCrossLeafPair = errors.New("no two servers in the same subnet on different leaves")
)

// ipChangeServer is a single-homed server attached to a subnet, with its access interface
// and the leaf switch it connects to.
type ipChangeServer struct {
	name  string
	iface string
	leaf  string
}

// findCrossLeafSubnetPair finds two single-homed servers attached to the same VPC subnet but
// on different leaf switches. Returns the VPC, the subnet name, and the two servers. Returns a
// nil VPC (no error) when no such pair exists. Multi-homed connections (MCLAG/ESLAG) and
// hostBGP subnets are skipped.
func findCrossLeafSubnetPair(ctx context.Context, kube kclient.Client) (*vpcapi.VPC, string, *ipChangeServer, *ipChangeServer, error) {
	attaches := &vpcapi.VPCAttachmentList{}
	if err := kube.List(ctx, attaches); err != nil {
		return nil, "", nil, nil, fmt.Errorf("listing VPCAttachments: %w", err)
	}

	type subnetKey struct {
		vpc    string
		subnet string
	}
	bySubnet := map[subnetKey][]ipChangeServer{}
	vpcs := map[string]*vpcapi.VPC{}

	for _, attach := range attaches.Items {
		conn := &wiringapi.Connection{}
		if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: attach.Spec.Connection}, conn); err != nil {
			continue
		}
		switches, serverNames, _, _, err := conn.Spec.Endpoints()
		if err != nil || len(serverNames) != 1 || len(switches) != 1 {
			continue
		}

		vpcName := attach.Spec.VPCName()
		vpc, ok := vpcs[vpcName]
		if !ok {
			vpc = &vpcapi.VPC{}
			if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: vpcName}, vpc); err != nil {
				continue
			}
			vpcs[vpcName] = vpc
		}

		subnetName := attach.Spec.SubnetName()
		subnet := vpc.Spec.Subnets[subnetName]
		// The trigger drives a real DHCP cycle, so the subnet must have DHCP enabled and must
		// not be a hostBGP subnet (which behaves differently).
		if subnet == nil || subnet.HostBGP || !subnet.DHCP.Enable {
			continue
		}

		var iface string
		if conn.Spec.Unbundled != nil {
			iface = fmt.Sprintf("%s.%d", conn.Spec.Unbundled.Link.Server.LocalPortName(), subnet.VLAN)
		} else {
			iface = fmt.Sprintf("bond0.%d", subnet.VLAN)
		}

		key := subnetKey{vpc: vpcName, subnet: subnetName}
		bySubnet[key] = append(bySubnet[key], ipChangeServer{name: serverNames[0], iface: iface, leaf: switches[0]})
	}

	for key, servers := range bySubnet {
		for i := range servers {
			for j := i + 1; j < len(servers); j++ {
				if servers[i].leaf != servers[j].leaf {
					return vpcs[key.vpc], key.subnet, &servers[i], &servers[j], nil
				}
			}
		}
	}

	return nil, "", nil, nil, nil
}

// dhcpIPChangeConvergenceTest verifies that when a host changes its DHCP IP within an L3VNI
// anycast-gateway subnet, a host in the same subnet on a different leaf regains connectivity to
// the new IP promptly. It deterministically induces the convergence race: pin the target host to
// a fresh static IP, make the cross-leaf prober send traffic to that IP before the host claims it
// (so the prober's leaf installs a failed neighbor on the SVI), then force the host onto the new
// IP and measure how long the prober takes to reach it.
func dhcpIPChangeConvergenceTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	// The race lives in the L3VNI symmetric IRB path (per-VPC VRF, EVPN type-5 host routes);
	// it does not exist in L2VNI mode.
	if testCtx.setupOpts.VPCMode != vpcapi.VPCModeL3VNI {
		slog.Info("Skipping DHCP IP change convergence test, requires L3VNI mode", "mode", testCtx.setupOpts.VPCMode)

		return true, nil, errNotL3VNI
	}

	vpc, subnetName, victim, prober, err := findCrossLeafSubnetPair(ctx, testCtx.kube)
	if err != nil {
		return false, nil, err
	}
	if vpc == nil {
		slog.Info("No same-subnet servers on different leaves, skipping DHCP IP change convergence test")

		return true, nil, errNoCrossLeafPair
	}

	slog.Info("Testing DHCP IP change convergence",
		"vpc", vpc.Name, "subnet", subnetName,
		"victim", victim.name, "victimLeaf", victim.leaf,
		"prober", prober.name, "proberLeaf", prober.leaf)

	// Derive a fresh, unused static IP in the subnet.
	subnet := vpc.Spec.Subnets[subnetName]
	_, subnetCIDR, err := net.ParseCIDR(subnet.Subnet)
	if err != nil {
		return false, nil, fmt.Errorf("parsing subnet CIDR %s: %w", subnet.Subnet, err)
	}
	base := subnetCIDR.IP.To4()
	if base == nil {
		return false, nil, fmt.Errorf("subnet %s is not IPv4", subnet.Subnet) //nolint:goerr113
	}
	newIP := make(net.IP, 4)
	binary.BigEndian.PutUint32(newIP, binary.BigEndian.Uint32(base)+dhcpIPChangeStaticOffset)
	if !subnetCIDR.Contains(newIP) {
		return false, nil, fmt.Errorf("static offset %d puts %s outside subnet %s", dhcpIPChangeStaticOffset, newIP, subnet.Subnet) //nolint:goerr113
	}
	newIPStr := newIP.String()
	newAddr, err := netip.ParseAddr(newIPStr)
	if err != nil {
		return false, nil, fmt.Errorf("parsing new IP %s: %w", newIPStr, err)
	}

	victimSSH, err := testCtx.getSSH(ctx, victim.name)
	if err != nil {
		return false, nil, fmt.Errorf("getting ssh for %s: %w", victim.name, err)
	}
	proberSSH, err := testCtx.getSSH(ctx, prober.name)
	if err != nil {
		return false, nil, fmt.Errorf("getting ssh for %s: %w", prober.name, err)
	}

	proberIPStr, err := getInterfaceIPv4(ctx, proberSSH, prober.iface)
	if err != nil {
		return false, nil, fmt.Errorf("getting prober %s IP: %w", prober.name, err)
	}
	proberAddr, err := netip.ParseAddr(proberIPStr)
	if err != nil {
		return false, nil, fmt.Errorf("parsing prober IP %s: %w", proberIPStr, err)
	}

	victimMAC, err := getInterfaceMAC(ctx, victimSSH, victim.iface)
	if err != nil || victimMAC == "" {
		return false, nil, fmt.Errorf("getting MAC for %s %s: %w", victim.name, victim.iface, err)
	}

	// Pin the victim's MAC to the fresh IP via a static DHCP lease, preserving the rest of the
	// subnet's DHCP config so the other servers are undisturbed.
	savedStatic := map[string]vpcapi.VPCDHCPStatic{}
	for k, v := range subnet.DHCP.Static {
		savedStatic[k] = v
	}
	if subnet.DHCP.Static == nil {
		subnet.DHCP.Static = map[string]vpcapi.VPCDHCPStatic{}
	}
	subnet.DHCP.Static[victimMAC] = vpcapi.VPCDHCPStatic{IP: newIPStr}

	if _, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc); err != nil {
		return false, nil, fmt.Errorf("adding static lease to VPC %s: %w", vpc.Name, err)
	}
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting ready after static lease: %w", err)
	}

	reverts := []RevertFunc{
		func(ctx context.Context) error {
			subnet := vpc.Spec.Subnets[subnetName]
			if len(savedStatic) == 0 {
				subnet.DHCP.Static = nil
			} else {
				subnet.DHCP.Static = savedStatic
			}
			if _, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc); err != nil {
				return fmt.Errorf("restoring DHCP config on VPC %s: %w", vpc.Name, err)
			}
			if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
				return fmt.Errorf("waiting ready after restoring DHCP config: %w", err)
			}
			if _, _, err := victimSSH.Run(ctx, fmt.Sprintf("sudo networkctl reconfigure %s", victim.iface)); err != nil {
				return fmt.Errorf("reconfiguring %s after revert: %w", victim.name, err)
			}

			return nil
		},
	}

	// Prime the prober's leaf: send traffic to the new IP before the victim claims it, so the
	// leaf installs a failed neighbor on the SVI. These pings are expected to fail.
	if pe := checkPing(ctx, dhcpIPChangePrimingPings, nil, prober.name, newIPStr, proberSSH, newAddr, &proberAddr, false); pe != nil {
		slog.Warn("Priming traffic to the new IP unexpectedly succeeded before assignment", "detail", pe.Error())
	}

	// Force the victim onto the new IP.
	if _, stderr, err := victimSSH.Run(ctx, fmt.Sprintf("sudo networkctl reconfigure %s", victim.iface)); err != nil {
		return false, reverts, fmt.Errorf("reconfiguring %s: %w (stderr: %s)", victim.name, err, stderr)
	}

	// Probe the new IP continuously so the prober's leaf keeps its neighbor entry hot across the
	// transition, while waiting for the victim to claim the IP. Once it is assigned, measure how
	// long the prober takes to reach it.
	var assignedAt time.Time
	var recovery time.Duration
	recovered := false
	deadline := time.Now().Add(dhcpIPChangeMaxWait)
	for time.Now().Before(deadline) {
		reachable := checkPing(ctx, 1, nil, prober.name, newIPStr, proberSSH, newAddr, &proberAddr, true) == nil

		if assignedAt.IsZero() {
			if cur, err := getInterfaceIPv4(ctx, victimSSH, victim.iface); err == nil && cur == newIPStr {
				assignedAt = time.Now()
				slog.Info("Victim acquired the new IP", "victim", victim.name, "newIP", newIPStr)
			}
		}
		if !assignedAt.IsZero() && reachable {
			recovery = time.Since(assignedAt)
			recovered = true

			break
		}

		select {
		case <-ctx.Done():
			return false, reverts, fmt.Errorf("waiting for connectivity recovery: %w", ctx.Err())
		case <-time.After(1 * time.Second):
		}
	}

	if assignedAt.IsZero() {
		return false, reverts, fmt.Errorf("%s did not acquire new IP %s after reconfigure", victim.name, newIPStr) //nolint:goerr113
	}
	if !recovered {
		return false, reverts, fmt.Errorf("%s never regained connectivity to %s within %s after the DHCP IP change", prober.name, newIPStr, dhcpIPChangeMaxWait) //nolint:goerr113
	}

	slog.Info("Cross-leaf connectivity recovered after DHCP IP change",
		"prober", prober.name, "victim", victim.name, "newIP", newIPStr, "recovery", recovery.Round(time.Second))

	if recovery > dhcpIPChangeRecoveryThreshold {
		return false, reverts, fmt.Errorf("%s took %s to reach %s after the DHCP IP change, exceeding the %s threshold", prober.name, recovery.Round(time.Second), newIPStr, dhcpIPChangeRecoveryThreshold) //nolint:goerr113
	}

	return false, reverts, nil
}
