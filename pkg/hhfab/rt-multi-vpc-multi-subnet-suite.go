// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	StaticExternalNH         = "172.31.255.1"
	StaticExternalIP         = "172.31.255.5"
	StaticExternalPL         = "24"
	StaticExternalDummyIface = "10.199.0.100"
)

func makeMultiVPCMultiSubnetSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Multi-Subnet Multi-VPC Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Multi-Subnets no restrictions",
			F:    testCtx.noRestrictionsTest,
		},
		{
			Name: "Multi-Subnets isolation",
			F:    testCtx.multiSubnetsIsolationTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoServers:     true,
			},
		},
		{
			Name: "Multi-Subnets with filtering",
			F:    testCtx.multiSubnetsSubnetFilteringTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				SubInterfaces: true,
				NoServers:     true,
			},
		},
		{
			Name: "StaticExternal",
			F:    testCtx.staticExternalTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				NoServers:     true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// Test VPC peering with multiple subnets and with restrictions.
// Assumes the scenario has at least 2 VPCs with 2 subnets each.
// 0. Configure peering between the VPCs
// 1. Isolate subnet-01 in vpc1, test connectivity
// 2. Override isolation with explicit permit list, test connectivity
// 3. Set restricted flag in subnet-02 in vpc2, test connectivity
// 4. Remove all restrictions and peerings
func (testCtx *VPCPeeringTestCtx) multiSubnetsIsolationTest(ctx context.Context) (bool, []RevertFunc, error) {
	var returnErr error
	var vpc1, vpc2 *vpcapi.VPC

	vpcs := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcs); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcs.Items) < 2 {
		return true, nil, errors.New("not enough VPCs found for multi-subnet isolation test") //nolint:goerr113
	}
	// check that we have at least 2 VPCs with at least 2 subnets each
	for _, vpc := range vpcs.Items {
		if len(vpc.Spec.Subnets) < 2 {
			continue
		}
		if vpc1 == nil {
			vpc1 = &vpc
		} else {
			vpc2 = &vpc

			break
		}
	}
	if vpc1 == nil || vpc2 == nil {
		return true, nil, errors.New("not enough VPCs with at least 2 subnets found") //nolint:goerr113
	}

	// peer the VPCs
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 1)
	appendVpcPeeringSpecByName(vpcPeerings, vpc1.Name, vpc2.Name, "", []string{}, []string{})
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, nil, nil, false); err != nil {
		return false, nil, fmt.Errorf("setting up VPC peerings: %w", err)
	}
	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		if err := DoSetupPeerings(ctx, testCtx.kube, nil, nil, nil, true); err != nil {
			return fmt.Errorf("removing VPC peerings: %w", err)
		}

		return nil
	})

	// modify vpc1 to have one isolated subnet
	permitList := make([]string, 0)
	isolated := false
	for subName, sub := range vpc1.Spec.Subnets {
		if !isolated {
			slog.Debug("Isolating subnet in vpc1", "vpc1", vpc1.Name, "subnet", subName)
			sub.Isolated = pointer.To(true)
			isolated = true
		}
		permitList = append(permitList, subName)
	}
	_, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc1)
	if err != nil {
		return false, reverts, fmt.Errorf("updating VPC %s: %w", vpc1.Name, err)
	}
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Removing all restrictions")
		vpc1.Spec.Permit = make([][]string, 0)
		for _, sub := range vpc1.Spec.Subnets {
			sub.Isolated = pointer.To(false)
		}
		for _, sub := range vpc2.Spec.Subnets {
			sub.Restricted = pointer.To(false)
		}
		_, err1 := CreateOrUpdateVpc(ctx, testCtx.kube, vpc1)
		_, err2 := CreateOrUpdateVpc(ctx, testCtx.kube, vpc2)
		if err1 != nil || err2 != nil {
			return errors.Join(err1, err2)
		}
		// do not wait as we are reverting the peering next anyway

		return nil
	})

	// TODO: agent generation check to ensure that the change was picked up
	// (tricky as we need to derive switch name from vpc, which involves quite a few steps)
	waitTime := 5 * time.Second
	time.Sleep(waitTime)
	tcOpts := testCtx.tcOpts
	tcOpts.WaitSwitchesReady = true
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		returnErr = fmt.Errorf("waiting for ready: %w", err)
	} else if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, tcOpts); err != nil {
		returnErr = fmt.Errorf("testing connectivity with isolated subnet: %w", err)
	}

	// override isolation with explicit permit list
	if returnErr == nil {
		vpc1.Spec.Permit = make([][]string, 1)
		vpc1.Spec.Permit[0] = make([]string, len(permitList))
		copy(vpc1.Spec.Permit[0], permitList)
		slog.Debug("Permitting subnets", "subnets", permitList)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc1)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC %s: %w", vpc1.Name, err)
		} else {
			time.Sleep(waitTime)
			if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with permit-list override: %w", err)
			}
		}
	}

	// set restricted flag in a single subnet of vpc2
	if returnErr == nil {
		for subName, sub := range vpc2.Spec.Subnets {
			slog.Debug("Restricting subnet in vpc2", "vpc2", vpc2.Name, "subnet", subName)
			sub.Restricted = pointer.To(true)

			break // only restrict one subnet
		}
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc2)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC %s: %w", vpc2.Name, err)
		} else {
			time.Sleep(waitTime)
			if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with restricted subnet: %w", err)
			}
		}
	}

	return false, reverts, returnErr
}

// Test VPC peering with multiple subnets and with subnet filtering.
// Assumes the scenario has at least 2 VPCs with at least 2 subnets each.
// It creates peering between them, but restricts the peering to only
// one subnet each. It then tests connectivity.
func (testCtx *VPCPeeringTestCtx) multiSubnetsSubnetFilteringTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcList := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcList); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcList.Items) < 2 {
		return true, nil, errors.New("not enough VPCs found for multi-subnet filtering test") //nolint:goerr113
	}
	var vpc1, vpc2 *vpcapi.VPC
	for _, vpc := range vpcList.Items {
		if len(vpc.Spec.Subnets) < 2 {
			continue
		}
		if vpc1 == nil {
			vpc1 = &vpc
		} else {
			vpc2 = &vpc

			break
		}
	}
	if vpc1 == nil || vpc2 == nil {
		return true, nil, errors.New("not enough VPCs with at least 2 subnets found") //nolint:goerr113
	}
	var sub1, sub2 string
	for subName := range vpc1.Spec.Subnets {
		sub1 = subName

		break
	}
	for subName := range vpc2.Spec.Subnets {
		sub2 = subName

		break
	}
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 1)
	appendVpcPeeringSpecByName(vpcPeerings, vpc1.Name, vpc2.Name, "", []string{sub1}, []string{sub2})

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		if err := DoSetupPeerings(ctx, testCtx.kube, nil, nil, nil, true); err != nil {
			return fmt.Errorf("removing VPC peerings: %w", err)
		}

		return nil
	})

	if err := DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts); err != nil {
		return false, reverts, err
	}

	return false, reverts, nil
}

func (testCtx *VPCPeeringTestCtx) pingStaticExternal(ctx context.Context, sourceNode string, sourceIP string, expected bool) error {
	slog.Debug("Pinging static external next hop", "sourceNode", sourceNode, "next-hop", StaticExternalNH, "expected", expected)
	ssh, err := testCtx.getSSH(ctx, sourceNode)
	if err != nil {
		return fmt.Errorf("getting ssh config for source node %s: %w", sourceNode, err)
	}
	seNhIP := netip.MustParseAddr(StaticExternalNH)
	seDummyIP := netip.MustParseAddr(StaticExternalDummyIface)
	var sIP *netip.Addr
	if sourceIP != "" {
		sIP = pointer.To(netip.MustParseAddr(sourceIP))
	}

	if err := checkPing(ctx, 3, nil, sourceNode, StaticExternalNH, ssh, seNhIP, sIP, expected); err != nil {
		return fmt.Errorf("ping to static external next hop: %w", err)
	}
	slog.Debug("Pinging static external dummy interface", "sourceNode", sourceNode, "dummy-interface", StaticExternalDummyIface, "expected", expected)
	if err := checkPing(ctx, 3, nil, sourceNode, StaticExternalDummyIface, ssh, seDummyIP, sIP, expected); err != nil {
		return fmt.Errorf("ping to static external dummy interface: %w", err)
	}

	return nil
}

/* This test replaces a server with a "static external" node, Here are the test steps:
 * 0. find an unbundled connection THAT IS NOT ATTACHED TO AN MCLAG SWITCH, take note of params (target server, switch, switch port, server port)
 * 1. find two VPCs with at least one server attached to each, i.e. vpc1 and vpc2
 * 2. delete the existing VPC attachement associated with the unbundled connection
 * 3. delete the unbundled connection
 * 4. create a new static external connection, using the port which is connected to the target server
 * 5. specify that the static external is within an existing vpc, i.e. vpc1
 * 6. ssh into target server, cleanup with hhfctl, then add the address specified in the static external, i.e. 172.31.255.1/24, to en2ps1 + set it up
 * 6a. add a default route via the nexthop specified in the static external, i.e. 172.31.255.5
 * 6b. add dummy interfaces within the subnets specified in the static external, e.g. 10.199.0.100/32
 * 7. select a server in vpc1, ssh into it and perform the following tests (should succeed):
 * 7a. ping the address specified in the static external, i.e. 172.31.255.1
 * 7b. ping the dummy interface, i.e. 10.199.0.100
 * 8. repeat tests 7a and 7b from a server in a different VPC, i.e. vpc2 (should fail)
 * 9. change the static External to not be attached to a VPC, i.e. set `withinVpc` to an empty string (NOTE: this requires delete + recreate)
 * 10. repeat tests 7a and 7b from a server in vpc1 (should fail)
 * 10a. repeat tests 7a and 7b from a switch that's not the one the static external is attached to (should succeed)
 * 11. cleanup everything and restore the original state
 */
func (testCtx *VPCPeeringTestCtx) staticExternalTest(ctx context.Context) (bool, []RevertFunc, error) {
	// find an unbundled connection not attached to an MCLAG switch (see https://github.com/githedgehog/fabricator/issues/673#issuecomment-3028423762)
	connList := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, connList, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeUnbundled}); err != nil {
		return false, nil, fmt.Errorf("listing connections: %w", err)
	}
	if len(connList.Items) == 0 {
		slog.Info("No unbundled connections found, skipping test")

		return true, nil, errNoUnbundled
	}
	swList := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, swList); err != nil {
		return false, nil, fmt.Errorf("listing switches: %w", err)
	}
	mclagSwitches := make(map[string]bool, 0)
	for _, sw := range swList.Items {
		if sw.Spec.Redundancy.Type == meta.RedundancyTypeMCLAG {
			mclagSwitches[sw.Name] = true
		}
	}
	var conn *wiringapi.Connection
	var targetServerVPC string
	for _, c := range connList.Items {
		swName := c.Spec.Unbundled.Link.Switch.DeviceName()
		if _, ok := mclagSwitches[swName]; ok {
			continue
		}
		conn = &c
		// recall the VPC attached to this connection for later
		vpcAttachList := &vpcapi.VPCAttachmentList{}
		if err := testCtx.kube.List(ctx, vpcAttachList, kclient.MatchingLabels{wiringapi.LabelConnection: conn.Name}); err != nil {
			return false, nil, fmt.Errorf("listing VPCAttachments for connection %s: %w", conn.Name, err)
		}
		if len(vpcAttachList.Items) != 1 {
			return false, nil, fmt.Errorf("expected 1 VPCAttachment for connection %s, got %d", conn.Name, len(vpcAttachList.Items)) //nolint:goerr113
		}
		targetServerVPC = vpcAttachList.Items[0].Spec.VPCName()

		break
	}
	if conn == nil {
		slog.Info("No unbundled connections found that are not attached to an MCLAG switch, skipping test")

		return true, nil, errNoUnbundled
	}

	targetServer := conn.Spec.Unbundled.Link.Server.DeviceName()
	switchName := conn.Spec.Unbundled.Link.Switch.DeviceName()
	switchPortName := conn.Spec.Unbundled.Link.Switch.PortName()
	serverPortName := conn.Spec.Unbundled.Link.Server.LocalPortName()
	slog.Debug("Found unbundled connection", "connection", conn.Name, "server", targetServer, "switch", switchName, "port", switchPortName, "VPC", targetServerVPC)
	targetServerSSH, err := testCtx.getSSH(ctx, targetServer)
	if err != nil {
		return false, nil, fmt.Errorf("getting ssh config for target server %s: %w", targetServer, err)
	}

	// find two VPCs with at least a server attached to each, we'll need them later for testing
	vpcList := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcList); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcList.Items) < 2 {
		slog.Info("Not enough VPCs found, skipping test")

		return true, nil, errNotEnoughVPCs
	}
	// inVPC is the VPC where we will add the static external
	// otherVPC is a separate VPC we will use for negative connectivity testing
	var inVPC, otherVPC *vpcapi.VPC
	var inServer, otherServer string
	// routeCheckSw keeps track of switches where we need to check for route presence later
	routeCheckSw := map[string]bool{}
	routeCheckSw[switchName] = true

	vpcAttachList := &vpcapi.VPCAttachmentList{}
	for _, vpc := range vpcList.Items {
		if inVPC != nil && otherVPC != nil {
			break
		}
		if err := testCtx.kube.List(ctx, vpcAttachList, kclient.MatchingLabels{wiringapi.LabelVPC: vpc.Name}); err != nil {
			return false, nil, fmt.Errorf("listing VPCAttachments for VPC %s: %w", vpc.Name, err)
		}
		for _, vpcAttach := range vpcAttachList.Items {
			conn := &wiringapi.Connection{}
			connName := vpcAttach.Spec.Connection
			if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: connName}, conn); err != nil {
				return false, nil, fmt.Errorf("getting connection %s for VPC Attach %s: %w", connName, vpcAttach.Name, err)
			}
			switches, servers, _, _, _ := conn.Spec.Endpoints()
			if len(servers) != 1 {
				return false, nil, fmt.Errorf("expected 1 server for connection %s, got %d", conn.Name, len(servers)) //nolint:goerr113
			}
			if servers[0] == targetServer {
				slog.Debug("Skipping target server", "vpc", vpc.Name, "server", targetServer)

				continue
			}
			if inVPC == nil {
				// if we have not found yet the VPC where we will add the static external and there's a single attachment to the target server,
				// that means we cannot use this VPC - there would be no other server within the VPC to test from
				if vpc.Name == targetServerVPC && len(vpcAttachList.Items) == 2 {
					slog.Debug("VPC has only one additional server beyond target, using it as otherVPC")
					otherVPC = &vpc
					otherServer = servers[0]

					break
				}
				inVPC = &vpc
				inServer = servers[0]
				for _, sw := range switches {
					routeCheckSw[sw] = true
				}

				break
			}
			otherVPC = &vpc
			otherServer = servers[0]

			break
		}
	}
	if inVPC == nil || otherVPC == nil || inServer == "" || otherServer == "" {
		slog.Info("Not enough VPCs with attached servers found, skipping test")

		return true, nil, errNotEnoughVPCs
	}
	slog.Debug("Found VPCs and servers", "inVPC", inVPC.Name, "inServer", inServer, "otherVPC", otherVPC.Name, "otherServer", otherServer)

	// get agent generation for the switch
	gen, genErr := getAgentGen(ctx, testCtx.kube, switchName)
	if genErr != nil {
		return false, nil, genErr
	}

	// Get the corresponding VPCAttachment
	vpcAttList := &vpcapi.VPCAttachmentList{}
	if err := testCtx.kube.List(ctx, vpcAttList, kclient.MatchingLabels{wiringapi.LabelConnection: conn.Name}); err != nil {
		return false, nil, fmt.Errorf("listing VPCAttachments: %w", err)
	}
	if len(vpcAttList.Items) != 1 {
		return false, nil, fmt.Errorf("expected 1 VPCAttachment for connection %s, got %d", conn.Name, len(vpcAttList.Items)) //nolint:goerr113
	}
	vpcAtt := vpcAttList.Items[0]
	subnetName := vpcAtt.Spec.SubnetName()
	vpcName := vpcAtt.Spec.VPCName()
	slog.Debug("Found VPCAttachment", "attachment", vpcAtt.Name, "subnet", subnetName, "vpc", vpcName)
	// Get the VPCAttachment's VPC so we can extract the VLAN (for hhnet config)
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: vpcName}, vpc); err != nil {
		return false, nil, fmt.Errorf("getting VPC %s: %w", vpcName, err)
	}
	vlan := vpc.Spec.Subnets[subnetName].VLAN
	slog.Debug("VLAN for VPCAttachment", "vlan", vlan)
	// Delete the VPCAttachment
	slog.Debug("Deleting VPCAttachment", "attachment", vpcAtt.Name)
	if err := testCtx.kube.Delete(ctx, &vpcAtt); err != nil {
		return false, nil, fmt.Errorf("deleting VPCAttachment %s: %w", vpcAtt.Name, err)
	}
	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		newVpcAtt := &vpcapi.VPCAttachment{
			ObjectMeta: kmetav1.ObjectMeta{
				Name:      vpcAtt.Name,
				Namespace: vpcAtt.Namespace,
			},
			Spec: vpcAtt.Spec,
		}
		gen, genErr := getAgentGen(ctx, testCtx.kube, switchName)
		if genErr != nil {
			return genErr
		}
		slog.Debug("Creating VPCAttachment", "attachment", newVpcAtt.Name)
		if err := testCtx.kube.Create(ctx, newVpcAtt); err != nil {
			return fmt.Errorf("creating VPCAttachment %s: %w", newVpcAtt.Name, err)
		}
		if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
			return fmt.Errorf("waiting for agent generation: %w", err)
		}
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}
		slog.Debug("Invoking hhnet cleanup on server", "server", targetServer)
		if _, stderr, err := targetServerSSH.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
			return fmt.Errorf("cleaning up %s via hhnet: %w: %s", targetServer, err, stderr)
		}
		slog.Debug("Configuring VLAN on server", "server", targetServer, "vlan", vlan, "port", serverPortName)
		if _, stderr, err := targetServerSSH.Run(ctx, fmt.Sprintf("/opt/bin/hhnet vlan %d %s", vlan, serverPortName)); err != nil {
			return fmt.Errorf("configuring VLAN on %s: %w: %s", targetServer, err, stderr)
		}
		// in case of L3 VPC mode, we need to give it time to switch to the longer lease time and switches to learn the routes
		if testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3Flat {
			time.Sleep(10 * time.Second)
		}

		slog.Debug("All state restored")

		return nil
	})

	slog.Debug("Deleting connection", "connection", conn.Name)
	if err := testCtx.kube.Delete(ctx, conn); err != nil {
		return false, reverts, fmt.Errorf("deleting connection %s: %w", conn.Name, err)
	}

	reverts = append(reverts, func(ctx context.Context) error {
		gen, genErr := getAgentGen(ctx, testCtx.kube, switchName)
		if genErr != nil {
			return genErr
		}
		newConn := &wiringapi.Connection{
			ObjectMeta: kmetav1.ObjectMeta{
				Name:      conn.Name,
				Namespace: conn.Namespace,
			},
			Spec: conn.Spec,
		}
		slog.Debug("Creating connection", "connection", newConn.Name)
		if err := testCtx.kube.Create(ctx, newConn); err != nil {
			return fmt.Errorf("creating connection %s: %w", newConn.Name, err)
		}
		if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
			return fmt.Errorf("waiting for agent generation: %w", err)
		}
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}

		return nil
	})

	if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
		return false, reverts, err
	}
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready: %w", err)
	}
	gen, genErr = getAgentGen(ctx, testCtx.kube, switchName)
	if genErr != nil {
		return false, reverts, genErr
	}

	// Create new connection with static external
	staticExtConn := &wiringapi.Connection{}
	staticExtConn.Name = fmt.Sprintf("release-test--static-external--%s", switchName)
	staticExtConn.Namespace = kmetav1.NamespaceDefault
	staticExtConn.Spec.StaticExternal = &wiringapi.ConnStaticExternal{
		WithinVPC: inVPC.Name,
		Link: wiringapi.ConnStaticExternalLink{
			Switch: wiringapi.ConnStaticExternalLinkSwitch{
				BasePortName: wiringapi.NewBasePortName(switchPortName),
				IP:           fmt.Sprintf("%s/%s", StaticExternalIP, StaticExternalPL),
				Subnets:      []string{fmt.Sprintf("%s/32", StaticExternalDummyIface)},
				NextHop:      StaticExternalNH,
			},
		},
	}
	slog.Debug("Creating connection", "connection", staticExtConn.Name)
	if err := testCtx.kube.Create(ctx, staticExtConn); err != nil {
		return false, reverts, fmt.Errorf("creating connection %s: %w", staticExtConn.Name, err)
	}
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Deleting connection", "connection", staticExtConn.Name)
		if err := testCtx.kube.Delete(ctx, staticExtConn); err != nil {
			return fmt.Errorf("deleting connection %s: %w", staticExtConn.Name, err)
		}

		return nil
	})

	if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
		return false, reverts, err
	}
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready: %w", err)
	}

	// Add address and default route to en2ps1 on the server
	slog.Debug("Adding address and default route to en2ps1 on the server", "server", targetServer)
	if _, stderr, err := targetServerSSH.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
		return false, reverts, fmt.Errorf("cleaning up server via hhnet: %w: %s", err, stderr)
	}
	if _, stderr, err := targetServerSSH.Run(ctx, fmt.Sprintf("sudo ip addr add %s/%s dev enp2s1", StaticExternalNH, StaticExternalPL)); err != nil {
		return false, reverts, fmt.Errorf("adding address to server: %w: %s", err, stderr)
	}
	if _, stderr, err := targetServerSSH.Run(ctx, "sudo ip link set dev enp2s1 up"); err != nil {
		return false, reverts, fmt.Errorf("setting up server interface: %w: %s", err, stderr)
	}
	if _, stderr, err := targetServerSSH.Run(ctx, fmt.Sprintf("sudo ip route add default via %s", StaticExternalIP)); err != nil {
		return false, reverts, fmt.Errorf("adding default route to server: %w: %s", err, stderr)
	}
	slog.Debug("Adding dummy inteface to the server", "server", targetServer, "address", fmt.Sprintf("%s/32", StaticExternalDummyIface))
	if _, stderr, err := targetServerSSH.Run(ctx, "sudo ip link add dummy0 type dummy"); err != nil {
		return false, reverts, fmt.Errorf("adding dummy interface to server: %w: %s", err, stderr)
	}
	if _, stderr, err := targetServerSSH.Run(ctx, fmt.Sprintf("sudo ip addr add %s/32 dev dummy0", StaticExternalDummyIface)); err != nil {
		return false, reverts, fmt.Errorf("adding address to dummy interface on server: %w: %s", err, stderr)
	}
	reverts = append(reverts, func(_ context.Context) error {
		slog.Debug("Removing address and default route from en2ps1 on the server", "server", targetServer)
		if _, stderr, err := targetServerSSH.Run(ctx, fmt.Sprintf("sudo ip addr del %s/%s dev enp2s1", StaticExternalNH, StaticExternalPL)); err != nil {
			return fmt.Errorf("removing address from %s: %w: %s", targetServer, err, stderr)
		}
		if _, stderr, err := targetServerSSH.Run(ctx, "sudo ip link del dev dummy0"); err != nil {
			return fmt.Errorf("removing dummy interface from %s: %w: %s", targetServer, err, stderr)
		}
		if _, stderr, err := targetServerSSH.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
			return fmt.Errorf("cleaning up %s via hhnet: %w: %s", targetServer, err, stderr)
		}

		return nil
	})
	// look for routes in the switch(es) before pinging, see https://github.com/githedgehog/fabricator/issues/932#issuecomment-3322976488
	if err := testCtx.waitForRoutesInSwitches(ctx, routeCheckSw, []string{StaticExternalNH, StaticExternalDummyIface}, "VrfV"+inVPC.Name, 3*time.Minute); err != nil {
		return false, reverts, fmt.Errorf("waiting for routes in switch %s vrf VrfV%s: %w", switchName, inVPC.Name, err)
	}

	slog.Debug("Pinging from the switch attached to the static external to trigger ARP resolution", "switch", switchName, "vrf", "VrfV"+inVPC.Name, "source-ip", StaticExternalIP, "target", StaticExternalNH)
	wuPingCmd := fmt.Sprintf("sonic-cli -c \"ping vrf VrfV%s -I %s %s -c 3 -W 1\"", inVPC.Name, StaticExternalIP, StaticExternalNH)
	switchSSH, err := testCtx.getSSH(ctx, switchName)
	if err != nil {
		return false, reverts, fmt.Errorf("getting ssh config for switch %s: %w", switchName, err)
	}
	stdout, stderr, pingErr := switchSSH.Run(ctx, wuPingCmd)
	if pingErr != nil {
		slog.Warn("Warm-up ping from switch failed, continuing anyway", "error", pingErr, "stderr", stderr)
	} else {
		slog.Debug("Ping output from switch", "output", stdout)
	}

	// Ping the addresses from server1 which is in the static external VPC, expect success
	if err := testCtx.pingStaticExternal(ctx, inServer, "", true); err != nil {
		return false, reverts, fmt.Errorf("pinging static external from %s in the SE VPC: %w", inServer, err)
	}
	// Ping the addresses from server2 which is in a different VPC, expect failure
	if err := testCtx.pingStaticExternal(ctx, otherServer, "", false); err != nil {
		return false, reverts, fmt.Errorf("pinging static external from %s in a different VPC: %w", otherServer, err)
	}

	slog.Debug("Deleting static external")
	// NOTE: just changing the WithinVPC field to an empty string causes this error in the agent:
	// "failed to run agent: failed to process agent config from k8s: failed to process agent config loaded from k8s: failed to apply actions: GNMI set request failed: gnmi set request failed: rpc error: code = InvalidArgument desc = L3 Configuration exists for Interface: Ethernet0"
	// so we need to remove the whole StaticExternal config and then update it again
	if err := testCtx.kube.Delete(ctx, staticExtConn); err != nil {
		return false, reverts, fmt.Errorf("deleting static external connection %s: %w", staticExtConn.Name, err)
	}
	time.Sleep(5 * time.Second)
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Now update the static external connection to not be within a VPC
	staticExtConn = &wiringapi.Connection{}
	staticExtConn.Name = fmt.Sprintf("release-test--static-external--%s", switchName)
	staticExtConn.Namespace = kmetav1.NamespaceDefault
	staticExtConn.Spec.StaticExternal = &wiringapi.ConnStaticExternal{
		WithinVPC: "",
		Link: wiringapi.ConnStaticExternalLink{
			Switch: wiringapi.ConnStaticExternalLinkSwitch{
				BasePortName: wiringapi.NewBasePortName(switchPortName),
				IP:           fmt.Sprintf("%s/%s", StaticExternalIP, StaticExternalPL),
				Subnets:      []string{fmt.Sprintf("%s/32", StaticExternalDummyIface)},
				NextHop:      StaticExternalNH,
			},
		},
	}
	slog.Debug("Re-creating the StaticExternal without the VPC constraint", "connection", staticExtConn.Name)
	if err := testCtx.kube.Create(ctx, staticExtConn); err != nil {
		return false, reverts, fmt.Errorf("creating connection %s: %w", staticExtConn.Name, err)
	}
	time.Sleep(5 * time.Second)
	if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
		return false, reverts, fmt.Errorf("waiting for switches to be ready: %w", err)
	}
	// look for routes in the switch(es) before pinging, see https://github.com/githedgehog/fabricator/issues/932#issuecomment-3322976488
	if err := testCtx.waitForRoutesInSwitches(ctx, routeCheckSw, []string{StaticExternalNH, StaticExternalDummyIface}, "default", 3*time.Minute); err != nil {
		return false, reverts, fmt.Errorf("waiting for routes in switch %s vrf default: %w", switchName, err)
	}

	// Ping the addresses from server1, this should now fail
	if err := testCtx.pingStaticExternal(ctx, inServer, "", false); err != nil {
		return false, reverts, fmt.Errorf("pinging static external from %s after removing VPC: %w", inServer, err)
	}
	// Ping the addresses from a leaf switch that's not the one the static external is attached to, this should succeed
	success := false
	for _, sw := range swList.Items {
		if sw.Name == switchName || sw.Spec.Role.IsSpine() {
			continue
		}
		// avoid pinging from MCLAG switches, as I'm seeing failures (probably due to asymmetric routing, since they share same VTEP IP)
		if sw.Spec.Redundancy.Type == meta.RedundancyTypeMCLAG {
			continue
		}
		if sw.Spec.VTEPIP == "" {
			slog.Warn("Leaf switch with no VTEP IP, skipping it", "switch", sw.Name)

			continue
		}
		// skip the switch if it is unused, i.e. left in a mesh topology from a spine-leaf one
		// FIXME: hack based on description, we should have a proper way to identify unused switches
		if strings.Contains(sw.Spec.Description, "unused") {
			slog.Debug("Skipping unused switch", "switch", sw.Name)

			continue
		}
		sourceIP := strings.SplitN(sw.Spec.VTEPIP, "/", 2)[0]
		if err := testCtx.pingStaticExternal(ctx, sw.Name, sourceIP, true); err != nil {
			return false, reverts, fmt.Errorf("pinging static external from %s: %w", sw.Name, err)
		}
		success = true

		break
	}
	if !success {
		return false, reverts, fmt.Errorf("could not find a leaf switch to ping from after removing VPC constraint") //nolint:goerr113
	}

	slog.Debug("All good, cleaning up")

	return false, reverts, nil
}
