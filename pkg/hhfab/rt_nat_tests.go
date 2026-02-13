// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// calculateStatelessNATIP calculates the expected NAT IP for a source IP using the stateless NAT offset algorithm.
// NOTE: This function mirrors the algorithm from dataplane
// nat_ip = nat_pool_start + (source_ip - source_subnet_start)
func calculateStatelessNATIP(sourceIP, sourceSubnet, natPoolStart netip.Addr) (netip.Addr, error) {
	// Calculate offset from source subnet start
	var offset uint32
	if sourceIP.Is4() && sourceSubnet.Is4() && natPoolStart.Is4() {
		sourceBytes := sourceIP.As4()
		subnetBytes := sourceSubnet.As4()

		sourceInt := binary.BigEndian.Uint32(sourceBytes[:])
		subnetInt := binary.BigEndian.Uint32(subnetBytes[:])

		if sourceInt < subnetInt {
			return netip.Addr{}, fmt.Errorf("source IP %s is before subnet start %s", sourceIP, sourceSubnet) //nolint:err113
		}
		offset = sourceInt - subnetInt

		// Add offset to NAT pool start
		natPoolBytes := natPoolStart.As4()
		natPoolInt := binary.BigEndian.Uint32(natPoolBytes[:])
		natIPInt := natPoolInt + offset

		var natIPBytes [4]byte
		binary.BigEndian.PutUint32(natIPBytes[:], natIPInt)

		return netip.AddrFrom4(natIPBytes), nil
	}

	return netip.Addr{}, fmt.Errorf("only IPv4 NAT is currently supported") //nolint:err113
}

// testNATGatewayConnectivity performs E2E connectivity testing for NAT gateway peering.
// It discovers server IPs, calculates expected NAT IPs, and performs ping/iperf3 tests.
// NOTE: This uses calculateStatelessNATIP which couples to the dataplane NAT algorithm.
// The function supports both source NAT and destination NAT:
// - If vpc2NATPool is set: vpc1 pings vpc2 using vpc2's NAT IPs (destination NAT)
// - If vpc2NATPool is empty: vpc1 pings vpc2's real IPs (source NAT on vpc1 side)
// vpc1NATPool is not used because source NAT is transparent from the client perspective.
func (testCtx *VPCPeeringTestCtx) testNATGatewayConnectivity(
	ctx context.Context,
	vpc1, vpc2 *vpcapi.VPC,
	_ /* vpc1NATPool */, vpc2NATPool []string,
) error {
	startTime := time.Now()
	slog.Info("Testing NAT gateway peering connectivity")

	servers := &wiringapi.ServerList{}
	if err := testCtx.kube.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}

	// Get servers attached to each VPC
	vpc1Servers := []string{}
	vpc2Servers := []string{}

	for _, server := range servers.Items {
		attachedSubnets, err := apiutil.GetAttachedSubnets(ctx, testCtx.kube, server.Name)
		if err != nil {
			continue
		}

		for subnetName := range attachedSubnets {
			if strings.HasPrefix(subnetName, vpc1.Name+"/") {
				vpc1Servers = append(vpc1Servers, server.Name)

				break
			}
			if strings.HasPrefix(subnetName, vpc2.Name+"/") {
				vpc2Servers = append(vpc2Servers, server.Name)

				break
			}
		}
	}

	if len(vpc1Servers) == 0 || len(vpc2Servers) == 0 {
		return fmt.Errorf("need servers in both VPCs for NAT connectivity test") //nolint:err113
	}

	slog.Debug("Found servers for NAT test", "vpc1", vpc1.Name, "servers", vpc1Servers, "vpc2", vpc2.Name, "servers", vpc2Servers)

	// Get SSH configs for servers
	sshConfigs := map[string]*sshutil.Config{}
	for _, serverName := range append(vpc1Servers, vpc2Servers...) {
		// Find VM by name
		var vm VM
		found := false
		for _, v := range testCtx.vlab.VMs {
			if v.Name == serverName {
				vm = v
				found = true

				break
			}
		}
		if !found {
			return fmt.Errorf("VM not found for server %s", serverName) //nolint:err113
		}

		sshCfg, err := testCtx.vlabCfg.SSHVM(ctx, testCtx.vlab, vm)
		if err != nil {
			return fmt.Errorf("getting ssh config for %s: %w", serverName, err)
		}

		sshConfigs[serverName] = sshCfg
	}

	// Discover server IPs
	serverIPs := map[string]netip.Addr{}
	for _, serverName := range append(vpc1Servers, vpc2Servers...) {
		sshCfg := sshConfigs[serverName]
		stdout, stderr, err := sshCfg.Run(ctx, "ip -o -4 addr show | awk '{print $2, $4}'")
		if err != nil {
			return fmt.Errorf("getting IP for %s: %w: %s", serverName, err, stderr)
		}

		found := false
		for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			if fields[0] == "lo" || fields[0] == "enp2s0" || fields[0] == "docker0" {
				continue
			}

			addr, err := netip.ParsePrefix(fields[1])
			if err != nil {
				continue
			}

			serverIPs[serverName] = addr.Addr()
			found = true
			slog.Debug("Discovered server IP", "server", serverName, "ip", addr.Addr().String())

			break
		}

		if !found {
			return fmt.Errorf("no IP found for server %s", serverName) //nolint:err113
		}
	}

	// Parse NAT pools
	var vpc2NATPoolStart netip.Addr
	if len(vpc2NATPool) > 0 {
		prefix, err := netip.ParsePrefix(vpc2NATPool[0])
		if err != nil {
			return fmt.Errorf("parsing vpc2 NAT pool: %w", err)
		}
		vpc2NATPoolStart = prefix.Addr()
	}

	// Get VPC subnet starts for offset calculation
	vpc2SubnetStart := netip.Addr{}
	for _, subnet := range vpc2.Spec.Subnets {
		if prefix, err := netip.ParsePrefix(subnet.Subnet); err == nil {
			vpc2SubnetStart = prefix.Addr()

			break
		}
	}

	// Test connectivity: vpc1 -> vpc2
	// If vpc2NATPool is set, ping vpc2's NAT IPs (destination NAT)
	// If vpc2NATPool is empty, ping vpc2's real IPs (source NAT on vpc1 side)
	pings := semaphore.NewWeighted(10)
	var errors []error
	var errMutex sync.Mutex

	for _, serverA := range vpc1Servers {
		for _, serverB := range vpc2Servers {
			destIP := serverIPs[serverB]

			// Calculate expected NAT IP for destination (only if vpc2 has NAT configured)
			natDestIP := destIP
			if len(vpc2NATPool) > 0 && !vpc2NATPoolStart.IsUnspecified() {
				var err error
				natDestIP, err = calculateStatelessNATIP(destIP, vpc2SubnetStart, vpc2NATPoolStart)
				if err != nil {
					return fmt.Errorf("calculating NAT IP for %s: %w", serverB, err)
				}
				slog.Debug("Calculated NAT IP", "server", serverB, "real", destIP, "nat", natDestIP)
			}

			// Ping NAT IP
			if err := pings.Acquire(ctx, 1); err != nil {
				return fmt.Errorf("acquiring ping semaphore: %w", err)
			}

			go func(from, to string, targetIP netip.Addr) {
				defer pings.Release(1)

				sshCfg := sshConfigs[from]
				cmd := fmt.Sprintf("ping -c 5 -W 2 %s", targetIP)
				stdout, stderr, err := sshCfg.Run(ctx, cmd)
				if err != nil {
					slog.Debug("Ping result", "from", from, "to", to, "target", targetIP, "expected", true, "ok", false, "fail", true, "err", err, "stdout", stdout, "stderr", stderr)
					errMutex.Lock()
					errors = append(errors, fmt.Errorf("ping from %s to %s (%s) failed: %w", from, to, targetIP, err))
					errMutex.Unlock()
				} else {
					slog.Debug("Ping result", "from", from, "to", to, "target", targetIP, "expected", true, "ok", true, "fail", false, "err", nil, "stdout", stdout, "stderr", stderr)
				}
			}(serverA, serverB, natDestIP)
		}
	}

	// Wait for all pings to complete
	if err := pings.Acquire(ctx, 10); err != nil {
		return fmt.Errorf("waiting for pings: %w", err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("NAT ping test failed with %d errors: %v", len(errors), errors) //nolint:err113
	}

	slog.Debug("NAT ping tests completed successfully, starting iperf3 tests")

	// Test iperf3: vpc1 -> vpc2
	iperfs := semaphore.NewWeighted(5)
	errors = []error{}

	for _, serverA := range vpc1Servers {
		for _, serverB := range vpc2Servers {
			destIP := serverIPs[serverB]

			// Calculate expected NAT IP for destination (only if vpc2 has NAT configured)
			natDestIP := destIP
			if len(vpc2NATPool) > 0 && !vpc2NATPoolStart.IsUnspecified() {
				var err error
				natDestIP, err = calculateStatelessNATIP(destIP, vpc2SubnetStart, vpc2NATPoolStart)
				if err != nil {
					return fmt.Errorf("calculating NAT IP for %s: %w", serverB, err)
				}
			}

			if err := iperfs.Acquire(ctx, 1); err != nil {
				return fmt.Errorf("acquiring iperf3 semaphore: %w", err)
			}

			go func(from, to string, targetIP netip.Addr) {
				defer iperfs.Release(1)

				fromSSH := sshConfigs[from]
				toSSH := sshConfigs[to]

				testCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
				defer cancel()

				g, gctx := errgroup.WithContext(testCtx)

				// Start iperf3 server
				g.Go(func() error {
					cmd := "toolbox -E LD_PRELOAD=/lib/x86_64-linux-gnu/libgcc_s.so.1 -q timeout 30 iperf3 -s -1 -J"
					stdout, stderr, err := toSSH.Run(gctx, cmd)
					if err != nil {
						slog.Error("iperf3 server failed", "to", to, "err", err, "stdout", stdout, "stderr", stderr)

						return fmt.Errorf("running iperf3 server: %w", err)
					}

					return nil
				})

				// Start iperf3 client
				g.Go(func() error {
					time.Sleep(1 * time.Second) // Give server time to start
					cmd := fmt.Sprintf("toolbox -E LD_PRELOAD=/lib/x86_64-linux-gnu/libgcc_s.so.1 -q timeout 30 iperf3 -P 4 -J -c %s -t 5 -M 1200", targetIP.String())
					stdout, stderr, err := fromSSH.Run(gctx, cmd)
					if err != nil {
						slog.Error("iperf3 client failed", "from", from, "to", to, "target", targetIP, "err", err, "stdout", stdout, "stderr", stderr)

						return fmt.Errorf("running iperf3 client: %w", err)
					}

					// Parse iperf3 results
					report, parseErr := parseIPerf3Report([]byte(stdout))
					if parseErr == nil {
						slog.Debug("IPerf3 result", "from", from, "to", to,
							"sendSpeed", asMbps(report.End.SumSent.BitsPerSecond),
							"receiveSpeed", asMbps(report.End.SumReceived.BitsPerSecond),
							"sent", asMB(float64(report.End.SumSent.Bytes)),
							"received", asMB(float64(report.End.SumReceived.Bytes)))
					} else {
						slog.Debug("iperf3 test succeeded", "from", from, "to", to, "target", targetIP)
					}

					return nil
				})

				if err := g.Wait(); err != nil {
					errMutex.Lock()
					errors = append(errors, fmt.Errorf("iperf3 from %s to %s (%s) failed: %w", from, to, targetIP, err))
					errMutex.Unlock()
				}
			}(serverA, serverB, natDestIP)
		}
	}

	// Wait for all iperf3 tests to complete
	if err := iperfs.Acquire(ctx, 5); err != nil {
		return fmt.Errorf("waiting for iperf3 tests: %w", err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("NAT iperf3 test failed with %d errors: %v", len(errors), errors) //nolint:err113
	}

	slog.Info("NAT connectivity test (ping+iperf3) completed successfully", "took", time.Since(startTime))

	return nil
}

// Test gateway peering with stateful source NAT (only VPC1 has stateful NAT configured)
// NOTE: Stateful NAT on both sides of a peering is not supported (see dataplane#1248)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringStatefulSourceNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for NAT gateway peering test") //nolint:goerr113
	}

	// Sort VPCs to ensure consistent selection
	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 1)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Only VPC1 has stateful NAT - VPC1's traffic will be source-NATed
	vpc1NATCIDR := []string{"192.168.11.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, GwPeeringOptions{
		VPC1NATCIDR: vpc1NATCIDR,
		StatefulNAT: true,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up NAT gateway peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Wait for NAT peering to take effect
	time.Sleep(15 * time.Second)

	// Test connectivity - VPC2 has no NAT, so we ping real IPs
	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, vpc1NATCIDR, nil); err != nil {
		if testCtx.pauseOnFail {
			if err := pauseOnFailure(ctx); err != nil {
				slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
			}
		}

		return false, nil, fmt.Errorf("testing NAT gateway peering connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with stateless source NAT (only VPC1 has NAT configured)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringStatelessSourceNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for stateless source NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 1)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Only VPC1 has NAT - this means VPC1's traffic will be source-NATed
	vpc1NATCIDR := []string{"192.168.21.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, GwPeeringOptions{
		VPC1NATCIDR: vpc1NATCIDR,
		StatefulNAT: false, // stateless
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up stateless source NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	time.Sleep(15 * time.Second)

	// Test connectivity - VPC2 has no NAT, so we ping real IPs
	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, vpc1NATCIDR, nil); err != nil {
		if testCtx.pauseOnFail {
			if err := pauseOnFailure(ctx); err != nil {
				slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
			}
		}

		return false, nil, fmt.Errorf("testing stateless source NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with stateless destination NAT (only VPC2 has NAT configured)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringStatelessDestinationNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for stateless destination NAT test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 1)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Only VPC2 has NAT - VPC1 will ping VPC2's NAT IPs (destination NAT)
	vpc2NATCIDR := []string{"192.168.22.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, GwPeeringOptions{
		VPC2NATCIDR: vpc2NATCIDR,
		StatefulNAT: false, // stateless
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up stateless destination NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	time.Sleep(15 * time.Second)

	// Test connectivity - VPC1 pings VPC2's NAT IPs
	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, nil, vpc2NATCIDR); err != nil {
		if testCtx.pauseOnFail {
			if err := pauseOnFailure(ctx); err != nil {
				slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
			}
		}

		return false, nil, fmt.Errorf("testing stateless destination NAT connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with stateless NAT and IP exclusions
func (testCtx *VPCPeeringTestCtx) gatewayPeeringStatelessNATWithIPExclusionTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for NAT IP exclusion test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 1)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Both VPCs have NAT configured
	vpc1NATCIDR := []string{"192.168.31.0/24"}
	vpc2NATCIDR := []string{"192.168.32.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, GwPeeringOptions{
		VPC1NATCIDR: vpc1NATCIDR,
		VPC2NATCIDR: vpc2NATCIDR,
		StatefulNAT: false, // stateless
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up NAT with IP exclusion peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	time.Sleep(15 * time.Second)

	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, vpc1NATCIDR, vpc2NATCIDR); err != nil {
		if testCtx.pauseOnFail {
			if err := pauseOnFailure(ctx); err != nil {
				slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
			}
		}

		return false, nil, fmt.Errorf("testing NAT with IP exclusion connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with stateless NAT and NAT pool exclusions
func (testCtx *VPCPeeringTestCtx) gatewayPeeringStatelessNATWithNATPoolExclusionTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, fmt.Errorf("not enough VPCs for NAT pool exclusion test") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 1)

	vpc1 := &vpcs.Items[0]
	vpc2 := &vpcs.Items[1]

	// Both VPCs have NAT configured with different pool ranges
	vpc1NATCIDR := []string{"192.168.41.0/24"}
	vpc2NATCIDR := []string{"192.168.42.0/24"}

	appendGwPeeringSpec(gwPeerings, vpc1, vpc2, GwPeeringOptions{
		VPC1NATCIDR: vpc1NATCIDR,
		VPC2NATCIDR: vpc2NATCIDR,
		StatefulNAT: false, // stateless
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up NAT pool exclusion peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	time.Sleep(15 * time.Second)

	if err := testCtx.testNATGatewayConnectivity(ctx, vpc1, vpc2, vpc1NATCIDR, vpc2NATCIDR); err != nil {
		if testCtx.pauseOnFail {
			if err := pauseOnFailure(ctx); err != nil {
				slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
			}
		}

		return false, nil, fmt.Errorf("testing NAT pool exclusion connectivity: %w", err)
	}

	return false, nil, nil
}

// Test gateway peering with overlapping NAT pools (edge case testing)
func (testCtx *VPCPeeringTestCtx) gatewayPeeringOverlapNATTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 3 {
		return true, nil, fmt.Errorf("not enough VPCs for overlap NAT test (need 3)") //nolint:goerr113
	}

	sort.Slice(vpcs.Items, func(i, j int) bool {
		return vpcs.Items[i].Name < vpcs.Items[j].Name
	})

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 2)

	existingVPC := &vpcs.Items[0]
	newVPC := &vpcs.Items[1]

	// Create first peering with NAT
	existingVPCNATCIDR := []string{"192.168.61.0/24"}
	newVPCNATCIDR := []string{"192.168.62.0/24"}

	appendGwPeeringSpec(gwPeerings, existingVPC, newVPC, GwPeeringOptions{
		VPC1NATCIDR: existingVPCNATCIDR,
		VPC2NATCIDR: newVPCNATCIDR,
		StatefulNAT: false,
	})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up overlap NAT peerings: %w", err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Wait for NAT peering to take effect
	time.Sleep(15 * time.Second)

	// Test connectivity using custom NAT-aware connectivity testing
	if err := testCtx.testNATGatewayConnectivity(ctx, existingVPC, newVPC, existingVPCNATCIDR, newVPCNATCIDR); err != nil {
		if testCtx.pauseOnFail {
			if err := pauseOnFailure(ctx); err != nil {
				slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
			}
		}

		return false, nil, fmt.Errorf("testing NAT gateway peering connectivity: %w", err)
	}

	return false, nil, nil
}

// getNATTestCases returns the NAT test cases to be added to the multi-VPC single-subnet suite
func getNATTestCases(testCtx *VPCPeeringTestCtx) []JUnitTestCase {
	return []JUnitTestCase{
		{
			Name: "Gateway Peering Stateful Source NAT",
			F:    testCtx.gatewayPeeringStatefulSourceNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Stateless Source NAT",
			F:    testCtx.gatewayPeeringStatelessSourceNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Stateless Destination NAT",
			F:    testCtx.gatewayPeeringStatelessDestinationNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Stateless NAT with IP Exclusion",
			F:    testCtx.gatewayPeeringStatelessNATWithIPExclusionTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Stateless NAT with NAT Pool Exclusion",
			F:    testCtx.gatewayPeeringStatelessNATWithNATPoolExclusionTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
		{
			Name: "Gateway Peering Overlap NAT",
			F:    testCtx.gatewayPeeringOverlapNATTest,
			SkipFlags: SkipFlags{
				NoGateway: true,
			},
		},
	}
}
