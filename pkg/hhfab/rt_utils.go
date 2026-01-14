// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var AllZeroPrefix = []string{"0.0.0.0/0"}

// add a single VPC peering spec to an existing map, which will be the input for DoSetupPeerings
func appendVpcPeeringSpecByName(vpcPeerings map[string]*vpcapi.VPCPeeringSpec, vpc1, vpc2, remote string, vpc1Subnets, vpc2Subnets []string) {
	entryName := fmt.Sprintf("%s--%s", vpc1, vpc2)
	vpc1SP := vpcapi.VPCPeer{}
	vpc1SP.Subnets = vpc1Subnets
	vpc2SP := vpcapi.VPCPeer{}
	vpc2SP.Subnets = vpc2Subnets
	vpcPeerings[entryName] = &vpcapi.VPCPeeringSpec{
		Remote: remote,
		Permit: []map[string]vpcapi.VPCPeer{
			{
				vpc1: vpc1SP,
				vpc2: vpc2SP,
			},
		},
	}
}

func appendVpcPeeringSpec(vpcPeerings map[string]*vpcapi.VPCPeeringSpec, index1, index2 int, remote string, vpc1Subnets, vpc2Subnets []string) {
	vpc1 := fmt.Sprintf("vpc-%02d", index1)
	vpc2 := fmt.Sprintf("vpc-%02d", index2)
	appendVpcPeeringSpecByName(vpcPeerings, vpc1, vpc2, remote, vpc1Subnets, vpc2Subnets)
}

// add a single external peering spec to an existing map, which will be the input for DoSetupPeerings
func appendExtPeeringSpecByName(extPeerings map[string]*vpcapi.ExternalPeeringSpec, vpc, ext string, subnets []string, prefixes []string) {
	entryName := fmt.Sprintf("%s--%s", vpc, ext)
	prefixesSpec := make([]vpcapi.ExternalPeeringSpecPrefix, len(prefixes))
	for i, prefix := range prefixes {
		prefixesSpec[i] = vpcapi.ExternalPeeringSpecPrefix{
			Prefix: prefix,
		}
	}
	extPeerings[entryName] = &vpcapi.ExternalPeeringSpec{
		Permit: vpcapi.ExternalPeeringSpecPermit{
			VPC: vpcapi.ExternalPeeringSpecVPC{
				Name:    vpc,
				Subnets: subnets,
			},
			External: vpcapi.ExternalPeeringSpecExternal{
				Name:     ext,
				Prefixes: prefixesSpec,
			},
		},
	}
}

func appendExtPeeringSpec(extPeerings map[string]*vpcapi.ExternalPeeringSpec, vpcIndex int, ext string, subnets []string, prefixes []string) {
	vpc := fmt.Sprintf("vpc-%02d", vpcIndex)
	appendExtPeeringSpecByName(extPeerings, vpc, ext, subnets, prefixes)
}

// populate the vpcPeerings map with all possible VPC peering combinations
func populateFullMeshVpcPeerings(ctx context.Context, kube kclient.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec) error {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return fmt.Errorf("listing VPCs: %w", err)
	}
	for i := 0; i < len(vpcs.Items); i++ {
		for j := i + 1; j < len(vpcs.Items); j++ {
			appendVpcPeeringSpec(vpcPeerings, i+1, j+1, "", []string{}, []string{})
		}
	}

	return nil
}

// populate the vpcPeerings map with a "full loop" of VPC peering connections, i.e.
// each VPC is connected to the next one in the list, and the last one is connected to the first
func populateFullLoopVpcPeerings(ctx context.Context, kube kclient.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec) error {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return fmt.Errorf("listing VPCs: %w", err)
	}
	for i := 0; i < len(vpcs.Items); i++ {
		appendVpcPeeringSpec(vpcPeerings, i+1, (i+1)%len(vpcs.Items)+1, "", []string{}, []string{})
	}

	return nil
}

// populate the externalPeerings map with all possible external VPC peering combinations
func populateAllExternalVpcPeerings(ctx context.Context, kube kclient.Client, extPeerings map[string]*vpcapi.ExternalPeeringSpec) error {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return fmt.Errorf("listing VPCs: %w", err)
	}
	exts := &vpcapi.ExternalList{}
	if err := kube.List(ctx, exts); err != nil {
		return fmt.Errorf("listing VPCs: %w", err)
	}
	for i := 0; i < len(vpcs.Items); i++ {
		for j := 0; j < len(exts.Items); j++ {
			appendExtPeeringSpec(extPeerings, i+1, exts.Items[j].Name, []string{"subnet-01"}, AllZeroPrefix)
		}
	}

	return nil
}

// Get last applied generation of the hedgehog agent on switch swName
func getAgentGen(ctx context.Context, kube kclient.Client, swName string) (int64, error) {
	ag := &agentapi.Agent{}
	err := kube.Get(ctx, kclient.ObjectKey{Name: swName, Namespace: kmetav1.NamespaceDefault}, ag)
	if err != nil {
		return -1, fmt.Errorf("getting agent %s: %w", swName, err)
	}

	return ag.Status.LastAppliedGen, nil
}

// Wait until the hedgehog agent on switch swName has moved beyond the given generation
func waitAgentGen(ctx context.Context, kube kclient.Client, swName string, lastGen int64) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ag := &agentapi.Agent{}

	for {
		err := kube.Get(ctx, kclient.ObjectKey{Name: swName, Namespace: kmetav1.NamespaceDefault}, ag)
		if err != nil {
			return fmt.Errorf("getting agent %s: %w", swName, err)
		}
		if ag.Status.LastAppliedGen > lastGen {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for agent %s to move past generation %d: %w", swName, lastGen, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}

	return nil
}

// Run a configure command on switch swName via the sonic-cli.
// Each of the strings in cmds is going to be wrapped in double quotes and
// passed as a separate argument to sonic-cli.
func execConfigCmd(ctx context.Context, ssh *sshutil.Config, swName string, cmds ...string) error {
	cmdList := []string{"sonic-cli", "-c", "configure"}
	for _, c := range cmds {
		// add escaped double quotes around the command
		cmdList = append(cmdList, "-c", fmt.Sprintf("\"%s\"", c))
	}
	if stdout, stderr, err := ssh.Run(ctx, strings.Join(cmdList, " ")); err != nil {
		slog.Error("Configuring switch", "switch", swName, "error", err, "stderr", stderr)
		slog.Debug("Stdout of errored command", "output", stdout)

		return fmt.Errorf("configuring switch %s: %w", swName, err)
	}

	return nil
}

func (testCtx *VPCPeeringTestCtx) getSSH(ctx context.Context, nodeName string) (*sshutil.Config, error) {
	return testCtx.vlabCfg.SSH(ctx, testCtx.vlab, nodeName)
}

// Enable or disable the hedgehog agent on switch swName.
func changeAgentStatus(ctx context.Context, ssh *sshutil.Config, swName string, up bool) error {
	cmd := fmt.Sprintf("sudo systemctl %s hedgehog-agent.service", map[bool]string{true: "start", false: "stop"}[up])
	_, stderr, err := ssh.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("changing agent status on switch %s: %w: %s", swName, err, stderr)
	}

	return nil
}

// Change the admin status of a switch port via the sonic-cli, i.e. by running (no) shutdown on the port.
func changeSwitchPortStatus(ctx context.Context, ssh *sshutil.Config, deviceName, nosPortName string, up bool) error {
	slog.Debug("Changing switch port status", "device", deviceName, "port", nosPortName, "up", up)
	if up {
		if err := execConfigCmd(ctx, ssh, deviceName, fmt.Sprintf("interface %s", nosPortName), "no shutdown"); err != nil {
			return err
		}
	} else {
		if err := execConfigCmd(ctx, ssh, deviceName, fmt.Sprintf("interface %s", nosPortName), "shutdown"); err != nil {
			return err
		}
	}

	return nil
}

// check if remote peering between two VPCs (as defined by their indexes) is legal, return an error otherwise
func checkRemotePeering(ctx context.Context, kube kclient.Reader, remote string, firstVPCIndex, secondVPCIndex int) error {
	vpc1Name := fmt.Sprintf("vpc-%02d", firstVPCIndex)
	vpc1 := &vpcapi.VPC{}
	vpc2Name := fmt.Sprintf("vpc-%02d", secondVPCIndex)
	vpc2 := &vpcapi.VPC{}
	swGroup := &wiringapi.SwitchGroup{}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: remote}, swGroup); err != nil {
		return fmt.Errorf("remote switch group %s not found: %w", remote, err)
	}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: vpc1Name}, vpc1); err != nil {
		return fmt.Errorf("error getting first VPC: %w", err)
	}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: vpc2Name}, vpc2); err != nil {
		return fmt.Errorf("error getting second VPC: %w", err)
	}
	for _, vpc := range []string{vpc1Name, vpc2Name} {
		vpcAttachList := &vpcapi.VPCAttachmentList{}
		if err := kube.List(ctx, vpcAttachList, kclient.MatchingLabels{wiringapi.LabelVPC: vpc}); err != nil {
			return fmt.Errorf("error listing VPCAttachments for VPC %s: %w", vpc, err)
		}
		for _, vpcAttach := range vpcAttachList.Items {
			conn := &wiringapi.Connection{}
			connName := vpcAttach.Spec.Connection
			if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: connName}, conn); err != nil {
				return fmt.Errorf("error getting connection %s for VPC Attach %s: %w", connName, vpcAttach.Name, err)
			}
			switches, _, _, _, _ := conn.Spec.Endpoints()
			for _, swName := range switches {
				sw := &wiringapi.Switch{}
				if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: swName}, sw); err != nil {
					return fmt.Errorf("error getting switch %s for VPC Attach %s: %w", swName, vpcAttach.Name, err)
				}
				if slices.Contains(sw.Spec.Groups, remote) {
					return fmt.Errorf("VPC %s is attached to switch %s which is in remote group %s", vpc, swName, remote) //nolint:goerr113
				}
			}
		}
	}

	return nil
}

// disable agent, shutdown port on switch, test connectivity, enable agent, set port up
func shutDownLinkAndTest(ctx context.Context, testCtx *VPCPeeringTestCtx, link wiringapi.ServerToSwitchLink) (returnErr error) {
	switchPort := link.Switch
	deviceName := switchPort.DeviceName()
	// get switch profile to find the port name in sonic-cli
	sw := &wiringapi.Switch{}
	switchName := switchPort.DeviceName()
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: switchName}, sw); err != nil {
		return fmt.Errorf("getting switch %s: %w", switchName, err)
	}
	profile := &wiringapi.SwitchProfile{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: sw.Spec.Profile}, profile); err != nil {
		return fmt.Errorf("getting switch profile %s: %w", sw.Spec.Profile, err)
	}
	portMap, err := profile.Spec.GetAPI2NOSPortsFor(&sw.Spec)
	if err != nil {
		return fmt.Errorf("getting API2NOS ports for switch %s: %w", switchName, err)
	}
	nosPortName, ok := portMap[switchPort.LocalPortName()]
	if !ok {
		return fmt.Errorf("port %s not found in switch profile %s for switch %s", switchPort.LocalPortName(), profile.Name, deviceName) //nolint:goerr113
	}
	// disable agent
	swSSH, sshErr := testCtx.getSSH(ctx, deviceName)
	if sshErr != nil {
		return fmt.Errorf("getting ssh config for switch %s: %w", deviceName, sshErr)
	}
	if err := changeAgentStatus(ctx, swSSH, deviceName, false); err != nil {
		return fmt.Errorf("disabling HH agent: %w", err)
	}
	defer func() {
		maxRetries := 5
		sleepTime := time.Second * 5
		for i := range maxRetries {
			if err := changeAgentStatus(ctx, swSSH, deviceName, true); err != nil {
				slog.Error("Enabling HH agent", "error", err)
				if i < maxRetries-1 {
					slog.Warn("Retrying", "delay", sleepTime)
					time.Sleep(sleepTime)
				}
			} else {
				return
			}
		}
		if returnErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("could not enable HH agent on switch %s after %d attempts", deviceName, maxRetries)) //nolint:goerr113
		} else {
			returnErr = fmt.Errorf("could not enable HH agent on switch %s after %d attempts", deviceName, maxRetries) //nolint:goerr113
		}
	}()

	// set port down
	if err := changeSwitchPortStatus(ctx, swSSH, deviceName, nosPortName, false); err != nil {
		return fmt.Errorf("setting switch port down: %w", err)
	}
	defer func() {
		if err := changeSwitchPortStatus(ctx, swSSH, deviceName, nosPortName, true); err != nil {
			portErr := fmt.Errorf("setting port up on %s: %w", deviceName, err)
			if returnErr != nil {
				returnErr = errors.Join(returnErr, portErr)
			} else {
				returnErr = portErr
			}
		}
	}()

	// wait a little for changes to settle
	slog.Debug("Waiting 5 seconds")
	time.Sleep(5 * time.Second)

	return DoVLABTestConnectivity(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, testCtx.tcOpts)
}

// check that a route is present in a switch (by checking in the sonic-cli)
func checkRouteInSwitch(ctx context.Context, ssh *sshutil.Config, switchName, route, vrfName string) (bool, error) {
	cmd := fmt.Sprintf("show ip route vrf %s %s", vrfName, route)
	stdout, stderr, err := ssh.Run(ctx, cmd)
	if err != nil {
		return false, fmt.Errorf("executing '%s' on switch %s: %w: %s", cmd, switchName, err, stderr)
	}

	return stdout != "", nil
}

// wait until all switches in a set have a bunch of routes installed, or error out after a configurable timeout
func (testCtx *VPCPeeringTestCtx) waitForRoutesInSwitches(ctx context.Context, switches map[string]bool, routes []string, vrfName string, timeout time.Duration) error {
	slog.Debug("Checking for routes in switches", "switches", switches, "routes", routes, "vrf", vrfName, "timeout", timeout)
	toCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sshs := make(map[string]*sshutil.Config, len(switches))
	for sw := range switches {
		ssh, err := testCtx.getSSH(ctx, sw)
		if err != nil {
			return fmt.Errorf("getting ssh config for switch %s: %w", sw, err)
		}
		sshs[sw] = ssh
	}

	for {
		allFound := true
		for sw := range switches {
			for _, route := range routes {
				if found, err := checkRouteInSwitch(toCtx, sshs[sw], sw, route, vrfName); err != nil {
					return fmt.Errorf("checking for route %s in switch %s vrf %s: %w", route, sw, vrfName, err)
				} else if !found {
					slog.Debug("Route not found yet", "switch", sw, "route", route, "vrf", vrfName)
					allFound = false

					break
				}
			}
		}
		if allFound {
			slog.Debug("All routes found in all switches")

			return nil
		}
		select {
		case <-toCtx.Done():
			return fmt.Errorf("timeout waiting for routes %v in switches %v", routes, switches) //nolint:goerr113
		case <-time.After(5 * time.Second):
		}
	}
}

// check that the DHCP lease is within the expected range.
func checkDHCPLease(leaseInfo *DHCPLeaseInfo, expectedLease int, tolerance int) error {
	if leaseInfo == nil {
		return fmt.Errorf("DHCP lease info is nil") //nolint:goerr113
	}
	if !leaseInfo.HasLease {
		return fmt.Errorf("no DHCP lease found") //nolint:goerr113
	}
	if leaseInfo.ValidLifetime > expectedLease {
		return fmt.Errorf("DHCP lease valid lifetime %d is greater than expected %d", leaseInfo.ValidLifetime, expectedLease) //nolint:goerr113
	}
	if leaseInfo.ValidLifetime < expectedLease-tolerance {
		return fmt.Errorf("DHCP lease valid lifetime %d is less than expected %d (tolerance %d)", leaseInfo.ValidLifetime, expectedLease, tolerance) //nolint:goerr113
	}
	slog.Debug("DHCP lease check passed", "lease", leaseInfo.ValidLifetime, "expected", expectedLease, "tolerance", tolerance)

	return nil
}

func checkDHCPAdvRoutes(grepString, expectedPrefix, expectedGw string, disableDefaultRoute bool, l3mode bool, subnet string) error {
	// check default route
	defaultRoutePresent := strings.Contains(grepString, "default via")
	if disableDefaultRoute && defaultRoutePresent {
		return fmt.Errorf("DHCP advertised routes contain default route: %s", grepString) //nolint:goerr113
	} else if !disableDefaultRoute && !defaultRoutePresent {
		return fmt.Errorf("DHCP advertised routes do not contain default route, expected it to be present: %s", grepString) //nolint:goerr113
	}
	slog.Debug("DHCP default route check passed", "present", defaultRoutePresent, "disabled", disableDefaultRoute)
	// in l3mode, if default route is disabled, we expect the subnet to be present
	if l3mode && disableDefaultRoute {
		if !strings.Contains(grepString, fmt.Sprintf("%s via", subnet)) {
			return fmt.Errorf("DHCP advertised routes do not contain expected subnet %s in l3mode: %s", subnet, grepString) //nolint:goerr113
		}
		slog.Debug("DHCP advertised subnet check passed in l3mode", "subnet", subnet)
	}

	// check that the expected advertised route is present
	if !strings.Contains(grepString, fmt.Sprintf("%s via %s", expectedPrefix, expectedGw)) {
		return fmt.Errorf("DHCP advertised routes do not contain expected route %s via %s: %s", expectedPrefix, expectedGw, grepString) //nolint:goerr113
	}
	slog.Debug("DHCP advertised route check passed", "route", expectedPrefix, "gateway", expectedGw)
	slog.Debug("All DHCP advertised routes checks passed")

	return nil
}

// Enable or disable RoCE on a particular switch
func setRoCE(ctx context.Context, kube kclient.Client, swName string, roce bool) error {
	sw := &wiringapi.Switch{}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: swName}, sw); err != nil {
		return fmt.Errorf("getting switch %s: %w", swName, err)
	}
	if sw.Spec.RoCE == roce {
		slog.Debug("RoCE already in the desired state", "switch", swName, "desiredState", roce)

		return nil
	}
	slog.Debug("Changing RoCE state on switch", "switch", swName, "desiredState", roce)
	currGen, getGenErr := getAgentGen(ctx, kube, swName)
	if getGenErr != nil {
		return getGenErr
	}
	sw.Spec.RoCE = roce
	if err := kube.Update(ctx, sw); err != nil {
		return fmt.Errorf("updating switch %s to set RoCE state: %w", swName, err)
	}
	slog.Debug("Waiting for switch to reboot after changing desired RoCE state", "switch", swName, "desiredState", roce)
	time.Sleep(6 * time.Minute) // wait for the switch to reboot and apply the changes
	if err := waitAgentGen(ctx, kube, swName, currGen); err != nil {
		return fmt.Errorf("waiting for agent generation after changing desired RoCE state: %w", err)
	}
	slog.Debug("Switch rebooted and RoCE state changed", "switch", swName, "desiredState", roce)

	return nil
}

// single-switch, dumbed down (no update as we don't need it here) version of WaitSwitchesReady
func waitSwitchReady(ctx context.Context, kube kclient.Client, swName string, appliedFor time.Duration, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	for {
		ag := &agentapi.Agent{}
		ready := false
		err := kube.Get(ctx, kclient.ObjectKey{Name: swName, Namespace: "default"}, ag)
		if err != nil {
			return fmt.Errorf("getting agent %q: %w", swName, err)
		}
		ready = ag.Status.LastAppliedGen == ag.Generation
		ready = ready && time.Since(ag.Status.LastHeartbeat.Time) < 1*time.Minute
		if appliedFor > 0 {
			ready = ready && time.Since(ag.Status.LastAppliedTime.Time) >= appliedFor
		}

		if ready {
			slog.Debug("Switch is ready", "switch", swName, "appliedFor", appliedFor)

			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for switch %s to be ready: %w", swName, ctx.Err())
		case <-time.After(15 * time.Second):
		}
	}
}

// set port breakout mode on a portof a switch
func setPortBreakout(ctx context.Context, kube kclient.Client, swName string, port string, targetMode string, waitCompleted bool) error {
	slog.Debug("Setting breakout mode", "switch", swName, "port", port, "mode", targetMode)
	sw := &wiringapi.Switch{}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: swName}, sw); err != nil {
		return fmt.Errorf("getting switch %s: %w", swName, err)
	}
	if sw.Spec.PortBreakouts == nil {
		sw.Spec.PortBreakouts = make(map[string]string)
	}
	if currMode, ok := sw.Spec.PortBreakouts[port]; ok && currMode == targetMode {
		slog.Debug("Port already in target breakout mode, skipping", "switch", swName, "port", port, "mode", targetMode)

		return nil
	}
	currGen, genErr := getAgentGen(ctx, kube, swName)
	if genErr != nil {
		return fmt.Errorf("getting agent %s generation: %w", swName, genErr)
	}

	sw.Spec.PortBreakouts[port] = targetMode
	if err := kube.Update(ctx, sw); err != nil {
		return fmt.Errorf("updating switch %s to set breakout mode %s for port %s: %w", swName, targetMode, port, err)
	}
	slog.Debug("Waiting for switch to apply breakout mode", "switch", swName, "port", port, "mode", targetMode)
	time.Sleep(5 * time.Second)
	if err := waitAgentGen(ctx, kube, swName, currGen); err != nil {
		return fmt.Errorf("waiting for switch %s to apply breakout mode %s for port %s: %w", swName, targetMode, port, err)
	}
	slog.Debug("Waiting for this switch to be ready after breakout mode change", "switch", swName, "port", port, "mode", targetMode)
	// note: cannot use WaitSwitchesReady here because it waits for all switches and we are modifying them in parallel
	if err := waitSwitchReady(ctx, kube, swName, 1*time.Minute, 5*time.Minute); err != nil {
		return fmt.Errorf("waiting for switch %s to be ready after breakout mode change: %w", swName, err)
	}

	if waitCompleted {
		maxRetries := 10
		agent := &agentapi.Agent{}
		for i := range maxRetries {
			if err := kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: swName}, agent); err != nil {
				return fmt.Errorf("getting agent %s after breakout mode change: %w", swName, err)
			}
			bkStatus, ok := agent.Status.State.Breakouts[port]
			if !ok {
				return fmt.Errorf("breakout mode %s for port %s on switch %s not found in agent status after applying breakout mode", targetMode, port, swName) //nolint:goerr113
			}
			if bkStatus.Mode != targetMode {
				return fmt.Errorf("breakout mode %s for port %s on switch %s not applied, expected %s but got %s", targetMode, port, swName, targetMode, bkStatus.Mode) //nolint:goerr113
			}
			if bkStatus.Status == "Completed" {
				slog.Debug("Breakout mode applied successfully", "switch", swName, "port", port, "mode", targetMode)

				return nil
			}
			if i < maxRetries-1 {
				slog.Debug("Breakout mode not yet completed, retrying", "switch", swName, "port", port, "mode", targetMode, "retry", i+1)
				time.Sleep(10 * time.Second)
			}
		}

		return fmt.Errorf("breakout mode %s for port %s on switch %s not completed after %d retries", targetMode, port, swName, maxRetries) //nolint:goerr113
	}

	return nil
}

// add a single gateway peering spec to an existing map, which will be the input for DoSetupPeerings
// If vpc1Subnets is empty, all subnets from the respective VPC will be used
func appendGwExtPeeringSpec(gwPeerings map[string]*gwapi.PeeringSpec, vpc1 *vpcapi.VPC, vpc1Subnets []string, external string) {
	entryName := fmt.Sprintf("%s--%s", vpc1.Name, external)

	// If no specific subnets provided, use all subnets from vpc1
	if len(vpc1Subnets) == 0 {
		vpc1Subnets = make([]string, 0, len(vpc1.Spec.Subnets))
		for _, subnet := range vpc1.Spec.Subnets {
			vpc1Subnets = append(vpc1Subnets, subnet.Subnet)
		}
	}

	vpc1Expose := gwapi.PeeringEntryExpose{}
	for _, subnet := range vpc1Subnets {
		vpc1Expose.IPs = append(vpc1Expose.IPs, gwapi.PeeringEntryIP{CIDR: subnet})
	}
	extExpose := gwapi.PeeringEntryExpose{
		DefaultDestination: true,
	}

	gwPeerings[entryName] = &gwapi.PeeringSpec{
		Peering: map[string]*gwapi.PeeringEntry{
			vpc1.Name: {
				Expose: []gwapi.PeeringEntryExpose{vpc1Expose},
			},
			"ext." + external: {
				Expose: []gwapi.PeeringEntryExpose{extExpose},
			},
		},
	}
}

// GwPeeringOptions contains optional parameters for gateway peering configuration
type GwPeeringOptions struct {
	// VPC1Subnets specifies which subnets from VPC1 to expose (if empty, all subnets are used)
	VPC1Subnets []string
	// VPC2Subnets specifies which subnets from VPC2 to expose (if empty, all subnets are used)
	VPC2Subnets []string
	// VPC1NATCIDR specifies the NAT CIDR ranges for VPC1
	VPC1NATCIDR []string
	// VPC2NATCIDR specifies the NAT CIDR ranges for VPC2
	VPC2NATCIDR []string
	// StatefulNAT enables stateful NAT instead of stateless (default: false = stateless)
	StatefulNAT bool
}

// appendGwPeeringSpec adds a single gateway peering spec to an existing map, which will be the input for DoSetupPeerings
func appendGwPeeringSpec(gwPeerings map[string]*gwapi.PeeringSpec, vpc1, vpc2 *vpcapi.VPC, opts GwPeeringOptions) {
	entryName := fmt.Sprintf("%s--%s", vpc1.Name, vpc2.Name)

	// If no specific subnets provided, use all subnets from vpc1
	vpc1Subnets := opts.VPC1Subnets
	if len(vpc1Subnets) == 0 {
		vpc1Subnets = make([]string, 0, len(vpc1.Spec.Subnets))
		for _, subnet := range vpc1.Spec.Subnets {
			vpc1Subnets = append(vpc1Subnets, subnet.Subnet)
		}
	}

	// If no specific subnets provided, use all subnets from vpc2
	vpc2Subnets := opts.VPC2Subnets
	if len(vpc2Subnets) == 0 {
		vpc2Subnets = make([]string, 0, len(vpc2.Spec.Subnets))
		for _, subnet := range vpc2.Spec.Subnets {
			vpc2Subnets = append(vpc2Subnets, subnet.Subnet)
		}
	}

	// Build NAT configuration based on mode
	var natConfig *gwapi.PeeringNAT
	if len(opts.VPC1NATCIDR) > 0 || len(opts.VPC2NATCIDR) > 0 {
		if opts.StatefulNAT {
			natConfig = &gwapi.PeeringNAT{
				Stateful: &gwapi.PeeringStatefulNAT{
					IdleTimeout: kmetav1.Duration{Duration: 5 * time.Minute},
				},
			}
		} else {
			natConfig = &gwapi.PeeringNAT{
				Stateless: &gwapi.PeeringStatelessNAT{},
			}
		}
	}

	vpc1Expose := gwapi.PeeringEntryExpose{}
	for _, subnet := range vpc1Subnets {
		vpc1Expose.IPs = append(vpc1Expose.IPs, gwapi.PeeringEntryIP{CIDR: subnet})
	}
	// Add NAT ranges if provided
	for _, natCIDR := range opts.VPC1NATCIDR {
		vpc1Expose.As = append(vpc1Expose.As, gwapi.PeeringEntryAs{CIDR: natCIDR})
	}
	// Set NAT configuration if NAT ranges are provided
	if len(opts.VPC1NATCIDR) > 0 {
		vpc1Expose.NAT = natConfig
	}

	vpc2Expose := gwapi.PeeringEntryExpose{}
	for _, subnet := range vpc2Subnets {
		vpc2Expose.IPs = append(vpc2Expose.IPs, gwapi.PeeringEntryIP{CIDR: subnet})
	}
	// Add NAT ranges if provided
	for _, natCIDR := range opts.VPC2NATCIDR {
		vpc2Expose.As = append(vpc2Expose.As, gwapi.PeeringEntryAs{CIDR: natCIDR})
	}
	// Set NAT configuration if NAT ranges are provided
	if len(opts.VPC2NATCIDR) > 0 {
		vpc2Expose.NAT = natConfig
	}

	gwPeerings[entryName] = &gwapi.PeeringSpec{
		Peering: map[string]*gwapi.PeeringEntry{
			vpc1.Name: {
				Expose: []gwapi.PeeringEntryExpose{vpc1Expose},
			},
			vpc2.Name: {
				Expose: []gwapi.PeeringEntryExpose{vpc2Expose},
			},
		},
	}
}

func (testCtx *VPCPeeringTestCtx) waitForDHCPRenewal(ctx context.Context, serverName, ifName string, shortLeaseTime uint32) error {
	isL3Mode := testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.setupOpts.VPCMode == vpcapi.VPCModeL3Flat

	ssh, err := testCtx.getSSH(ctx, serverName)
	if err != nil {
		return fmt.Errorf("getting ssh config for server %s: %w", serverName, err)
	}

	_, stderr, err := ssh.Run(ctx, fmt.Sprintf("sudo networkctl reconfigure %s", ifName))
	if err != nil {
		if stderr != "" {
			return fmt.Errorf("reconfiguring interface to get short lease: %w (stderr: %s)", err, stderr)
		}

		return fmt.Errorf("reconfiguring interface to get short lease: %w", err)
	}

	// In L3VNI mode, check immediately for 2-step process
	if isL3Mode {
		shortLeaseFound := false
		for i := 0; i < 30; i++ {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during L3 reconfiguration polling: %w", ctx.Err())
			case <-time.After(1 * time.Second):
			}

			info, err := fetchAndParseDHCPLease(ctx, ssh, ifName)
			if err != nil || !info.HasLease {
				continue
			}

			// Look for short lease (L3VNI 2-step process)
			if !shortLeaseFound && info.ValidLifetime >= 5 && info.ValidLifetime <= 15 {
				shortLeaseFound = true
				slog.Debug("L3 mode short lease detected during reconfiguration", "server", serverName, "lease", info.ValidLifetime)

				continue
			}

			// Wait for full lease
			if info.ValidLifetime >= int(shortLeaseTime)-10 {
				if !shortLeaseFound {
					return fmt.Errorf("L3VNI mode requires 2-step lease process, but no short lease detected before full lease on %s", ifName) //nolint:goerr113
				}
				slog.Debug("L3 mode 2-step reconfiguration completed", "server", serverName, "final_lease", info.ValidLifetime)

				break
			}
		}
	} else {
		// L2VNI mode: simple wait
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during initial wait: %w", ctx.Err())
		case <-time.After(15 * time.Second):
		}
	}

	initialInfo, err := fetchAndParseDHCPLease(ctx, ssh, ifName)
	if err != nil {
		return fmt.Errorf("parsing initial lease: %w", err)
	}
	if !initialInfo.HasLease {
		return fmt.Errorf("no initial lease found on %s", ifName) //nolint:goerr113
	}
	slog.Info("Initial short lease acquired", "server", serverName, "interface", ifName,
		"lease_time", initialInfo.ValidLifetime, "expected_short", shortLeaseTime)
	if err := checkDHCPLease(initialInfo, int(shortLeaseTime), 20); err != nil {
		return fmt.Errorf("initial lease time not as expected: %w", err)
	}

	// Wait longer to ensure renewal has time to complete
	// DHCP renewal happens at T1 (50% of lease = 30s), plus time for server response
	renewalWaitTime := time.Duration(shortLeaseTime/2+25) * time.Second
	slog.Info("Waiting for automatic DHCP renewal", "server", serverName, "wait_time", renewalWaitTime)

	select {
	case <-ctx.Done():
		return fmt.Errorf("context cancelled during renewal wait: %w", ctx.Err())
	case <-time.After(renewalWaitTime):
	}

	renewedInfo, err := fetchAndParseDHCPLease(ctx, ssh, ifName)
	if err != nil {
		return fmt.Errorf("parsing renewed lease: %w", err)
	}
	if !renewedInfo.HasLease {
		return fmt.Errorf("no lease found after renewal period on %s", ifName) //nolint:goerr113
	}
	slog.Info("Post-renewal lease state", "server", serverName, "interface", ifName,
		"lease_time", renewedInfo.ValidLifetime, "initial_lease", initialInfo.ValidLifetime)

	// Verify the renewed lease has the configured short lease time
	// This tests both that renewal works AND that the DHCP configuration is properly applied
	expectedRenewedLease := int(shortLeaseTime)
	if err := checkDHCPLease(renewedInfo, expectedRenewedLease, 20); err != nil {
		return fmt.Errorf("renewed lease time not as expected: %w", err)
	}

	// Also verify that renewal actually happened (lease didn't just count down)
	if renewedInfo.ValidLifetime <= initialInfo.ValidLifetime/2 {
		return fmt.Errorf("lease doesn't appear to have been renewed - time not refreshed (initial: %d, renewed: %d)", //nolint:goerr113
			initialInfo.ValidLifetime, renewedInfo.ValidLifetime)
	}

	slog.Info("DHCP renewal completed successfully", "server", serverName,
		"initial_lease", initialInfo.ValidLifetime, "renewed_lease", renewedInfo.ValidLifetime)

	return nil
}

type ServerWithInterface struct {
	Name      string
	Interface string
}

type RenewalResult struct {
	Server   string
	Duration time.Duration
	Error    error
}

type DHCPLeaseInfo struct {
	ValidLifetime     int
	PreferredLifetime int
	HasLease          bool
	LeaseText         string
}

func parseDHCPLease(output string) (*DHCPLeaseInfo, error) {
	info := &DHCPLeaseInfo{}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "valid_lft") {
			continue
		}

		info.HasLease = true
		info.LeaseText = line

		tokens := strings.Fields(line)

		for i, token := range tokens {
			if token == "valid_lft" && i+1 < len(tokens) {
				if stripped, found := strings.CutSuffix(tokens[i+1], "sec"); found {
					if lease, err := strconv.Atoi(stripped); err == nil {
						info.ValidLifetime = lease
					}
				}
			}
			if token == "preferred_lft" && i+1 < len(tokens) {
				if stripped, found := strings.CutSuffix(tokens[i+1], "sec"); found {
					if lease, err := strconv.Atoi(stripped); err == nil {
						info.PreferredLifetime = lease
					}
				}
			}
		}

		break
	}

	if info.HasLease && info.ValidLifetime == 0 {
		return nil, fmt.Errorf("failed to parse lease time from: %s", info.LeaseText) //nolint:goerr113
	}

	return info, nil
}

func fetchAndParseDHCPLease(ctx context.Context, ssh *sshutil.Config, ifName string) (*DHCPLeaseInfo, error) {
	output, _, err := ssh.Run(ctx, fmt.Sprintf("ip addr show dev %s proto 4", ifName))
	if err != nil {
		return nil, fmt.Errorf("running ip addr on the host: %w", err)
	}

	info, err := parseDHCPLease(output)
	if err != nil {
		return nil, fmt.Errorf("parsing lease: %w", err)
	}

	return info, nil
}

// getInterfaceIPv4 retrieves the IPv4 address assigned to the specified network interface.
func getInterfaceIPv4(ctx context.Context, ssh *sshutil.Config, ifName string) (string, error) {
	ipOut, _, err := ssh.Run(ctx, fmt.Sprintf("ip addr show dev %s proto 4 | grep 'inet ' | awk '{print $2}' | cut -d'/' -f1", ifName))
	if err != nil {
		return "", fmt.Errorf("getting IP address: %w", err)
	}

	return strings.TrimSpace(ipOut), nil
}

// getInterfaceMAC retrieves the MAC address of the specified network interface.
func getInterfaceMAC(ctx context.Context, ssh *sshutil.Config, ifName string) (string, error) {
	macOut, _, err := ssh.Run(ctx, fmt.Sprintf("ip link show %s | grep -o 'link/ether [0-9a-f:]*' | awk '{print $2}'", ifName))
	if err != nil {
		return "", fmt.Errorf("getting MAC address: %w", err)
	}

	return strings.TrimSpace(macOut), nil
}

// AttachedServerInfo contains information about a server attached to a VPC subnet.
type AttachedServerInfo struct {
	ServerName string
	Interface  string
	VPCName    string
	SubnetName string
	VPC        *vpcapi.VPC
	Subnet     *vpcapi.VPCSubnet
}

var errNoAttachedServers = errors.New("no servers attached to VPC subnets found")

// findAnyAttachedServer finds the first server attached to any VPC subnet.
// Does not filter on DHCP being enabled. Skips hostBGP subnets as they behave differently.
// Returns errNoAttachedServers if no suitable server is found.
func findAnyAttachedServer(ctx context.Context, kube kclient.Client) (*AttachedServerInfo, error) {
	vpcAttaches := &vpcapi.VPCAttachmentList{}
	if err := kube.List(ctx, vpcAttaches); err != nil {
		return nil, fmt.Errorf("listing VPCAttachments: %w", err)
	}

	for _, attach := range vpcAttaches.Items {
		conn := &wiringapi.Connection{}
		if err := kube.Get(ctx, kclient.ObjectKey{
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
		if err := kube.Get(ctx, kclient.ObjectKey{
			Namespace: kmetav1.NamespaceDefault,
			Name:      attach.Spec.VPCName(),
		}, vpc); err != nil {
			continue
		}

		subnetName := attach.Spec.SubnetName()
		subnet := vpc.Spec.Subnets[subnetName]
		if subnet == nil || subnet.HostBGP {
			continue
		}

		var ifName string
		if conn.Spec.Unbundled != nil {
			ifName = fmt.Sprintf("%s.%d", conn.Spec.Unbundled.Link.Server.LocalPortName(), subnet.VLAN)
		} else {
			ifName = fmt.Sprintf("bond0.%d", subnet.VLAN)
		}

		return &AttachedServerInfo{
			ServerName: serverNames[0],
			Interface:  ifName,
			VPCName:    vpc.Name,
			SubnetName: subnetName,
			VPC:        vpc,
			Subnet:     subnet,
		}, nil
	}

	return nil, errNoAttachedServers
}
