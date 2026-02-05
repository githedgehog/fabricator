// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func makeSingleVPCSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Single VPC Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "No restrictions",
			F:    testCtx.noRestrictionsTest,
			SkipFlags: SkipFlags{
				NoServers: true,
			},
		},
		{
			Name: "Single VPC with restrictions",
			F:    testCtx.singleVPCWithRestrictionsTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoServers:     true,
			},
		},
		{
			Name: "DNS/NTP/MTU/DHCP lease",
			F:    testCtx.dnsNtpMtuTest,
		},
		{
			Name: "DHCP renewal",
			F:    testCtx.dhcpRenewalTest,
		},
		{
			Name: "DHCP pool depletion",
			F:    testCtx.dhcpDepletionTest,
		},
		{
			Name: "MCLAG Failover",
			F:    testCtx.mclagTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoServers:     true,
			},
		},
		{
			Name: "ESLAG Failover",
			F:    testCtx.eslagTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoServers:     true,
			},
		},
		{
			Name: "Bundled Failover",
			F:    testCtx.bundledFailoverTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoServers:     true,
			},
		},
		{
			Name: "Spine Failover",
			F:    testCtx.spineFailoverTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoFabricLink:  true,
				NoServers:     true,
			},
		},
		{
			Name: "Mesh Failover",
			F:    testCtx.meshFailoverTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoMeshLink:    true,
				NoServers:     true,
			},
		},
		{
			Name: "RoCE flag and basic traffic marking",
			F:    testCtx.roceBasicTest,
			SkipFlags: SkipFlags{
				RoCE:      true,
				NoServers: true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// Basic test for mclag failover.
// For each mclag connection, set one of the links down by shutting down the port on the switch,
// then test connectivity. Repeat for the other link.
func (testCtx *VPCPeeringTestCtx) mclagTest(ctx context.Context) (bool, []RevertFunc, error) {
	// list connections in the fabric, filter by MC-LAG connection type
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeMCLAG}); err != nil {
		return false, nil, fmt.Errorf("listing connections: %w", err)
	}
	if len(conns.Items) == 0 {
		slog.Info("No MCLAG connections found, skipping test")

		return true, nil, errNoMclags
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing MCLAG connection", "connection", conn.Name)
		if len(conn.Spec.MCLAG.Links) != 2 {
			return false, nil, fmt.Errorf("MCLAG connection %s has %d links, expected 2", conn.Name, len(conn.Spec.MCLAG.Links)) //nolint:goerr113
		}
		for _, link := range conn.Spec.MCLAG.Links {
			if err := shutDownLinkAndTest(ctx, testCtx, link); err != nil {
				return false, nil, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
			if !testCtx.extended {
				slog.Debug("Skipping other link, set extended=true to iterate over all links")

				break
			}
		}
	}

	return false, nil, nil
}

// Basic test for eslag failover.
// For each eslag connection, set one of the links down by shutting down the port on the switch,
// then test connectivity. Repeat for the other link.
func (testCtx *VPCPeeringTestCtx) eslagTest(ctx context.Context) (bool, []RevertFunc, error) {
	// l3vni mode is not compatible with ESLAG, so there will be no servers attached to ESLAG connections
	if testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3VNI {
		return true, nil, fmt.Errorf("L3VNI mode is not compatible with ESLAG") //nolint:goerr113
	}
	// list connections in the fabric, filter by ES-LAG connection type
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeESLAG}); err != nil {
		return false, nil, fmt.Errorf("listing connections: %w", err)
	}
	if len(conns.Items) == 0 {
		slog.Info("No ESLAG connections found, skipping test")

		return true, nil, errNoEslags
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing ESLAG connection", "connection", conn.Name)
		if len(conn.Spec.ESLAG.Links) != 2 {
			return false, nil, fmt.Errorf("ESLAG connection %s has %d links, expected 2", conn.Name, len(conn.Spec.ESLAG.Links)) //nolint:goerr113
		}
		for _, link := range conn.Spec.ESLAG.Links {
			if err := shutDownLinkAndTest(ctx, testCtx, link); err != nil {
				return false, nil, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
			if !testCtx.extended {
				slog.Debug("Skipping other link, set extended=true to iterate over all links")

				break
			}
		}
	}

	return false, nil, nil
}

// Basic test for bundled connection failover.
// For each bundled connection, set one of the links down by shutting down the port on the switch,
// then test connectivity. Repeat for the other link(s).
func (testCtx *VPCPeeringTestCtx) bundledFailoverTest(ctx context.Context) (bool, []RevertFunc, error) {
	// list connections in the fabric, filter by bundled connection type
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeBundled}); err != nil {
		return false, nil, fmt.Errorf("listing connections: %w", err)
	}
	if len(conns.Items) == 0 {
		slog.Info("No bundled connections found, skipping test")

		return true, nil, errNoBundled
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing Bundled connection", "connection", conn.Name)
		if len(conn.Spec.Bundled.Links) < 2 {
			return false, nil, fmt.Errorf("MCLAG connection %s has %d links, expected at least 2", conn.Name, len(conn.Spec.Bundled.Links)) //nolint:goerr113
		}
		for _, link := range conn.Spec.Bundled.Links {
			if err := shutDownLinkAndTest(ctx, testCtx, link); err != nil {
				return false, nil, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
			if !testCtx.extended {
				slog.Debug("Skipping other link, set extended=true to iterate over all links")

				break
			}
		}
	}

	return false, nil, nil
}

// Basic test for spine failover.
// Iterate over the spine switches (skip the first one), and shut down all links towards them.
// Test connectivity, then re-enable the links.
func (testCtx *VPCPeeringTestCtx) spineFailoverTest(ctx context.Context) (bool, []RevertFunc, error) {
	var returnErr error

	// list spines, unfortunately we cannot filter by role
	switches := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, switches); err != nil {
		return false, nil, fmt.Errorf("listing switches: %w", err)
	}
	spines := make([]wiringapi.Switch, 0)
	spinesSSH := make(map[string]*sshutil.Config)
	for _, sw := range switches.Items {
		if sw.Spec.Role == wiringapi.SwitchRoleSpine {
			spines = append(spines, sw)
			sshCfg, sshErr := testCtx.getSSH(ctx, sw.Name)
			if sshErr != nil {
				return false, nil, fmt.Errorf("getting ssh config for spine switch %s: %w", sw.Name, sshErr)
			}
			spinesSSH[sw.Name] = sshCfg
		}
	}

	if len(spines) < 2 {
		slog.Info("Not enough spines found, skipping test")

		return true, nil, errNotEnoughSpines
	}

outer:
	for i, spine := range spines {
		if i == 0 {
			continue
		}
		slog.Debug("Disabling links to spine", "spine", spine.Name)
		// get switch profile to find the port name in sonic-cli
		profile := &wiringapi.SwitchProfile{}
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: spine.Spec.Profile}, profile); err != nil {
			return false, nil, fmt.Errorf("getting switch profile %s: %w", spine.Spec.Profile, err)
		}
		portMap, err := profile.Spec.GetAPI2NOSPortsFor(&spine.Spec)
		if err != nil {
			return false, nil, fmt.Errorf("getting API2NOS ports for switch %s: %w", spine.Name, err)
		}
		// disable agent on spine
		spineSSH, ok := spinesSSH[spine.Name]
		if !ok {
			return false, nil, fmt.Errorf("no ssh config found for spine switch %s", spine.Name) //nolint:goerr113
		}
		if err := changeAgentStatus(ctx, spineSSH, spine.Name, false); err != nil {
			return false, nil, fmt.Errorf("disabling HH agent: %w", err)
		}

		// look for connections that have this spine as a switch
		conns := &wiringapi.ConnectionList{}
		if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{wiringapi.ListLabelSwitch(spine.Name): wiringapi.ListLabelValue, wiringapi.LabelConnectionType: wiringapi.ConnectionTypeFabric}); err != nil {
			returnErr = fmt.Errorf("listing connections: %w", err)

			break
		}
		slog.Debug(fmt.Sprintf("Found %d connections to spine %s", len(conns.Items), spine.Name))
		for _, conn := range conns.Items {
			for _, link := range conn.Spec.Fabric.Links {
				spinePort := link.Spine.LocalPortName()
				nosPortName, ok := portMap[spinePort]
				if !ok {
					returnErr = fmt.Errorf("port %s not found in switch profile %s for switch %s", spinePort, profile.Name, spine.Name) //nolint:goerr113

					break outer
				}
				if err := changeSwitchPortStatus(ctx, spineSSH, spine.Name, nosPortName, false); err != nil {
					returnErr = fmt.Errorf("setting switch port down: %w", err)

					break outer
				}
				// not setting ports back up as restarting the agent eventually takes care of that
			}
		}
	}

	if returnErr == nil {
		// wait a bit to make sure that the fabric has converged; can't rely on agents as we disabled them
		slog.Debug("Waiting 30 seconds for fabric to converge")
		time.Sleep(30 * time.Second)
		if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
			returnErr = err
		}
	}

	// re-enable agents on all spines
	for i, spine := range spines {
		if i == 0 {
			continue
		}
		maxRetries := 5
		sleepTime := time.Second * 5
		enabled := false
		for i := range maxRetries {
			spineSSH, ok := spinesSSH[spine.Name]
			if !ok {
				return false, nil, fmt.Errorf("no ssh config found for spine switch %s", spine.Name) //nolint:goerr113
			}
			if err := changeAgentStatus(ctx, spineSSH, spine.Name, true); err != nil {
				slog.Error("Enabling HH agent", "switch", spine.Name, "error", err)
				if i < maxRetries-1 {
					slog.Warn("Retrying in 5 seconds")
					time.Sleep(sleepTime)
				}
			} else {
				enabled = true

				break
			}
		}
		if enabled {
			continue
		}
		// if we get here, we failed to enable the agent
		if returnErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("could not enable HH agent on switch %s after %d attempts", spine.Name, maxRetries)) //nolint:goerr113
		} else {
			returnErr = fmt.Errorf("could not enable HH agent on switch %s after %d attempts", spine.Name, maxRetries) //nolint:goerr113
		}
	}

	return false, nil, returnErr
}

// Basic test for mesh failover.
// Iterate over leaf switches, shutdown all mesh links except for one, test connectivity
// as soon as we manage to test this on a leaf, return and renable all agents as part of the revert
func (testCtx *VPCPeeringTestCtx) meshFailoverTest(ctx context.Context) (bool, []RevertFunc, error) {
	// list leaves, unfortunately we cannot filter by role
	switches := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, switches); err != nil {
		return false, nil, fmt.Errorf("listing switches: %w", err)
	}
	leaves := make([]wiringapi.Switch, 0)
	leavesSSH := make(map[string]*sshutil.Config)
	for _, sw := range switches.Items {
		if sw.Spec.Role != wiringapi.SwitchRoleSpine {
			leaves = append(leaves, sw)
			sshCfg, sshErr := testCtx.getSSH(ctx, sw.Name)
			if sshErr != nil {
				return false, nil, fmt.Errorf("getting ssh config for leaf switch %s: %w", sw.Name, sshErr)
			}
			leavesSSH[sw.Name] = sshCfg
		}
	}

	if len(leaves) < 2 {
		slog.Info("Not enough leaves found, skipping test")

		return true, nil, errNotEnoughLeaves
	}
	var someLeafTested bool

	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		for _, leaf := range leaves {
			maxRetries := 5
			sleepTime := time.Second * 5
			enabled := false
			for i := range maxRetries {
				leafSSH, ok := leavesSSH[leaf.Name]
				if !ok {
					return fmt.Errorf("no ssh config found for leaf switch %s", leaf.Name) //nolint:goerr113
				}
				if err := changeAgentStatus(ctx, leafSSH, leaf.Name, true); err != nil {
					slog.Error("Enabling HH agent", "switch", leaf.Name, "error", err)
					if i < maxRetries-1 {
						slog.Warn("Retrying in 5 seconds")
						time.Sleep(sleepTime)
					}
				} else {
					enabled = true

					break
				}
			}
			if enabled {
				continue
			}
			// if we get here, we failed to enable the agent
			return fmt.Errorf("could not enable HH agent on switch %s after %d attempts", leaf.Name, maxRetries) //nolint:goerr113
		}

		return nil
	})

	for _, leaf := range leaves {
		// get mesh links for this leaf
		meshConns := &wiringapi.ConnectionList{}
		if err := testCtx.kube.List(ctx, meshConns, kclient.MatchingLabels{wiringapi.ListLabelSwitch(leaf.Name): wiringapi.ListLabelValue, wiringapi.LabelConnectionType: wiringapi.ConnectionTypeMesh}); err != nil {
			return false, nil, fmt.Errorf("listing mesh connections for leaf %s: %w", leaf.Name, err)
		}
		if len(meshConns.Items) < 2 {
			slog.Debug("Not enough mesh connections for leaf", "leaf", leaf.Name, "connections", len(meshConns.Items))

			continue
		}
		// get switch profile to find the port name in sonic-cli
		profile := &wiringapi.SwitchProfile{}
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: leaf.Spec.Profile}, profile); err != nil {
			return false, nil, fmt.Errorf("getting switch profile %s: %w", leaf.Spec.Profile, err)
		}
		portMap, err := profile.Spec.GetAPI2NOSPortsFor(&leaf.Spec)
		if err != nil {
			return false, nil, fmt.Errorf("getting API2NOS ports for switch %s: %w", leaf.Name, err)
		}
		// disable agent on leaf
		leafSSH, ok := leavesSSH[leaf.Name]
		if !ok {
			return false, nil, fmt.Errorf("no ssh config found for leaf switch %s", leaf.Name) //nolint:goerr113
		}
		if err := changeAgentStatus(ctx, leafSSH, leaf.Name, false); err != nil {
			return false, nil, fmt.Errorf("disabling HH agent: %w", err)
		}

		for i, conn := range meshConns.Items {
			// skip one connection so we don't end up isolated
			if i == 0 {
				continue
			}
			for _, link := range conn.Spec.Mesh.Links {
				var linkSwitch *wiringapi.ConnFabricLinkSwitch
				switch {
				case link.Leaf1.DeviceName() == leaf.Name:
					linkSwitch = &link.Leaf1
				case link.Leaf2.DeviceName() == leaf.Name:
					linkSwitch = &link.Leaf2
				default:
					return false, reverts, fmt.Errorf("leaf %s not found in mesh link of connection %s", leaf.Name, conn.Name) //nolint:goerr113
				}

				swPort := linkSwitch.LocalPortName()
				nosPortName, ok := portMap[swPort]
				if !ok {
					return false, reverts, fmt.Errorf("port %s not found in switch profile %s for switch %s", swPort, profile.Name, leaf.Name) //nolint:goerr113
				}
				if err := changeSwitchPortStatus(ctx, leafSSH, leaf.Name, nosPortName, false); err != nil {
					return false, reverts, fmt.Errorf("setting switch port down: %w", err)
				}
			}
		}

		// wait a bit to make sure that the fabric has converged; can't rely on agents as we disabled them
		slog.Debug("Waiting 30 seconds for fabric to converge")
		time.Sleep(30 * time.Second)
		if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
			return false, reverts, err
		}
		someLeafTested = true

		break
	}

	if !someLeafTested {
		return true, reverts, errors.New("no mesh leaves could be tested") //nolint:goerr113
	}

	return false, reverts, nil
}

// Vanilla test for VPC peering, just test connectivity without any further restriction
func (testCtx *VPCPeeringTestCtx) noRestrictionsTest(ctx context.Context) (bool, []RevertFunc, error) {
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for readiness: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// Test VPC peering with multiple subnets and with restrictions.
// Assumes the scenario has a VPC with at least 3 subnets.
// 1. Isolate the first subnet, test connectivity
// 2. Set restricted flag in the second subnet, test connectivity
// 3. Set both isolated and restricted flags in the third subnet, test connectivity
// 4. Override isolation with explicit permit list, test connectivity
// 5. Remove all restrictions
func (testCtx *VPCPeeringTestCtx) singleVPCWithRestrictionsTest(ctx context.Context) (bool, []RevertFunc, error) {
	var returnErr error

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	var vpc *vpcapi.VPC
	var subnet1, subnet2, subnet3 *vpcapi.VPCSubnet
	var subnet1Name, subnet2Name, subnet3Name string
outer:
	for _, v := range vpcs.Items {
		if len(v.Spec.Subnets) < 3 {
			continue
		}
		vpc = &v
		for subName, sub := range v.Spec.Subnets {
			switch {
			case subnet1 == nil:
				subnet1 = sub
				subnet1Name = subName
			case subnet2 == nil:
				subnet2 = sub
				subnet2Name = subName
			default:
				subnet3 = sub
				subnet3Name = subName

				break outer
			}
		}
	}
	if vpc == nil {
		return true, nil, errors.New("no VPC with at least 3 subnets found") //nolint:goerr113
	}
	permitList := []string{subnet1Name, subnet2Name, subnet3Name}
	waitTime := 5 * time.Second

	// isolate subnet1
	slog.Debug("Isolating subnet1", "subnet", subnet1Name)
	subnet1.Isolated = pointer.To(true)
	_, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, nil, fmt.Errorf("updating VPC %s: %w", vpc.Name, err)
	}

	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Removing all restrictions")
		vpc.Spec.Permit = make([][]string, 0)
		for _, sub := range vpc.Spec.Subnets {
			sub.Isolated = pointer.To(false)
			sub.Restricted = pointer.To(false)
		}
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			return fmt.Errorf("updating VPC %s: %w", vpc.Name, err)
		}
		time.Sleep(waitTime)
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}

		return nil
	})

	time.Sleep(waitTime)
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		returnErr = fmt.Errorf("waiting for ready: %w", err)
	} else if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		returnErr = fmt.Errorf("testing connectivity with %s isolated: %w", subnet1Name, err)
	}

	// set restricted flags for subnet2
	if returnErr == nil {
		slog.Debug("Restricting subnet2", "subnet", subnet2Name)
		subnet2.Restricted = pointer.To(true)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC %s: %w", vpc.Name, err)
		} else {
			time.Sleep(waitTime)
			if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with %s restricted: %w", subnet2Name, err)
			}
		}
	}

	// make subnet3 isolated and restricted
	if returnErr == nil {
		slog.Debug("Isolating and restricting subnet3", "subnet", subnet3Name)
		subnet3.Isolated = pointer.To(true)
		subnet3.Restricted = pointer.To(true)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC %s: %w", vpc.Name, err)
		} else {
			time.Sleep(waitTime)
			if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with %s isolated and restricted: %w", subnet3Name, err)
			}
		}
	}

	// override isolation with explicit permit list
	if returnErr == nil {
		vpc.Spec.Permit = make([][]string, 1)
		vpc.Spec.Permit[0] = make([]string, 3)
		copy(vpc.Spec.Permit[0], permitList)
		slog.Debug("Permitting all subnets", "subnets", permitList)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC %s: %w", vpc.Name, err)
		} else {
			time.Sleep(waitTime)
			if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with all subnets in permit list: %w", err)
			}
		}
	}

	return false, reverts, returnErr
}

// Test that DNS, NTP, MTU and DHCP Lease settings for a VPC are correctly propagated to the servers.
// For DNS, we check the content of /etc/resolv.conf;
// for NTP, we check the output of timedatectl show-timesync;
// for MTU, we check the output of "ip link" on the vlan interface;
// for DHCP Lease, we check the output of "ip addr" on the server.
func (testCtx *VPCPeeringTestCtx) dnsNtpMtuTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcAttaches := &vpcapi.VPCAttachmentList{}
	if err := testCtx.kube.List(ctx, vpcAttaches); err != nil {
		return false, nil, fmt.Errorf("listing VPCAttachments: %w", err)
	}
	if len(vpcAttaches.Items) == 0 {
		slog.Warn("No VPCAttachments found, skipping DNS/MTU/NTP test")

		return true, nil, errors.New("no VPCAttachments found") //nolint:goerr113
	}
	vpcAttach := vpcAttaches.Items[0]
	subnetName := vpcAttach.Spec.SubnetName()
	vpcName := vpcAttach.Spec.VPCName()
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: vpcName}, vpc); err != nil {
		return false, nil, fmt.Errorf("getting VPC %s: %w", vpcName, err)
	}
	subnet, ok := vpc.Spec.Subnets[subnetName]
	if !ok {
		return false, nil, fmt.Errorf("subnet %s not found in VPC %s", subnetName, vpcName) //nolint:goerr113
	}

	conn := &wiringapi.Connection{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: vpcAttach.Spec.Connection}, conn); err != nil {
		return false, nil, fmt.Errorf("getting connection %s for VPCAttachment %s: %w", vpcAttach.Spec.Connection, vpcAttach.Name, err)
	}
	_, servers, _, _, epErr := conn.Spec.Endpoints() //nolint:dogsled
	if epErr != nil {
		return false, nil, fmt.Errorf("getting endpoints for connection %s: %w", conn.Name, epErr)
	}
	if len(servers) != 1 {
		return false, nil, fmt.Errorf("expected 1 server for connection %s, got %d", conn.Name, len(servers)) //nolint:goerr113
	}
	serverName := servers[0]
	serverSSH, sshErr := testCtx.getSSH(ctx, serverName)
	if sshErr != nil {
		return false, nil, fmt.Errorf("getting ssh config for server %s: %w", serverName, sshErr)
	}
	netconfCmd, netconfErr := GetServerNetconfCmd(conn, subnet.VLAN, testCtx.setupOpts.HashPolicy)
	if netconfErr != nil {
		return false, nil, fmt.Errorf("getting netconf command for server %s: %w", serverName, netconfErr)
	}
	var ifName string
	if conn.Spec.Unbundled != nil {
		ifName = fmt.Sprintf("%s.%d", conn.Spec.Unbundled.Link.Server.LocalPortName(), subnet.VLAN)
	} else {
		ifName = fmt.Sprintf("bond0.%d", subnet.VLAN)
	}

	slog.Debug("Found server for VPCAttachment", "server", serverName, "vpc", vpcName, "subnet", subnetName, "vlan", subnet.VLAN, "ifName", ifName)

	// Set DNS, NTP and MTU
	slog.Debug("Setting DNS, NTP, MTU and DHCP lease time")
	l3mode := testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3Flat
	dhcpOpts := &vpcapi.VPCDHCPOptions{
		DNSServers:       []string{"1.1.1.1"},
		TimeServers:      []string{"1.1.1.1"},
		InterfaceMTU:     1400,
		LeaseTimeSeconds: 1800,
		AdvertisedRoutes: []vpcapi.VPCDHCPRoute{
			{
				Destination: "9.9.9.9/32",
				Gateway:     subnet.Gateway,
			},
		},
		// disable default route for L3 VPC mode to test the advertisement of the subnet route
		DisableDefaultRoute: l3mode,
	}

	subnet.DHCP = vpcapi.VPCDHCP{
		Enable:  true,
		Options: dhcpOpts,
	}
	change, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil || !change {
		return false, nil, fmt.Errorf("updating VPC vpc-01: %w", err)
	}
	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Cleaning up")
		for _, sub := range vpc.Spec.Subnets {
			sub.DHCP = vpcapi.VPCDHCP{
				Enable:  true,
				Options: nil,
			}
		}
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			return fmt.Errorf("updating VPC vpc-01: %w", err)
		}

		// Wait for convergence
		time.Sleep(5 * time.Second)
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}
		if _, stderr, err := serverSSH.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
			return fmt.Errorf("cleaning up interfaces on %s: %w: %s", serverName, err, stderr)
		}
		cmd := fmt.Sprintf("/opt/bin/hhnet %s", netconfCmd)
		if _, stderr, err := serverSSH.Run(ctx, cmd); err != nil {
			return fmt.Errorf("bonding interfaces on %s: %w: %s", serverName, err, stderr)
		}
		// in case of L3 VPC mode, we need to give it time to switch to the longer lease time and switches to learn the routes
		if testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3Flat {
			time.Sleep(10 * time.Second)
		}

		return nil
	})

	// Wait for convergence
	time.Sleep(5 * time.Second)
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready: %w", err)
	}
	// Configure network interfaces on target server
	slog.Debug("Configuring network interfaces", "server", serverName, "netconfCmd", netconfCmd, "ifName", ifName)
	if _, stderr, err := serverSSH.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
		return false, reverts, fmt.Errorf("cleaning up interfaces on %s: %w: %s", serverName, err, stderr)
	}
	cmd := fmt.Sprintf("/opt/bin/hhnet %s", netconfCmd)
	hhnetStdOut, hhnetStdErr, hhnetErr := serverSSH.Run(ctx, cmd)
	if hhnetErr != nil {
		return false, reverts, fmt.Errorf("running hhnet %s on %s: %w: %s", netconfCmd, serverName, hhnetErr, hhnetStdErr)
	}
	slog.Debug("Network interface configured correctly", "server", serverName, "ifname", ifName, "DHCP address", strings.TrimSpace(hhnetStdOut))

	// Check DNS, NTP, MTU and DHCP lease
	slog.Debug("Checking DNS, NTP, MTU and DHCP lease")
	var dnsFound, ntpFound, mtuFound, leaseCheck, advRoutes bool
	if _, stderr, err := serverSSH.Run(ctx, "grep \"nameserver 1.1.1.1\" /etc/resolv.conf"); err != nil {
		slog.Error("1.1.1.1 not found in resolv.conf", "error", err, "stderr", stderr)
	} else {
		dnsFound = true
	}
	if _, stderr, err := serverSSH.Run(ctx, "timedatectl show-timesync | grep 1.1.1.1"); err != nil {
		slog.Error("1.1.1.1 not found in timesync", "error", err, "stderr", stderr)
	} else {
		ntpFound = true
	}
	if _, stderr, err := serverSSH.Run(ctx, fmt.Sprintf("ip link show dev %s | grep \"mtu 1400\"", ifName)); err != nil {
		slog.Error("mtu 1400 not found on server interface", "interface", ifName, "error", err, "stderr", stderr)
	} else {
		mtuFound = true
	}

	// make sure to check the DHCP lease time after initial short lease time for L3 VPC modes
	if l3mode {
		time.Sleep(10 * time.Second)
	}

	lease, leaseErr := fetchAndParseDHCPLease(ctx, serverSSH, ifName)
	if leaseErr != nil {
		slog.Error("failed to get lease time", "error", leaseErr)
	} else if err := checkDHCPLease(lease, 1800, 120); err != nil {
		slog.Error("DHCP lease time check failed", "error", err)
	} else {
		leaseCheck = true
	}
	stdout, stderr, advRoutesErr := serverSSH.Run(ctx, "ip route show")
	if advRoutesErr != nil {
		slog.Error("failed to get IP routes from server", "error", advRoutesErr, "stderr", stderr)
	} else if err := checkDHCPAdvRoutes(stdout, "9.9.9.9", subnet.Gateway, dhcpOpts.DisableDefaultRoute, l3mode, subnet.Subnet); err != nil {
		slog.Error("DHCP advertised routes check failed", "error", err)
	} else {
		advRoutes = true
	}

	if !dnsFound || !ntpFound || !mtuFound || !leaseCheck || !advRoutes {
		return false, reverts, fmt.Errorf("DNS: %v, NTP: %v, MTU: %v, DHCP lease: %v, Advertised Routes: %v", dnsFound, ntpFound, mtuFound, leaseCheck, advRoutes) //nolint:goerr113
	}

	return false, reverts, nil
}

// Test DHCP renewal on VPC-attached interfaces
// Uses 1 server by default, all servers in extended mode
// Sets VPC DHCPOptions to a shorter lease and reconfigures servers via networkctl
// Waits for DHCP renewal and checks lease time
func (testCtx *VPCPeeringTestCtx) dhcpRenewalTest(ctx context.Context) (bool, []RevertFunc, error) {
	// Find VPC with at least one server attached that has DHCP enabled
	vpcAttaches := &vpcapi.VPCAttachmentList{}
	if err := testCtx.kube.List(ctx, vpcAttaches); err != nil {
		return false, nil, fmt.Errorf("listing VPCAttachments: %w", err)
	}

	var testVPC *vpcapi.VPC
	testServers := []ServerWithInterface{}

	for _, attach := range vpcAttaches.Items {
		conn := &wiringapi.Connection{}
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
			Namespace: kmetav1.NamespaceDefault,
			Name:      attach.Spec.Connection,
		}, conn); err != nil {
			continue
		}

		_, serverNames, _, _, err := conn.Spec.Endpoints()
		if err != nil || len(serverNames) != 1 {
			continue
		}

		vpc := &vpcapi.VPC{}
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
			Namespace: kmetav1.NamespaceDefault,
			Name:      attach.Spec.VPCName(),
		}, vpc); err != nil {
			continue
		}

		subnetName := attach.Spec.SubnetName()
		subnet := vpc.Spec.Subnets[subnetName]
		if subnet == nil || !subnet.DHCP.Enable {
			continue
		}

		// Determine the interface name based on connection type
		var ifName string
		if conn.Spec.Unbundled != nil {
			ifName = fmt.Sprintf("%s.%d", conn.Spec.Unbundled.Link.Server.LocalPortName(), subnet.VLAN)
		} else {
			ifName = fmt.Sprintf("bond0.%d", subnet.VLAN)
		}

		// If this is the first VPC, save it
		if testVPC == nil {
			testVPC = vpc
		}

		// Collect servers from the same VPC
		if testVPC.Name == vpc.Name {
			testServers = append(testServers, ServerWithInterface{
				Name:      serverNames[0],
				Interface: ifName,
			})
		}

		// In non-extended mode, stop after finding first server
		if !testCtx.extended && len(testServers) > 0 {
			break
		}
	}

	if testVPC == nil || len(testServers) == 0 {
		slog.Info("No servers with DHCP-enabled VPC attachments found, skipping DHCP renewal test")

		return true, nil, fmt.Errorf("no servers with DHCP-enabled VPC attachments found") //nolint:goerr113
	}

	testServerCount := 1
	if testCtx.extended {
		testServerCount = len(testServers)
	}

	testServers = testServers[:testServerCount]
	slog.Info("Testing DHCP renewal", "servers", len(testServers), "vpc", testVPC.Name)

	// Save original lease times for all DHCP-enabled subnets
	originalLeaseTimes := make(map[string]uint32)
	for subnetName, subnet := range testVPC.Spec.Subnets {
		if subnet.DHCP.Enable && subnet.DHCP.Options != nil {
			originalLeaseTimes[subnetName] = subnet.DHCP.Options.LeaseTimeSeconds
		}
	}

	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Reverting DHCP lease time for all subnets", "vpc", testVPC.Name)

		// Restore original lease times
		for subnetName, originalLeaseTime := range originalLeaseTimes {
			if subnet := testVPC.Spec.Subnets[subnetName]; subnet != nil && subnet.DHCP.Options != nil {
				subnet.DHCP.Options.LeaseTimeSeconds = originalLeaseTime
			}
		}

		_, err := CreateOrUpdateVpc(ctx, testCtx.kube, testVPC)
		if err != nil {
			return fmt.Errorf("reverting VPC %s DHCP lease times: %w", testVPC.Name, err)
		}

		time.Sleep(5 * time.Second)

		return WaitReady(ctx, testCtx.kube, testCtx.wrOpts)
	})

	shortLeaseTime := uint32(60)
	slog.Debug("Setting short DHCP lease time for all subnets", "vpc", testVPC.Name, "lease_time", shortLeaseTime)

	// Set short lease time for all DHCP-enabled subnets in the VPC
	for _, subnet := range testVPC.Spec.Subnets {
		if !subnet.DHCP.Enable {
			continue
		}

		if subnet.DHCP.Options == nil {
			subnet.DHCP.Options = &vpcapi.VPCDHCPOptions{
				DNSServers:       []string{},
				TimeServers:      []string{},
				InterfaceMTU:     9036,
				AdvertisedRoutes: []vpcapi.VPCDHCPRoute{},
			}
		}

		subnet.DHCP.Options.LeaseTimeSeconds = shortLeaseTime
	}

	change, err := CreateOrUpdateVpc(ctx, testCtx.kube, testVPC)
	if err != nil || !change {
		return false, reverts, fmt.Errorf("updating VPC %s with short lease time: %w", testVPC.Name, err)
	}

	slog.Debug("Waiting for DHCP configuration to propagate")
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready after DHCP change: %w", err)
	}

	// Give extra time for DHCP server to pick up the new lease configuration
	// This ensures that when we test renewal, the server will give out the correct lease time
	slog.Debug("Waiting for DHCP server to apply new lease time configuration")
	time.Sleep(30 * time.Second)

	var wg sync.WaitGroup
	results := make(chan RenewalResult, len(testServers))

	for _, server := range testServers {
		wg.Add(1)
		go func(srv ServerWithInterface) {
			defer wg.Done()

			start := time.Now()
			err := testCtx.waitForDHCPRenewal(ctx, srv.Name, srv.Interface, shortLeaseTime)
			duration := time.Since(start)

			result := RenewalResult{
				Server:   srv.Name,
				Duration: duration,
				Error:    err,
			}

			results <- result
		}(server)
	}

	wg.Wait()
	close(results)

	var failures []string
	successCount := 0
	maxDuration := time.Duration(0)

	for result := range results {
		if result.Error != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", result.Server, result.Error))
		} else {
			successCount++
			if result.Duration > maxDuration {
				maxDuration = result.Duration
			}
		}
	}

	if len(failures) > 0 {
		return false, reverts, fmt.Errorf("DHCP renewal failures: %v", failures) //nolint:goerr113
	}

	slog.Info("DHCP renewal test completed successfully", "servers", len(testServers), "maxDuration", maxDuration)

	return false, reverts, nil
}

// Test DHCP pool exhaustion and recovery behavior
// Requires N >= 2 servers attached to same DHCP-enabled subnet
// Shrinks the DHCP pool to N-1 addresses so N-1 servers deplete it
// Verifies the Nth server cannot get an IP, then releases one and verifies recovery
func (testCtx *VPCPeeringTestCtx) dhcpDepletionTest(ctx context.Context) (bool, []RevertFunc, error) {
	const minServers = 2

	// Find all servers attached to DHCP-enabled VPC subnets
	serversBySubnet, err := findDHCPEnabledServers(ctx, testCtx.kube)
	if err != nil {
		return false, nil, err
	}

	// Find the VPC/subnet with the most servers (at least minServers)
	var testVPC *vpcapi.VPC
	var testSubnetName string
	var testServers []ServerWithInterface
	maxServers := 0

	for _, servers := range serversBySubnet {
		if len(servers) >= minServers && len(servers) > maxServers {
			// All servers in a group have the same VPC
			testVPC = servers[0].VPC
			testSubnetName = servers[0].SubnetName
			testServers = make([]ServerWithInterface, len(servers))
			for i, s := range servers {
				testServers[i] = ServerWithInterface{
					Name:      s.ServerName,
					Interface: s.Interface,
				}
			}
			maxServers = len(servers)
		}
	}

	if testVPC == nil {
		slog.Info("Not enough servers with DHCP-enabled VPC attachments found, skipping DHCP depletion test", "min_required", minServers)

		return true, nil, nil
	}

	// Calculate how many IPs we need for depletion (one less than total servers)
	numServersToDeplete := len(testServers) - 1
	poolSize := uint32(numServersToDeplete) //nolint:gosec // Safe: server count is small

	// Log server details
	serverNames := make([]string, len(testServers))
	for i, s := range testServers {
		serverNames[i] = s.Name
	}

	slog.Info("Testing DHCP pool depletion and recovery", "vpc", testVPC.Name, "subnet", testSubnetName, "total_servers", len(testServers), "pool_size", poolSize, "servers", serverNames, "last_server", testServers[len(testServers)-1].Name)

	subnet := testVPC.Spec.Subnets[testSubnetName]

	// Save original range
	originalRangeStart := subnet.DHCP.Range.Start
	originalRangeEnd := subnet.DHCP.Range.End

	// Shrink range to exactly poolSize IPs
	startIP := net.ParseIP(subnet.DHCP.Range.Start)
	if startIP == nil {
		return false, nil, fmt.Errorf("invalid DHCP range start: %s", subnet.DHCP.Range.Start) //nolint:goerr113
	}

	startInt := binary.BigEndian.Uint32(startIP.To4())
	newEndInt := startInt + poolSize - 1 // poolSize IPs total
	newEndIP := make(net.IP, 4)
	binary.BigEndian.PutUint32(newEndIP, newEndInt)

	subnet.DHCP.Range.End = newEndIP.String()

	slog.Info("Shrinking DHCP range for depletion test", "original_start", originalRangeStart, "original_end", originalRangeEnd, "new_start", subnet.DHCP.Range.Start, "new_end", subnet.DHCP.Range.End, "pool_size", poolSize)

	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Reverting DHCP range", "vpc", testVPC.Name, "subnet", testSubnetName)

		subnet := testVPC.Spec.Subnets[testSubnetName]
		subnet.DHCP.Range.Start = originalRangeStart
		subnet.DHCP.Range.End = originalRangeEnd

		_, err := CreateOrUpdateVpc(ctx, testCtx.kube, testVPC)
		if err != nil {
			return fmt.Errorf("reverting VPC %s DHCP range: %w", testVPC.Name, err)
		}

		time.Sleep(5 * time.Second)

		return WaitReady(ctx, testCtx.kube, testCtx.wrOpts)
	})

	// Update VPC with shrunk range
	change, err := CreateOrUpdateVpc(ctx, testCtx.kube, testVPC)
	if err != nil || !change {
		return false, reverts, fmt.Errorf("updating VPC %s with shrunk range: %w", testVPC.Name, err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready after range change: %w", err)
	}

	time.Sleep(10 * time.Second)

	// First, flush IPs and bring interfaces down to prevent automatic DHCP requests
	// This ensures we have full control over when each server requests an IP
	slog.Info("Flushing existing IPs and bringing interfaces down on all servers")

	for _, server := range testServers {
		ssh, err := testCtx.getSSH(ctx, server.Name)
		if err != nil {
			return false, reverts, fmt.Errorf("getting ssh config for server %s: %w", server.Name, err)
		}

		// Flush IP and bring interface down to prevent networkd from auto-requesting DHCP
		_, _, err = ssh.Run(ctx, fmt.Sprintf("sudo ip addr flush dev %s && sudo ip link set %s down", server.Interface, server.Interface))
		if err != nil {
			return false, reverts, fmt.Errorf("flushing IP and bringing down interface on %s: %w", server.Name, err)
		}
	}

	// Clear all DHCP allocations from the DHCPSubnet to ensure clean state
	dhcpSubnetName := fmt.Sprintf("%s--%s", testVPC.Name, testSubnetName)
	dhcpSubnet := &dhcpapi.DHCPSubnet{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
		Namespace: kmetav1.NamespaceDefault,
		Name:      dhcpSubnetName,
	}, dhcpSubnet); err != nil {
		return false, reverts, fmt.Errorf("getting DHCPSubnet %s: %w", dhcpSubnetName, err)
	}

	// Clear all allocations to start fresh
	dhcpSubnet.Status.Allocated = make(map[string]dhcpapi.DHCPAllocated)
	if err := testCtx.kube.Status().Update(ctx, dhcpSubnet); err != nil {
		return false, reverts, fmt.Errorf("clearing DHCPSubnet allocations: %w", err)
	}

	slog.Debug("Cleared DHCP allocations", "subnet", dhcpSubnetName)

	time.Sleep(5 * time.Second)

	// Reconfigure first N-1 servers to deplete the pool
	slog.Info("Depleting DHCP pool", "num_servers", numServersToDeplete)

	// Parse the DHCP range to validate assigned IPs are within it
	rangeStartIP := net.ParseIP(subnet.DHCP.Range.Start)
	rangeEndIP := net.ParseIP(subnet.DHCP.Range.End)
	rangeStartInt := binary.BigEndian.Uint32(rangeStartIP.To4())
	rangeEndInt := binary.BigEndian.Uint32(rangeEndIP.To4())

	for i := 0; i < numServersToDeplete; i++ {
		server := testServers[i]
		slog.Debug("Requesting IP for depletion", "server", server.Name, "index", i)

		ssh, err := testCtx.getSSH(ctx, server.Name)
		if err != nil {
			return false, reverts, fmt.Errorf("getting ssh config for server %s: %w", server.Name, err)
		}

		// Bring interface up and reconfigure to trigger DHCP request
		_, _, err = ssh.Run(ctx, fmt.Sprintf("sudo ip link set %s up && sudo networkctl reconfigure %s", server.Interface, server.Interface))
		if err != nil {
			return false, reverts, fmt.Errorf("bringing up and reconfiguring interface on %s: %w", server.Name, err)
		}

		// Wait for IP assignment with retries
		gotIP, err := waitForDHCPIP(ctx, ssh, server.Interface, 10)
		if err != nil {
			return false, reverts, fmt.Errorf("waiting for IP on %s: %w", server.Name, err)
		}

		if gotIP == "" {
			return false, reverts, fmt.Errorf("server %s did not get IP during depletion phase", server.Name) //nolint:goerr113
		}

		// Validate that the assigned IP is within the configured DHCP range
		gotIPParsed := net.ParseIP(gotIP)
		if gotIPParsed == nil {
			return false, reverts, fmt.Errorf("server %s got invalid IP %s", server.Name, gotIP) //nolint:goerr113
		}

		gotIPInt := binary.BigEndian.Uint32(gotIPParsed.To4())
		if gotIPInt < rangeStartInt || gotIPInt > rangeEndInt {
			return false, reverts, fmt.Errorf("server %s got IP %s which is outside configured range %s-%s", server.Name, gotIP, subnet.DHCP.Range.Start, subnet.DHCP.Range.End) //nolint:goerr113
		}

		slog.Debug("Server got IP during depletion", "server", server.Name, "ip", gotIP)
	}

	// Verify pool is depleted by trying to get IP on the last server
	lastServer := testServers[len(testServers)-1]
	slog.Info("Verifying pool depletion on last server", "server", lastServer.Name)

	ssh, err := testCtx.getSSH(ctx, lastServer.Name)
	if err != nil {
		return false, reverts, fmt.Errorf("getting ssh config for server %s: %w", lastServer.Name, err)
	}

	// Bring interface up and reconfigure to trigger DHCP request
	_, _, err = ssh.Run(ctx, fmt.Sprintf("sudo ip link set %s up && sudo networkctl reconfigure %s", lastServer.Interface, lastServer.Interface))
	if err != nil {
		return false, reverts, fmt.Errorf("bringing up and reconfiguring interface on %s: %w", lastServer.Name, err)
	}

	time.Sleep(5 * time.Second)

	// Check that last server did NOT get an IP
	assignedIP, err := getInterfaceIPv4(ctx, ssh, lastServer.Interface)
	if err != nil {
		return false, reverts, fmt.Errorf("getting IP address from %s: %w", lastServer.Name, err)
	}
	if assignedIP != "" {
		return false, reverts, fmt.Errorf("last server %s got IP %s when pool should be depleted", lastServer.Name, assignedIP) //nolint:goerr113
	}

	slog.Info("Pool depletion verified - last server has no IP")

	// Release IP from first server by flushing interface and clearing DHCP allocation
	slog.Info("Releasing IP from first server", "server", testServers[0].Name)

	firstSSH, err := testCtx.getSSH(ctx, testServers[0].Name)
	if err != nil {
		return false, reverts, fmt.Errorf("getting ssh config for server %s: %w", testServers[0].Name, err)
	}

	// Get MAC address of the first server's interface to clear its DHCP allocation
	firstServerMAC, err := getInterfaceMAC(ctx, firstSSH, testServers[0].Interface)
	if err != nil {
		return false, reverts, fmt.Errorf("getting MAC address from %s: %w", testServers[0].Name, err)
	}

	_, _, err = firstSSH.Run(ctx, fmt.Sprintf("sudo ip addr flush dev %s", testServers[0].Interface))
	if err != nil {
		return false, reverts, fmt.Errorf("flushing IP on %s: %w", testServers[0].Name, err)
	}

	// Clear the DHCP allocation for this server to free up the IP
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
		Namespace: kmetav1.NamespaceDefault,
		Name:      dhcpSubnetName,
	}, dhcpSubnet); err != nil {
		return false, reverts, fmt.Errorf("getting DHCPSubnet %s: %w", dhcpSubnetName, err)
	}

	delete(dhcpSubnet.Status.Allocated, firstServerMAC)
	if err := testCtx.kube.Status().Update(ctx, dhcpSubnet); err != nil {
		return false, reverts, fmt.Errorf("clearing DHCP allocation for %s: %w", testServers[0].Name, err)
	}

	slog.Debug("Cleared DHCP allocation for first server", "server", testServers[0].Name, "mac", firstServerMAC)

	time.Sleep(5 * time.Second)

	// Now try to get IP on last server again - should succeed
	slog.Info("Attempting to get IP on last server after freeing one IP")

	// Reconfigure to trigger new DHCP request (interface is already up from depletion check)
	_, _, err = ssh.Run(ctx, fmt.Sprintf("sudo networkctl reconfigure %s", lastServer.Interface))
	if err != nil {
		return false, reverts, fmt.Errorf("reconfiguring interface on %s after freeing IP: %w", lastServer.Name, err)
	}

	// Wait for IP assignment with retries
	recoveredIP, err := waitForDHCPIP(ctx, ssh, lastServer.Interface, 10)
	if err != nil {
		return false, reverts, fmt.Errorf("waiting for IP on %s after recovery: %w", lastServer.Name, err)
	}

	if recoveredIP == "" {
		return false, reverts, fmt.Errorf("last server %s did not get IP after pool recovery", lastServer.Name) //nolint:goerr113
	}

	slog.Info("DHCP pool recovery verified", "server", lastServer.Name, "ip", recoveredIP)

	slog.Info("DHCP depletion test completed successfully")

	return false, reverts, nil
}

// Test RoCE functionality and DSCP traffic marking by enabling RoCE on a leaf switch
// with servers, generating DSCP 24 marked traffic, and verifying UC3 queue counters.
func (testCtx *VPCPeeringTestCtx) roceBasicTest(ctx context.Context) (bool, []RevertFunc, error) {
	// this should never fail
	if len(testCtx.roceLeaves) == 0 {
		slog.Error("RoCE leaves not specified, skipping RoCE basic test")

		return true, nil, errNoRoceLeaves
	}

	// Find a RoCE-capable leaf with server connections
	var swName string
	sw := &wiringapi.Switch{}
outer:
	for _, candidateSwitch := range testCtx.roceLeaves {
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: candidateSwitch}, sw); err != nil {
			return false, nil, fmt.Errorf("getting switch %s: %w", swName, err)
		}
		// Skip switches that are unused
		// FIXME: hack based on description, we should have a proper way to identify unused switches
		if strings.Contains(sw.Spec.Description, "unused") {
			slog.Debug("Skipping unused switch", "switch", candidateSwitch)

			continue
		}

		connList := &wiringapi.ConnectionList{}
		if err := testCtx.kube.List(ctx, connList, kclient.MatchingLabels{
			wiringapi.ListLabelSwitch(candidateSwitch): wiringapi.ListLabelValue,
		}); err != nil {
			return false, nil, fmt.Errorf("listing connections for switch %s: %w", candidateSwitch, err)
		}

		for _, conn := range connList.Items {
			_, servers, _, _, err := conn.Spec.Endpoints()
			if err == nil && len(servers) > 0 {
				swName = candidateSwitch
				slog.Debug("Selected RoCE leaf with servers", "switch", swName)

				break outer
			}
		}
		slog.Debug("Skipping RoCE leaf with no servers", "switch", candidateSwitch)
	}

	if swName == "" {
		slog.Info("No RoCE-capable leaves with servers found, skipping test")

		return true, nil, fmt.Errorf("no RoCE leaves with servers found") //nolint:goerr113
	}
	slog.Debug("Using RoCE leaf switch for test", "switch", swName)

	swSSH, sshErr := testCtx.getSSH(ctx, swName)
	if sshErr != nil {
		return false, nil, fmt.Errorf("getting ssh config for switch %s: %w", swName, sshErr)
	}

	// enable RoCE on the switch if not already enabled
	if err := setRoCE(ctx, testCtx.kube, swName, true); err != nil {
		return false, nil, fmt.Errorf("enabling RoCE on switch %s: %w", swName, err)
	}

	dscpOpts := testCtx.tcOpts
	dscpOpts.IPerfsDSCP = 24 // Mapped to traffic class 3

	slog.Debug("Clearing queue counters on switch", "switch", swName)
	if err := execConfigCmd(ctx, swSSH, swName, "clear queue counters"); err != nil {
		return false, nil, fmt.Errorf("clearing queue counters on switch %s: %w", swName, err)
	}

	slog.Debug("Testing connectivity with DSCP options", "dscpOpts", dscpOpts)
	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, dscpOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity with DSCP opts: %w", err)
	}

	// check counters on the RoCE enabled switch for UC3 traffic. they are stored as part of the switch agent status
	// retry a few times as we only sync up stats every 15 seconds or so, and sometimes we appear to only update
	// partial statistics (e.g. packets transmitted but not bits)
	maxAttempts := 6
	minTransmitBits := uint64(5000)
	minTransmitPkts := uint64(50)
	for i := 0; i < maxAttempts; i++ {
		slog.Debug("Checking UC3 transmit bits on switch", "switch", swName, "attempt", i+1)
		uc3Map := make(map[string]bool)
		agent := &agentapi.Agent{}
		err := testCtx.kube.Get(ctx, kclient.ObjectKey{Name: swName, Namespace: kmetav1.NamespaceDefault}, agent)
		if err != nil {
			return false, nil, fmt.Errorf("getting agent %s: %w", swName, err)
		}

		for iface, ifaceStats := range agent.Status.State.Interfaces {
			if ifaceStats.OperStatus != agentapi.OperStatusUp {
				continue
			}
			if ifaceStats.Counters == nil {
				slog.Debug("No counters for operUp interface", "switch", swName, "interface", iface)

				continue
			}
			uc3, ok := ifaceStats.Counters.Queues["UC3"]
			if !ok {
				slog.Debug("No UC3 queue counters for operUP interface", "switch", swName, "interface", iface)

				continue
			}
			if uc3.TransmitBits > minTransmitBits {
				slog.Debug("Found UC3 transmit bits on interface", "switch", swName, "interface", iface, "uc3TransmitBits", uc3.TransmitBits)
				uc3Map[iface] = true
			} else if uc3.TransmitPkts > minTransmitPkts {
				slog.Debug("Found UC3 transmit packets but no bits on interface", "switch", swName, "interface", iface, "uc3TransmitPackets", uc3.TransmitPkts)
				uc3Map[iface] = true
			}
		}

		if len(uc3Map) != 0 {
			slog.Debug("UC3 transmit bits/pkts found on switch", "switch", swName, "uc3Map", uc3Map)

			break
		}
		if i < maxAttempts-1 {
			slog.Debug("No UC3 transmit bits/pkts found yet, retrying in 10 seconds", "switch", swName, "attempt", i+1, "maxAttempts", maxAttempts)
			time.Sleep(10 * time.Second)
		} else {
			return false, nil, fmt.Errorf("no UC3 transmit bits/pkts found on switch %s after %d attempts", swName, maxAttempts) //nolint:goerr113
		}
	}

	return false, nil, nil
}
