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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
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
			Name: "DHCP static lease",
			F:    testCtx.dhcpStaticLeaseTest,
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
					slog.Warn("Retrying", "delay", sleepTime)
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

// Basic test for gateway failover.
// Creates a custom gateway group with explicit priorities, sets up gateway peering using
// that group, then shuts down all spine ports connected to the primary gateway.
// After restoring, tests connectivity again.
// Requires at least 2 gateways and 2 VPCs.
func (testCtx *VPCPeeringTestCtx) gatewayFailoverTest(ctx context.Context) (bool, []RevertFunc, error) {
	var returnErr error

	// list gateways
	gateways := &gwapi.GatewayList{}
	if err := testCtx.kube.List(ctx, gateways); err != nil {
		return false, nil, fmt.Errorf("listing gateways: %w", err)
	}
	if len(gateways.Items) < 2 {
		slog.Info("Not enough gateways found, skipping test", "gateways", len(gateways.Items))

		return true, nil, errNotEnoughGateways
	}

	// list VPCs for gateway peering setup
	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		slog.Info("Not enough VPCs found for gateway peering, skipping test", "vpcs", len(vpcs.Items))

		return true, nil, errNotEnoughVPCs
	}

	// sort gateways alphabetically for consistent selection
	sort.Slice(gateways.Items, func(i, j int) bool {
		return gateways.Items[i].Name < gateways.Items[j].Name
	})

	// create a custom gateway group to exercise the GatewayGroup API
	const failoverTestGroup = "failover-test"
	const (
		primaryPriority = uint32(100)
		backupPriority  = uint32(50)
	)

	slog.Debug("Creating gateway group for failover test", "group", failoverTestGroup)
	gwGroup := &gwapi.GatewayGroup{
		TypeMeta: kmetav1.TypeMeta{
			Kind:       "GatewayGroup",
			APIVersion: gwapi.GroupVersion.String(),
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      failoverTestGroup,
			Namespace: kmetav1.NamespaceDefault,
		},
	}
	if err := testCtx.kube.Create(ctx, gwGroup); err != nil {
		return false, nil, fmt.Errorf("creating gateway group %s: %w", failoverTestGroup, err)
	}

	// store original group specs for revert (deep copy to avoid modification by append)
	originalGroups := make(map[string][]gwapi.GatewayGroupMembership)
	for i := range gateways.Items {
		originalGroups[gateways.Items[i].Name] = slices.Clone(gateways.Items[i].Spec.Groups)
	}

	// add gateways to the new group with explicit priorities
	// first gateway (alphabetically) gets higher priority (100), second gets lower (50)
	slog.Debug("Adding gateways to failover test group", "group", failoverTestGroup,
		"primary", gateways.Items[0].Name, "primaryPriority", primaryPriority,
		"backup", gateways.Items[1].Name, "backupPriority", backupPriority)

	gateways.Items[0].Spec.Groups = append(gateways.Items[0].Spec.Groups,
		gwapi.GatewayGroupMembership{Name: failoverTestGroup, Priority: primaryPriority})
	gateways.Items[1].Spec.Groups = append(gateways.Items[1].Spec.Groups,
		gwapi.GatewayGroupMembership{Name: failoverTestGroup, Priority: backupPriority})

	if err := testCtx.kube.Update(ctx, &gateways.Items[0]); err != nil {
		return false, nil, fmt.Errorf("updating gateway %s group membership: %w", gateways.Items[0].Name, err)
	}
	if err := testCtx.kube.Update(ctx, &gateways.Items[1]); err != nil {
		return false, nil, fmt.Errorf("updating gateway %s group membership: %w", gateways.Items[1].Name, err)
	}

	// set up gateway peering between the first two VPCs using the custom group
	slog.Debug("Setting up gateway peering for failover test", "group", failoverTestGroup)
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	gwPeerings := make(map[string]*gwapi.PeeringSpec, 1)
	appendGwPeeringSpec(gwPeerings, &vpcs.Items[0], &vpcs.Items[1], nil, nil)
	// set the peering to use our custom gateway group
	for _, spec := range gwPeerings {
		spec.GatewayGroup = failoverTestGroup
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, gwPeerings, true); err != nil {
		return false, nil, fmt.Errorf("setting up gateway peerings: %w", err)
	}

	// the target is the gateway with highest priority (we set it explicitly above)
	targetGateway := gateways.Items[0].Name
	slog.Debug("Selected gateway for failover test (highest priority)", "gateway", targetGateway, "priority", primaryPriority)

	// find all gateway connections for the target gateway
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeGateway}); err != nil {
		return false, nil, fmt.Errorf("listing gateway connections: %w", err)
	}

	// collect spine ports to shut down and their SSH configs
	type spinePort struct {
		spineName   string
		nosPortName string
	}
	spinePorts := make([]spinePort, 0)
	spinesSSH := make(map[string]*sshutil.Config)
	spineProfiles := make(map[string]*wiringapi.SwitchProfile)
	spineSpecs := make(map[string]*wiringapi.SwitchSpec)

	for _, conn := range conns.Items {
		if conn.Spec.Gateway == nil {
			continue
		}
		for _, link := range conn.Spec.Gateway.Links {
			// check if this link connects to our target gateway
			gatewayPort := link.Gateway.Port
			if !strings.HasPrefix(gatewayPort, targetGateway+"/") {
				continue
			}

			spineName := link.Switch.DeviceName()
			spinePortName := link.Switch.LocalPortName()

			slog.Debug("Found gateway link", "gateway", targetGateway, "spine", spineName, "port", spinePortName)

			// get SSH config for this spine if we don't have it yet
			if _, ok := spinesSSH[spineName]; !ok {
				sshCfg, sshErr := testCtx.getSSH(ctx, spineName)
				if sshErr != nil {
					return false, nil, fmt.Errorf("getting ssh config for spine switch %s: %w", spineName, sshErr)
				}
				spinesSSH[spineName] = sshCfg

				// get switch and profile for port mapping
				sw := &wiringapi.Switch{}
				if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: spineName}, sw); err != nil {
					return false, nil, fmt.Errorf("getting switch %s: %w", spineName, err)
				}
				spineSpecs[spineName] = &sw.Spec

				profile := &wiringapi.SwitchProfile{}
				if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: sw.Spec.Profile}, profile); err != nil {
					return false, nil, fmt.Errorf("getting switch profile %s: %w", sw.Spec.Profile, err)
				}
				spineProfiles[spineName] = profile
			}

			// map API port name to NOS port name
			portMap, err := spineProfiles[spineName].Spec.GetAPI2NOSPortsFor(spineSpecs[spineName])
			if err != nil {
				return false, nil, fmt.Errorf("getting API2NOS ports for switch %s: %w", spineName, err)
			}
			nosPortName, ok := portMap[spinePortName]
			if !ok {
				return false, nil, fmt.Errorf("port %s not found in switch profile for switch %s", spinePortName, spineName) //nolint:goerr113
			}

			spinePorts = append(spinePorts, spinePort{spineName: spineName, nosPortName: nosPortName})
		}
	}

	if len(spinePorts) == 0 {
		return false, nil, fmt.Errorf("no spine ports found for gateway %s", targetGateway) //nolint:goerr113
	}

	slog.Debug("Found spine ports to shut down", "gateway", targetGateway, "ports", len(spinePorts))

	// disable agent on all involved spines to prevent ports up
	// track which spines were disabled so we can roll back on error
	disabledSpines := make([]string, 0, len(spinesSSH))
	for spineName, spineSSH := range spinesSSH {
		slog.Debug("Disabling HH agent on spine", "spine", spineName)
		if err := changeAgentStatus(ctx, spineSSH, spineName, false); err != nil {
			// rollback: re-enable agents on already-disabled spines to avoid leaving them in a bad state
			for _, ds := range disabledSpines {
				slog.Debug("Re-enabling HH agent on spine after error", "spine", ds)
				if reErr := changeAgentStatus(ctx, spinesSSH[ds], ds, true); reErr != nil {
					slog.Warn("Failed to re-enable HH agent on spine during rollback", "spine", ds, "error", reErr)
				}
			}

			return false, nil, fmt.Errorf("disabling HH agent on spine %s: %w", spineName, err)
		}
		disabledSpines = append(disabledSpines, spineName)
	}

	// shut down all spine ports connected to the target gateway
	for _, sp := range spinePorts {
		slog.Debug("Shutting down spine port", "spine", sp.spineName, "port", sp.nosPortName)
		if err := changeSwitchPortStatus(ctx, spinesSSH[sp.spineName], sp.spineName, sp.nosPortName, false); err != nil {
			returnErr = fmt.Errorf("setting switch port down: %w", err)

			break
		}
	}

	if returnErr == nil {
		// wait for fabric to converge; with BFD this should be sub-second, but we give it some buffer
		slog.Debug("Waiting 10 seconds for fabric to converge after failover")
		time.Sleep(10 * time.Second)

		// test connectivity during failover
		slog.Debug("Testing connectivity during failover")
		if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
			returnErr = fmt.Errorf("connectivity test during failover: %w", err)
		}
	}

	// re-enable agents on all spines
	for spineName, spineSSH := range spinesSSH {
		maxRetries := 5
		sleepTime := time.Second * 5
		enabled := false
		for i := range maxRetries {
			if err := changeAgentStatus(ctx, spineSSH, spineName, true); err != nil {
				slog.Error("Enabling HH agent", "switch", spineName, "error", err)
				if i < maxRetries-1 {
					slog.Warn("Retrying", "delay", sleepTime)
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
			returnErr = errors.Join(returnErr, fmt.Errorf("could not enable HH agent on switch %s after %d attempts", spineName, maxRetries)) //nolint:goerr113
		} else {
			returnErr = fmt.Errorf("could not enable HH agent on switch %s after %d attempts", spineName, maxRetries) //nolint:goerr113
		}
	}

	// test connectivity after revert (both gateways should be working)
	if returnErr == nil {
		// BFD provides sub-second failover detection; 10s buffer for agent restart + route reconvergence
		slog.Debug("Waiting for fabric to converge after revert")
		time.Sleep(10 * time.Second)

		slog.Debug("Testing connectivity after revert")
		if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
			returnErr = fmt.Errorf("connectivity test after revert: %w", err)
		}
	}

	// restore original gateway group memberships
	slog.Debug("Restoring original gateway group memberships")
	for i := range gateways.Items {
		gw := &gateways.Items[i]
		// re-fetch to get latest version
		if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: gw.Namespace, Name: gw.Name}, gw); err != nil {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("fetching gateway %s for restore: %w", gw.Name, err))
			} else {
				returnErr = fmt.Errorf("fetching gateway %s for restore: %w", gw.Name, err)
			}

			continue
		}
		gw.Spec.Groups = originalGroups[gw.Name]
		if err := testCtx.kube.Update(ctx, gw); err != nil {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("restoring gateway %s group membership: %w", gw.Name, err))
			} else {
				returnErr = fmt.Errorf("restoring gateway %s group membership: %w", gw.Name, err)
			}
		}
	}

	// delete the custom gateway group
	slog.Debug("Deleting failover test gateway group", "group", failoverTestGroup)
	if err := testCtx.kube.Delete(ctx, gwGroup); err != nil {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("deleting gateway group %s: %w", failoverTestGroup, err))
		} else {
			returnErr = fmt.Errorf("deleting gateway group %s: %w", failoverTestGroup, err)
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
						slog.Warn("Retrying", "delay", sleepTime)
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

// testStaticIPAssignment tests that a server gets the expected static IP assignment.
// It updates the VPC with the static allocation, reconfigures the server's interface,
// and verifies both the assigned IP and the DHCPSubnet status.
func (testCtx *VPCPeeringTestCtx) testStaticIPAssignment(ctx context.Context, vpc *vpcapi.VPC, subnetName string, server ServerWithInterface, serverMAC string, staticIP net.IP, description string) error {
	slog.Info("Testing static IP assignment", "description", description, "ip", staticIP.String(), "mac", serverMAC)

	// Add static allocation
	subnet := vpc.Spec.Subnets[subnetName]
	if subnet.DHCP.Static == nil {
		subnet.DHCP.Static = make(map[string]vpcapi.VPCDHCPStatic)
	}
	subnet.DHCP.Static[serverMAC] = vpcapi.VPCDHCPStatic{IP: staticIP.String()}

	change, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil || !change {
		return fmt.Errorf("updating VPC %s with static allocation: %w", vpc.Name, err)
	}

	slog.Debug("Waiting for DHCP configuration to propagate")
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return fmt.Errorf("waiting for ready after DHCP change: %w", err)
	}

	time.Sleep(10 * time.Second)

	// Reconfigure interface to get new IP
	ssh, err := testCtx.getSSH(ctx, server.Name)
	if err != nil {
		return fmt.Errorf("getting ssh config for server %s: %w", server.Name, err)
	}

	_, stderr, err := ssh.Run(ctx, fmt.Sprintf("sudo networkctl reconfigure %s", server.Interface))
	if err != nil {
		if stderr != "" {
			return fmt.Errorf("reconfiguring interface: %w (stderr: %s)", err, stderr)
		}

		return fmt.Errorf("reconfiguring interface: %w", err)
	}

	time.Sleep(5 * time.Second)

	// Verify server got the static IP
	assignedIP, err := getInterfaceIPv4(ctx, ssh, server.Interface)
	if err != nil {
		return fmt.Errorf("getting IP address from %s: %w", server.Name, err)
	}
	if assignedIP != staticIP.String() {
		return fmt.Errorf("server %s did not get static IP: expected %s, got %s", server.Name, staticIP.String(), assignedIP) //nolint:goerr113
	}

	slog.Info("Static IP assignment verified", "description", description, "server", server.Name, "ip", assignedIP)

	// Verify static IP is reserved in DHCPSubnet status
	dhcpSubnetName := fmt.Sprintf("%s--%s", vpc.Name, subnetName)
	dhcpSubnet := &dhcpapi.DHCPSubnet{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{
		Namespace: kmetav1.NamespaceDefault,
		Name:      dhcpSubnetName,
	}, dhcpSubnet); err != nil {
		return fmt.Errorf("getting DHCPSubnet %s: %w", dhcpSubnetName, err)
	}

	allocation, found := dhcpSubnet.Status.Allocated[serverMAC]
	if !found {
		return fmt.Errorf("static allocation for MAC %s not found in DHCPSubnet status", serverMAC) //nolint:goerr113
	}

	if allocation.IP != staticIP.String() {
		return fmt.Errorf("DHCPSubnet allocation mismatch: expected %s, got %s", staticIP.String(), allocation.IP) //nolint:goerr113
	}

	slog.Info("Static IP test passed", "description", description, "ip", staticIP.String())

	return nil
}

// Test DHCP static reservations within dynamic pool range
// Verifies that static IP assignments work correctly both within and outside the dynamic range.
// The test finds any server on any subnet, saves the existing DHCP config (if any),
// forces a hardcoded DHCP config, runs tests, and restores the original config.
func (testCtx *VPCPeeringTestCtx) dhcpStaticLeaseTest(ctx context.Context) (bool, []RevertFunc, error) {
	// 1. Find any server attached to any VPC subnet (regardless of DHCP config)
	serverInfo, err := findAnyAttachedServer(ctx, testCtx.kube)
	if errors.Is(err, errNoAttachedServers) {
		slog.Info("No servers with VPC attachments found, skipping DHCP static lease test")

		return true, nil, errNoAttachedServers
	}
	if err != nil {
		return false, nil, err
	}

	ssh, err := testCtx.getSSH(ctx, serverInfo.ServerName)
	if err != nil {
		return false, nil, fmt.Errorf("getting ssh config for server %s: %w", serverInfo.ServerName, err)
	}

	serverMAC, err := getInterfaceMAC(ctx, ssh, serverInfo.Interface)
	if err != nil || serverMAC == "" {
		return false, nil, fmt.Errorf("getting MAC address for server %s interface %s: %w", serverInfo.ServerName, serverInfo.Interface, err)
	}

	testServer := ServerWithInterface{
		Name:      serverInfo.ServerName,
		Interface: serverInfo.Interface,
	}

	slog.Info("Testing DHCP static lease", "server", testServer.Name, "vpc", serverInfo.VPCName, "subnet", serverInfo.SubnetName, "mac", serverMAC)

	// 2. Save the current DHCP config (may be empty/disabled)
	subnet := serverInfo.VPC.Spec.Subnets[serverInfo.SubnetName]
	savedDHCP := subnet.DHCP.DeepCopy()

	// Parse subnet CIDR to derive hardcoded test IPs
	_, subnetCIDR, err := net.ParseCIDR(subnet.Subnet)
	if err != nil {
		return false, nil, fmt.Errorf("parsing subnet CIDR %s: %w", subnet.Subnet, err)
	}
	baseIP := subnetCIDR.IP.To4()
	if baseIP == nil {
		return false, nil, fmt.Errorf("subnet %s is not IPv4", subnet.Subnet) //nolint:goerr113
	}
	baseInt := binary.BigEndian.Uint32(baseIP)

	// Hardcoded test values derived from subnet base:
	// - Dynamic range: base+10 to base+50
	// - Static IP in range: base+20
	// - Static IP outside range: base+100
	rangeStart := make(net.IP, 4)
	binary.BigEndian.PutUint32(rangeStart, baseInt+10)
	rangeEnd := make(net.IP, 4)
	binary.BigEndian.PutUint32(rangeEnd, baseInt+50)
	staticIPInRange := make(net.IP, 4)
	binary.BigEndian.PutUint32(staticIPInRange, baseInt+20)
	staticIPOutsideRange := make(net.IP, 4)
	binary.BigEndian.PutUint32(staticIPOutsideRange, baseInt+100)

	// 3. Force a hardcoded DHCP config
	subnet.DHCP = vpcapi.VPCDHCP{
		Enable: true,
		Range: &vpcapi.VPCDHCPRange{
			Start: rangeStart.String(),
			End:   rangeEnd.String(),
		},
	}

	if _, err := CreateOrUpdateVpc(ctx, testCtx.kube, serverInfo.VPC); err != nil {
		return false, nil, fmt.Errorf("updating VPC %s with test DHCP config: %w", serverInfo.VPCName, err)
	}

	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, nil, fmt.Errorf("waiting for ready after DHCP config change: %w", err)
	}

	time.Sleep(5 * time.Second)

	// 5. Setup revert to restore original DHCP config
	reverts := []RevertFunc{
		func(ctx context.Context) error {
			slog.Debug("Reverting DHCP config", "vpc", serverInfo.VPCName, "subnet", serverInfo.SubnetName)

			subnet := serverInfo.VPC.Spec.Subnets[serverInfo.SubnetName]
			subnet.DHCP = *savedDHCP

			if _, err := CreateOrUpdateVpc(ctx, testCtx.kube, serverInfo.VPC); err != nil {
				return fmt.Errorf("reverting VPC %s DHCP config: %w", serverInfo.VPCName, err)
			}

			return WaitReady(ctx, testCtx.kube, testCtx.wrOpts)
		},
	}

	// 4. Run the tests with known hardcoded IPs
	// Test 1: Static IP within the dynamic range
	if err := testCtx.testStaticIPAssignment(ctx, serverInfo.VPC, serverInfo.SubnetName, testServer, serverMAC, staticIPInRange, "static IP within dynamic range"); err != nil {
		return false, reverts, err
	}

	// Test 2: Static IP outside the dynamic range
	if err := testCtx.testStaticIPAssignment(ctx, serverInfo.VPC, serverInfo.SubnetName, testServer, serverMAC, staticIPOutsideRange, "static IP outside dynamic range"); err != nil {
		return false, reverts, err
	}

	slog.Info("DHCP static lease test completed successfully", "server", testServer.Name)

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
