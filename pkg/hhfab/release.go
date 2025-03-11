// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errNoExternals = errors.New("no external peers found")
var errNoMclags = errors.New("no MCLAG connections found")
var errNoEslags = errors.New("no ESLAG connections found")
var errNoBundled = errors.New("no bundled connections found")
var errNoUnbundled = errors.New("no unbundled connections found")
var errNotEnoughSpines = errors.New("not enough spines found")
var errInitalSetup = errors.New("initial setup failed")

type VPCPeeringTestCtx struct {
	workDir          string
	cacheDir         string
	kube             client.Client
	wipeBetweenTests bool
	opts             SetupVPCsOpts
	tcOpts           TestConnectivityOpts
	extName          string
	hhfabBin         string
	extended         bool
	failFast         bool
	pauseOnFail      bool
}

// prepare for a test: wipe the fabric and then create the VPCs according to the
// options in the test context
func (testCtx *VPCPeeringTestCtx) setupTest(ctx context.Context) error {
	if err := hhfctl.VPCWipeWithClient(ctx, testCtx.kube); err != nil {
		return fmt.Errorf("wiping fabric: %w", err)
	}
	if err := DoVLABSetupVPCs(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.opts); err != nil {
		return fmt.Errorf("setting up VPCs: %w", err)
	}

	return nil
}

// add a single VPC peering spec to an existing map, which will be the input for DoSetupPeerings
func appendVpcPeeringSpec(vpcPeerings map[string]*vpcapi.VPCPeeringSpec, index1, index2 int, remote string, vpc1Subnets, vpc2Subnets []string) {
	vpc1 := fmt.Sprintf("vpc-%02d", index1)
	vpc2 := fmt.Sprintf("vpc-%02d", index2)
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

// add a single external peering spec to an existing map, which will be the input for DoSetupPeerings
func appendExtPeeringSpec(extPeerings map[string]*vpcapi.ExternalPeeringSpec, vpcIndex int, ext string, subnets []string, prefixes []string) {
	entryName := fmt.Sprintf("vpc-%02d--%s", vpcIndex, ext)
	vpc := fmt.Sprintf("vpc-%02d", vpcIndex)
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

// populate the vpcPeerings map with all possible VPC peering combinations
func populateFullMeshVpcPeerings(ctx context.Context, kube client.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec) error {
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
func populateFullLoopVpcPeerings(ctx context.Context, kube client.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec) error {
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
func populateAllExternalVpcPeerings(ctx context.Context, kube client.Client, extPeerings map[string]*vpcapi.ExternalPeeringSpec) error {
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
			appendExtPeeringSpec(extPeerings, i+1, exts.Items[j].Name, []string{"subnet-01"}, []string{})
		}
	}

	return nil
}

// Get last applied generation of the hedgehog agent on switch swName
func getAgentGen(ctx context.Context, kube client.Client, swName string) (int64, error) {
	ag := &agentapi.Agent{}
	err := kube.Get(ctx, client.ObjectKey{Name: swName, Namespace: "default"}, ag)
	if err != nil {
		return -1, fmt.Errorf("getting agent %s: %w", swName, err)
	}

	return ag.Status.LastAppliedGen, nil
}

// Wait until the hedgehog agent on switch swName has moved beyond the given generation
func waitAgentGen(ctx context.Context, kube client.Client, swName string, lastGen int64) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ag := &agentapi.Agent{}

	for {
		err := kube.Get(ctx, client.ObjectKey{Name: swName, Namespace: "default"}, ag)
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
func execConfigCmd(hhfabBin, workDir, swName string, cmds ...string) error {
	cmd := exec.Command(
		hhfabBin,
		"vlab",
		"ssh",
		"-n",
		swName,
		"sonic-cli",
		"-c", "configure",
	)
	for _, c := range cmds {
		// add escaped double quotes around the command
		cmd.Args = append(cmd.Args, "-c", fmt.Sprintf("\"%s\"", c))
	}
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Error("Configuring switch", "switch", swName, "error", err)
		slog.Debug("Output of errored command", "output", string(out))

		return fmt.Errorf("configuring switch %s: %w", swName, err)
	}

	return nil
}

// Run a command on node nodeName via ssh.
func execNodeCmd(hhfabBin, workDir, nodeName string, command string) error {
	cmd := exec.Command(
		hhfabBin,
		"vlab",
		"ssh",
		"-n",
		nodeName,
		command,
	)
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Error("Running command", "command", command, "node", nodeName, "error", err)
		slog.Debug("Output of errored command", "output", string(out))

		return fmt.Errorf("running command %s on node %s: %w", command, nodeName, err)
	}

	return nil
}

// Enable or disable the hedgehog agent on switch swName.
func changeAgentStatus(hhfabBin, workDir, swName string, up bool) error {
	return execNodeCmd(hhfabBin, workDir, swName, fmt.Sprintf("sudo systemctl %s hedgehog-agent.service", map[bool]string{true: "start", false: "stop"}[up]))
}

// Change the admin status of a switch port via the sonic-cli, i.e. by running (no) shutdown on the port.
func changeSwitchPortStatus(hhfabBin, workDir, deviceName, nosPortName string, up bool) error {
	slog.Debug("Changing switch port status", "device", deviceName, "port", nosPortName, "up", up)
	if up {
		if err := execConfigCmd(
			hhfabBin,
			workDir,
			deviceName,
			fmt.Sprintf("interface %s", nosPortName),
			"no shutdown",
		); err != nil {
			return err
		}
	} else {
		if err := execConfigCmd(
			hhfabBin,
			workDir,
			deviceName,
			fmt.Sprintf("interface %s", nosPortName),
			"shutdown",
		); err != nil {
			return err
		}
	}

	return nil
}

// ping the IP address ip from node nodeName, expectSuccess determines whether the ping should succeed or fail.
func pingFromServer(hhfabBin, workDir, nodeName, ip string, expectSuccess bool) error {
	cmd := exec.Command(
		hhfabBin,
		"vlab",
		"ssh",
		"-n",
		nodeName,
		"ping",
		"-c", "3",
		"-W", "1",
		ip,
	)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	// NOTE: there's no real need to check the output as, when both -c and -W are specified,
	// ping will return exit code 0 only if all packets were received.
	pingOK := err == nil && strings.Contains(string(out), "0% packet loss")
	if expectSuccess && !pingOK {
		slog.Error("Ping failed, expected success", "source", nodeName, "target", ip, "error", err)
		slog.Debug("Output of ping", "output", string(out))

		return errors.New("ping failed, expected success") //nolint:goerr113
	} else if !expectSuccess && pingOK {
		slog.Error("Ping succeeded, expected failure", "source", nodeName, "target", ip, "error", err)

		return errors.New("ping succeeded, expected failure") //nolint:goerr113
	}

	return nil
}

// Test functions

// The starter test is presumably an arbitrary point in the space of possible VPC peering configurations.
// It was presumably chosen because going from this to a full mesh configuration could trigger
// the gNMI bug. Note that in order to reproduce it one should disable the forced cleanup between
// tests.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsStarterTest(ctx context.Context) (bool, error) {
	// 1+2:r=border 1+3 3+5 2+4 4+6 5+6 6+7 7+8 8+9  5~default--5835:s=subnet-01 6~default--5835:s=subnet-01  1~default--5835:s=subnet-01  2~default--5835:s=subnet-01  9~default--5835:s=subnet-01  7~default--5835:s=subnet-01
	// check whether border switchgroup exists
	remote := "border"
	swGroup := &wiringapi.SwitchGroup{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: remote}, swGroup); err != nil {
		slog.Warn("Border switch group not found, not using remote", "error", err)
		remote = ""
	}

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 9)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, remote, []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 1, 3, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 3, 5, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 4, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 4, 6, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 5, 6, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 6, 7, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 7, 8, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 8, 9, "", []string{}, []string{})

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	appendExtPeeringSpec(externalPeerings, 5, testCtx.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 6, testCtx.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 1, testCtx.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 2, testCtx.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 9, testCtx.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 7, testCtx.extName, []string{"subnet-01"}, []string{})

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil
}

// Test connectivity between all VPCs in a full mesh configuration, including all externals.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullMeshAllExternalsTest(ctx context.Context) (bool, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 15)
	if err := populateFullMeshVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, fmt.Errorf("populating full mesh VPC peerings: %w", err)
	}

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, fmt.Errorf("populating all external VPC peerings: %w", err)
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil
}

// Test connectivity between all VPCs with no peering except of the external ones.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsOnlyExternalsTest(ctx context.Context) (bool, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, fmt.Errorf("populating all external VPC peerings: %w", err)
	}
	if len(externalPeerings) == 0 {
		slog.Info("No external peerings found, skipping test")

		return true, errNoExternals
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil
}

// Test connectivity between all VPCs in a full loop configuration, including all externals.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullLoopAllExternalsTest(ctx context.Context) (bool, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 6)
	if err := populateFullLoopVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, fmt.Errorf("populating full loop VPC peerings: %w", err)
	}
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, fmt.Errorf("populating all external VPC peerings: %w", err)
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil
}

// Arbitrary configuration which again was shown to occasionally trigger the gNMI bug.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsSergeisSpecialTest(ctx context.Context) (bool, error) {
	// 1+2 2+3 2+4:r=border 6+5 1~default--5835:s=subnet-01
	// check whether border switchgroup exists
	remote := "border"
	swGroup := &wiringapi.SwitchGroup{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: remote}, swGroup); err != nil {
		slog.Warn("Border switch group not found, not using remote", "error", err)
		remote = ""
	}

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 4)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 4, remote, []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 6, 5, "", []string{}, []string{})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	appendExtPeeringSpec(externalPeerings, 1, testCtx.extName, []string{"subnet-01"}, []string{})
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil
}

// disable agent, shutdown port on switch, test connectivity, enable agent, set port up
func shutDownLinkAndTest(ctx context.Context, testCtx *VPCPeeringTestCtx, link wiringapi.ServerToSwitchLink) (returnErr error) {
	switchPort := link.Switch
	deviceName := switchPort.DeviceName()
	// get switch profile to find the port name in sonic-cli
	sw := &wiringapi.Switch{}
	switchName := switchPort.DeviceName()
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: switchName}, sw); err != nil {
		return fmt.Errorf("getting switch %s: %w", switchName, err)
	}
	profile := &wiringapi.SwitchProfile{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: sw.Spec.Profile}, profile); err != nil {
		return fmt.Errorf("getting switch profile %s: %w", sw.Spec.Profile, err)
	}
	portMap, err := profile.Spec.GetAPI2NOSPortsFor(&sw.Spec)
	if err != nil {
		return fmt.Errorf("getting API2NOS ports for switch %s: %w", switchName, err)
	}
	nosPortName, ok := portMap[switchPort.LocalPortName()]
	if !ok {
		return fmt.Errorf("Port %s not found in switch profile %s for switch %s", switchPort.LocalPortName(), profile.Name, deviceName) //nolint:goerr113
	}
	// disable agent
	if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, false); err != nil {
		return fmt.Errorf("disabling HH agent: %w", err)
	}
	defer func() {
		maxRetries := 5
		sleepTime := time.Second * 5
		for i := 0; i < maxRetries; i++ {
			if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, true); err != nil {
				slog.Error("Enabling HH agent", "error", err)
				if i < maxRetries-1 {
					slog.Warn("Retrying in 5 seconds")
					time.Sleep(sleepTime)
				}
			} else {
				return
			}
		}
		if returnErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("Could not enable HH agent on switch %s after %d attempts", deviceName, maxRetries)) //nolint:goerr113
		} else {
			returnErr = fmt.Errorf("Could not enable HH agent on switch %s after %d attempts", deviceName, maxRetries) //nolint:goerr113
		}
	}()

	// set port down
	if err := changeSwitchPortStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, nosPortName, false); err != nil {
		return fmt.Errorf("setting switch port down: %w", err)
	}
	defer func() {
		if err := changeSwitchPortStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, nosPortName, true); err != nil {
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

	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return fmt.Errorf("testing connectivity: %w", err)
	}

	return nil
}

// Basic test for mclag failover.
// For each mclag connection, set one of the links down by shutting down the port on the switch,
// then test connectivity. Repeat for the other link.
func (testCtx *VPCPeeringTestCtx) mclagTest(ctx context.Context) (bool, error) {
	// list connections in the fabric, filter by MC-LAG connection type
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, client.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeMCLAG}); err != nil {
		return false, fmt.Errorf("listing connections: %w", err)
	}
	if len(conns.Items) == 0 {
		slog.Info("No MCLAG connections found, skipping test")

		return true, errNoMclags
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing MCLAG connection", "connection", conn.Name)
		if len(conn.Spec.MCLAG.Links) != 2 {
			return false, fmt.Errorf("MCLAG connection %s has %d links, expected 2", conn.Name, len(conn.Spec.MCLAG.Links)) //nolint:goerr113
		}
		for _, link := range conn.Spec.MCLAG.Links {
			if err := shutDownLinkAndTest(ctx, testCtx, link); err != nil {
				return false, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
			if !testCtx.extended {
				slog.Debug("Skipping other link, set extended=true to iterate over all links")

				break
			}
		}
	}

	return false, nil
}

// Basic test for eslag failover.
// For each eslag connection, set one of the links down by shutting down the port on the switch,
// then test connectivity. Repeat for the other link.
func (testCtx *VPCPeeringTestCtx) eslagTest(ctx context.Context) (bool, error) {
	// list connections in the fabric, filter by ES-LAG connection type
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, client.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeESLAG}); err != nil {
		return false, fmt.Errorf("listing connections: %w", err)
	}
	if len(conns.Items) == 0 {
		slog.Info("No ESLAG connections found, skipping test")

		return true, errNoEslags
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing ESLAG connection", "connection", conn.Name)
		if len(conn.Spec.ESLAG.Links) != 2 {
			return false, fmt.Errorf("ESLAG connection %s has %d links, expected 2", conn.Name, len(conn.Spec.ESLAG.Links)) //nolint:goerr113
		}
		for _, link := range conn.Spec.ESLAG.Links {
			if err := shutDownLinkAndTest(ctx, testCtx, link); err != nil {
				return false, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
			if !testCtx.extended {
				slog.Debug("Skipping other link, set extended=true to iterate over all links")

				break
			}
		}
	}

	return false, nil
}

// Basic test for bundled connection failover.
// For each bundled connection, set one of the links down by shutting down the port on the switch,
// then test connectivity. Repeat for the other link(s).
func (testCtx *VPCPeeringTestCtx) bundledFailoverTest(ctx context.Context) (bool, error) {
	// list connections in the fabric, filter by bundled connection type
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, client.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeBundled}); err != nil {
		return false, fmt.Errorf("listing connections: %w", err)
	}
	if len(conns.Items) == 0 {
		slog.Info("No bundled connections found, skipping test")

		return true, errNoBundled
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing Bundled connection", "connection", conn.Name)
		if len(conn.Spec.Bundled.Links) < 2 {
			return false, fmt.Errorf("MCLAG connection %s has %d links, expected at least 2", conn.Name, len(conn.Spec.Bundled.Links)) //nolint:goerr113
		}
		for _, link := range conn.Spec.Bundled.Links {
			if err := shutDownLinkAndTest(ctx, testCtx, link); err != nil {
				return false, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
			if !testCtx.extended {
				slog.Debug("Skipping other link, set extended=true to iterate over all links")

				break
			}
		}
	}

	return false, nil
}

// Basic test for spine failover.
// Iterate over the spine switches (skip the first one), and shut down all links towards them.
// Test connectivity, then re-enable the links.
func (testCtx *VPCPeeringTestCtx) spineFailoverTest(ctx context.Context) (bool, error) {
	var returnErr error

	// list spines, unfortunately we cannot filter by role
	switches := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, switches); err != nil {
		return false, fmt.Errorf("listing switches: %w", err)
	}
	spines := make([]wiringapi.Switch, 0)
	for _, sw := range switches.Items {
		if sw.Spec.Role == wiringapi.SwitchRoleSpine {
			spines = append(spines, sw)
		}
	}

	if len(spines) < 2 {
		slog.Info("Not enough spines found, skipping test")

		return true, errNotEnoughSpines
	}

outer:
	for i, spine := range spines {
		if i == 0 {
			continue
		}
		slog.Debug("Disabling links to spine", "spine", spine.Name)
		// get switch profile to find the port name in sonic-cli
		profile := &wiringapi.SwitchProfile{}
		if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: spine.Spec.Profile}, profile); err != nil {
			return false, fmt.Errorf("getting switch profile %s: %w", spine.Spec.Profile, err)
		}
		portMap, err := profile.Spec.GetAPI2NOSPortsFor(&spine.Spec)
		if err != nil {
			return false, fmt.Errorf("getting API2NOS ports for switch %s: %w", spine.Name, err)
		}
		// disable agent on spine
		if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, spine.Name, false); err != nil {
			return false, fmt.Errorf("disabling HH agent: %w", err)
		}

		// look for connections that have this spine as a switch
		conns := &wiringapi.ConnectionList{}
		if err := testCtx.kube.List(ctx, conns, client.MatchingLabels{wiringapi.ListLabelSwitch(spine.Name): "true", wiringapi.LabelConnectionType: wiringapi.ConnectionTypeFabric}); err != nil {
			returnErr = fmt.Errorf("listing connections: %w", err)

			break
		}
		slog.Debug(fmt.Sprintf("Found %d connections to spine %s", len(conns.Items), spine.Name))
		for _, conn := range conns.Items {
			for _, link := range conn.Spec.Fabric.Links {
				spinePort := link.Spine.LocalPortName()
				nosPortName, ok := portMap[spinePort]
				if !ok {
					returnErr = fmt.Errorf("Port %s not found in switch profile %s for switch %s", spinePort, profile.Name, spine.Name) //nolint:goerr113

					break outer
				}
				if err := changeSwitchPortStatus(testCtx.hhfabBin, testCtx.workDir, spine.Name, nosPortName, false); err != nil {
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
		if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
			returnErr = fmt.Errorf("testing connectivity: %w", err)
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
		for i := 0; i < maxRetries; i++ {
			if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, spine.Name, true); err != nil {
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
			returnErr = errors.Join(returnErr, fmt.Errorf("Could not enable HH agent on switch %s after %d attempts", spine.Name, maxRetries)) //nolint:goerr113
		} else {
			returnErr = fmt.Errorf("Could not enable HH agent on switch %s after %d attempts", spine.Name, maxRetries) //nolint:goerr113
		}
	}

	return false, returnErr
}

// Vanilla test for VPC peering, just test connectivity without any further restriction
func (testCtx *VPCPeeringTestCtx) noRestrictionsTest(ctx context.Context) (bool, error) {
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil
}

// Test VPC peering with multiple subnets and with restrictions.
// Assumes the scenario has 3 subnets for VPC vpc-01 and vpc-02.
// 1. Isolate subnet-01 in vpc-01, test connectivity
// 2. Override isolation with explicit permit list, test connectivity
// 3. Set restricted flag in subnet-02 in vpc-02, test connectivity
// 4. Remove all restrictions
func (testCtx *VPCPeeringTestCtx) multiSubnetsIsolationTest(ctx context.Context) (bool, error) {
	var returnErr error

	// modify vpc-01 to have isolated subnets
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		return false, fmt.Errorf("getting VPC vpc-01: %w", err)
	}
	if len(vpc.Spec.Subnets) != 3 {
		return false, fmt.Errorf("VPC vpc-01 has %d subnets, expected 3", len(vpc.Spec.Subnets)) //nolint:goerr113
	}

	// this is going to be used later, let's get it out of the way
	vpc2 := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-02"}, vpc2); err != nil {
		return false, fmt.Errorf("getting VPC vpc-02: %w", err)
	}
	if len(vpc2.Spec.Subnets) != 3 {
		return false, fmt.Errorf("VPC vpc-02 has %d subnets, expected 3", len(vpc2.Spec.Subnets)) //nolint:goerr113
	}
	subnet2, ok := vpc2.Spec.Subnets["subnet-02"]
	if !ok {
		return false, fmt.Errorf("Subnet subnet-02 not found in VPC vpc-02") //nolint:goerr113
	}

	permitList := make([]string, 0)
	for subName, sub := range vpc.Spec.Subnets {
		if subName == "subnet-01" {
			slog.Debug("Isolating subnet subnet-01")
			sub.Isolated = pointer.To(true)
		}
		permitList = append(permitList, subName)
	}
	_, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, fmt.Errorf("updating VPC vpc-01: %w", err)
	}
	// TODO: agent generation check to ensure that the change was picked up
	// (tricky as we need to derive switch name from vpc, which involves quite a few steps)
	time.Sleep(5 * time.Second)
	if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
		returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
	} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		returnErr = fmt.Errorf("testing connectivity with isolated subnet-01: %w", err)
	}

	// override isolation with explicit permit list
	if returnErr == nil {
		vpc.Spec.Permit = make([][]string, 1)
		vpc.Spec.Permit[0] = make([]string, 3)
		copy(vpc.Spec.Permit[0], permitList)
		slog.Debug("Permitting subnets", "subnets", permitList)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC vpc-01: %w", err)
		} else {
			time.Sleep(5 * time.Second)
			if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
				returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with permit-list override: %w", err)
			}
		}
	}

	// set restricted flags in vpc-02
	if returnErr == nil {
		slog.Debug("Restricting subnet 'subnet-02'")
		subnet2.Restricted = pointer.To(true)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc2)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC vpc-02: %w", err)
		} else {
			time.Sleep(5 * time.Second)
			if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
				returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with restricted subnet-02: %w", err)
			}
		}
	}

	// remove all restrictions for next tests
	slog.Debug("Removing all restrictions")
	vpc.Spec.Permit = make([][]string, 0)
	for _, sub := range vpc.Spec.Subnets {
		sub.Isolated = pointer.To(false)
	}
	for _, sub := range vpc2.Spec.Subnets {
		sub.Restricted = pointer.To(false)
	}
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		if returnErr == nil {
			returnErr = fmt.Errorf("updating VPC vpc-01: %w", err)
		} else {
			returnErr = errors.Join(returnErr, fmt.Errorf("updating VPC vpc-01: %w", err))
		}
	}
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc2)
	if err != nil {
		if returnErr == nil {
			returnErr = fmt.Errorf("updating VPC vpc-02: %w", err)
		} else {
			returnErr = errors.Join(returnErr, fmt.Errorf("updating VPC vpc-02: %w", err))
		}
	}
	time.Sleep(5 * time.Second)
	if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
		if returnErr == nil {
			returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
		} else {
			returnErr = errors.Join(returnErr, fmt.Errorf("waiting for switches to be ready: %w", err))
		}
	}

	// test connectivity
	if returnErr == nil && testCtx.extended {
		if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
			returnErr = fmt.Errorf("testing connectivity after removing all constraints: %w", err)
		}
	}

	return false, returnErr
}

// Test VPC peering with multiple subnets and with subnet filtering.
// Assumes the scenario has 3 VPCs and at least 2 subnets in each VPC.
// It creates peering between all VPCs, but restricts the peering to only one subnet
// between 1-3 and 2-3. It then tests connectivity.
func (testCtx *VPCPeeringTestCtx) multiSubnetsSubnetFilteringTest(ctx context.Context) (bool, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 3)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 1, 3, "", []string{}, []string{"subnet-01"})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, "", []string{}, []string{"subnet-02"})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, fmt.Errorf("setting up peerings: %w", err)
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil
}

// Test VPC peering with multiple subnets and with restrictions.
// Assumes the scenario has 3 subnets for VPC vpc-01.
// 1. Isolate subnet-01, test connectivity
// 2. Set restricted flag in subnet-02, test connectivity
// 3. Set both isolated and restricted flags in subnet-03, test connectivity
// 4. Override isolation with explicit permit list, test connectivity
// 5. Remove all restrictions
func (testCtx *VPCPeeringTestCtx) singleVPCWithRestrictionsTest(ctx context.Context) (bool, error) {
	var returnErr error

	// isolate subnet-01
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		return false, fmt.Errorf("getting VPC vpc-01: %w", err)
	}
	if len(vpc.Spec.Subnets) != 3 {
		return false, fmt.Errorf("VPC vpc-01 has %d subnets, expected 3", len(vpc.Spec.Subnets)) //nolint:goerr113
	}
	subnet1, ok := vpc.Spec.Subnets["subnet-01"]
	if !ok {
		return false, errors.New("Subnet subnet-01 not found in VPC vpc-01") //nolint:goerr113
	}
	subnet2, ok := vpc.Spec.Subnets["subnet-02"]
	if !ok {
		return false, errors.New("Subnet subnet-02 not found in VPC vpc-01") //nolint:goerr113
	}
	subnet3, ok := vpc.Spec.Subnets["subnet-03"]
	if !ok {
		return false, errors.New("Subnet subnet-03 not found in VPC vpc-01") //nolint:goerr113
	}
	permitList := []string{"subnet-01", "subnet-02", "subnet-03"}

	slog.Debug("Isolating subnet 'subnet-01'")
	subnet1.Isolated = pointer.To(true)
	_, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, fmt.Errorf("updating VPC vpc-01: %w", err)
	}
	time.Sleep(5 * time.Second)
	if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
		returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
	} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		returnErr = fmt.Errorf("testing connectivity with subnet-01 isolated: %w", err)
	}

	// set restricted flags for subnet-02
	if returnErr == nil {
		slog.Debug("Restricting subnet 'subnet-02'")
		subnet2.Restricted = pointer.To(true)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC vpc-01: %w", err)
		} else {
			time.Sleep(5 * time.Second)
			if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
				returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity: %w", err)
			}
		}
	}

	// make subnet-03 isolated and restricted
	if returnErr == nil {
		slog.Debug("Isolating and restricting subnet 'subnet-03'")
		subnet3.Isolated = pointer.To(true)
		subnet3.Restricted = pointer.To(true)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC vpc-01: %w", err)
		} else {
			time.Sleep(5 * time.Second)
			if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
				returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity: %w", err)
			}
		}
	}

	// override isolation with explicit permit list
	if returnErr == nil {
		vpc.Spec.Permit = make([][]string, 1)
		vpc.Spec.Permit[0] = make([]string, 3)
		copy(vpc.Spec.Permit[0], permitList)
		slog.Debug("Permitting subnets", "subnets", permitList)
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			returnErr = fmt.Errorf("updating VPC vpc-01: %w", err)
		} else {
			time.Sleep(5 * time.Second)
			if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
				returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity: %w", err)
			}
		}
	}

	// remove all restrictions for next tests
	slog.Debug("Removing all restrictions")
	vpc.Spec.Permit = make([][]string, 0)
	for _, sub := range vpc.Spec.Subnets {
		sub.Isolated = pointer.To(false)
		sub.Restricted = pointer.To(false)
	}
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		if returnErr == nil {
			returnErr = fmt.Errorf("updating VPC vpc-01: %w", err)
		} else {
			returnErr = errors.Join(returnErr, fmt.Errorf("updating VPC vpc-01: %w", err))
		}
	}
	time.Sleep(5 * time.Second)
	if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
		if returnErr == nil {
			returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
		} else {
			returnErr = errors.Join(returnErr, fmt.Errorf("waiting for switches to be ready: %w", err))
		}
	}

	if returnErr == nil && testCtx.extended {
		// test connectivity
		if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
			return false, fmt.Errorf("testing connectivity: %w", err)
		}
	}

	return false, returnErr
}

/* test following the manual steps to test static external attachments:
 * 1. find an unbundled connection, take note of params (server, switch, switch port, server port)
 * 2. delete the existing VPC attachement associated with the connection
 * 3. delete the connection
 * 4. create a new connection with the static external specified, using the port which is connected to the server
 * 4a. specify that the static external is within an existing vpc, i.e. vpc-01
 * 5. ssh into server, cleanup with hhfctl, then add the address specified in the static external, i.e. 172.31.255.1/24, to en2ps1 + set it up
 * 6. ssh into server and add a default route via the nexthop specified in the static external, i.e. 172.31.255.5
 * 7. ssh into a server in the specified vpc, and ping the address specified in the static external, i.e. 172.31.255.1
 * 8. add dummy interfaces within the subnets specified in the static external and ping them from the source server
 * 9. cleanup everything and restore the original state
 */
func (testCtx *VPCPeeringTestCtx) staticExternalTest(ctx context.Context) (_ bool, returnErr error) {
	// find an unbundled connection
	connList := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, connList, client.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeUnbundled}); err != nil {
		return false, fmt.Errorf("listing connections: %w", err)
	}
	if len(connList.Items) == 0 {
		slog.Info("No unbundled connections found, skipping test")

		return true, errNoUnbundled
	}
	conn := connList.Items[0]
	server := conn.Spec.Unbundled.Link.Server.DeviceName()
	switchName := conn.Spec.Unbundled.Link.Switch.DeviceName()
	switchPortName := conn.Spec.Unbundled.Link.Switch.PortName()
	serverPortName := conn.Spec.Unbundled.Link.Server.LocalPortName()
	slog.Debug("Found unbundled connection", "connection", conn.Name, "server", server, "switch", switchName, "port", switchPortName)

	// get agent generation for the switch
	gen, genErr := getAgentGen(ctx, testCtx.kube, switchName)
	if genErr != nil {
		return false, genErr
	}

	// Get the corresponding VPCAttachment
	vpcAttList := &vpcapi.VPCAttachmentList{}
	if err := testCtx.kube.List(ctx, vpcAttList, client.MatchingLabels{wiringapi.LabelConnection: conn.Name}); err != nil {
		return false, fmt.Errorf("listing VPCAttachments: %w", err)
	}
	if len(vpcAttList.Items) != 1 {
		return false, fmt.Errorf("Expected 1 VPCAttachment for connection %s, got %d", conn.Name, len(vpcAttList.Items)) //nolint:goerr113
	}
	vpcAtt := vpcAttList.Items[0]
	subnetName := vpcAtt.Spec.SubnetName()
	vpcName := vpcAtt.Spec.VPCName()
	slog.Debug("Found VPCAttachment", "attachment", vpcAtt.Name, "subnet", subnetName, "vpc", vpcName)
	// Get the VPCAttachment's VPC so we can extract the VLAN (for hhnet config)
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: vpcName}, vpc); err != nil {
		return false, fmt.Errorf("getting VPC %s: %w", vpcName, err)
	}
	vlan := vpc.Spec.Subnets[subnetName].VLAN
	slog.Debug("VLAN for VPCAttachment", "vlan", vlan)
	// Delete the VPCAttachment
	slog.Debug("Deleting VPCAttachment", "attachment", vpcAtt.Name)
	if err := testCtx.kube.Delete(ctx, &vpcAtt); err != nil {
		return false, fmt.Errorf("deleting VPCAttachment %s: %w", vpcAtt.Name, err)
	}
	defer func() {
		newVpcAtt := &vpcapi.VPCAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vpcAtt.Name,
				Namespace: vpcAtt.Namespace,
			},
			Spec: vpcAtt.Spec,
		}
		gen, genErr := getAgentGen(ctx, testCtx.kube, switchName)
		if genErr != nil {
			if returnErr == nil {
				returnErr = genErr
			} else {
				returnErr = errors.Join(returnErr, genErr)
			}

			return
		}
		slog.Debug("Creating VPCAttachment", "attachment", newVpcAtt.Name)
		if err := testCtx.kube.Create(ctx, newVpcAtt); err != nil {
			createErr := fmt.Errorf("creating VPCAttachment %s: %w", newVpcAtt.Name, err)
			if returnErr == nil {
				returnErr = createErr
			} else {
				returnErr = errors.Join(returnErr, createErr)
			}

			return
		}
		if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
			if returnErr == nil {
				returnErr = err
			} else {
				returnErr = errors.Join(returnErr, err)
			}

			return
		}
		if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
			waitErr := fmt.Errorf("waiting for switches to be ready: %w", err)
			if returnErr == nil {
				returnErr = waitErr
			} else {
				returnErr = errors.Join(returnErr, waitErr)
			}

			return
		}
		slog.Debug("Invoking hhnet cleanup on server", "server", server)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "/opt/bin/hhnet cleanup"); err != nil {
			cleanupErr := fmt.Errorf("cleaning up %s via hhnet: %w", server, err)
			if returnErr == nil {
				returnErr = cleanupErr
			} else {
				returnErr = errors.Join(returnErr, cleanupErr)
			}

			return
		}
		slog.Debug("Configuring VLAN on server", "server", server, "vlan", vlan, "port", serverPortName)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, fmt.Sprintf("/opt/bin/hhnet vlan %d %s", vlan, serverPortName)); err != nil {
			vlanErr := fmt.Errorf("configuring VLAN on %s: %w", server, err)
			if returnErr == nil {
				returnErr = vlanErr
			} else {
				returnErr = errors.Join(returnErr, vlanErr)
			}

			return
		}
		slog.Debug("All state restored")
	}()

	slog.Debug("Deleting connection", "connection", conn.Name)
	if err := testCtx.kube.Delete(ctx, &conn); err != nil {
		return false, fmt.Errorf("deleting connection %s: %w", conn.Name, err)
	}

	defer func() {
		gen, genErr := getAgentGen(ctx, testCtx.kube, switchName)
		if genErr != nil {
			if returnErr == nil {
				returnErr = genErr
			} else {
				returnErr = errors.Join(returnErr, genErr)
			}

			return
		}
		newConn := &wiringapi.Connection{
			ObjectMeta: metav1.ObjectMeta{
				Name:      conn.Name,
				Namespace: conn.Namespace,
			},
			Spec: conn.Spec,
		}
		slog.Debug("Creating connection", "connection", newConn.Name)
		if err := testCtx.kube.Create(ctx, newConn); err != nil {
			createErr := fmt.Errorf("creating connection %s: %w", newConn.Name, err)
			if returnErr == nil {
				returnErr = createErr
			} else {
				returnErr = errors.Join(returnErr, createErr)
			}

			return
		}
		if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
			if returnErr == nil {
				returnErr = err
			} else {
				returnErr = errors.Join(returnErr, err)
			}

			return
		}
		if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
			waitErr := fmt.Errorf("waiting for switches to be ready: %w", err)
			if returnErr == nil {
				returnErr = waitErr
			} else {
				returnErr = errors.Join(returnErr, waitErr)
			}
		}
	}()

	if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
		return false, err
	}
	if err := WaitSwitchesReady(ctx, testCtx.kube, 1, 5*time.Minute); err != nil {
		return false, fmt.Errorf("waiting for switches to be ready: %w", err)
	}
	gen, genErr = getAgentGen(ctx, testCtx.kube, switchName)
	if genErr != nil {
		return false, genErr
	}

	// Create new connection with static external
	staticExtConn := &wiringapi.Connection{}
	staticExtConn.Name = fmt.Sprintf("release-test--static-external--%s", switchName)
	staticExtConn.Namespace = "default"
	staticExtConn.Spec.StaticExternal = &wiringapi.ConnStaticExternal{
		WithinVPC: "vpc-01",
		Link: wiringapi.ConnStaticExternalLink{
			Switch: wiringapi.ConnStaticExternalLinkSwitch{
				BasePortName: wiringapi.NewBasePortName(switchPortName),
				IP:           "172.31.255.5/24",
				Subnets:      []string{"10.99.0.0/24", "10.199.0.100/32"},
				NextHop:      "172.31.255.1",
			},
		},
	}
	slog.Debug("Creating connection", "connection", staticExtConn.Name)
	if err := testCtx.kube.Create(ctx, staticExtConn); err != nil {
		return false, fmt.Errorf("creating connection %s: %w", staticExtConn.Name, err)
	}
	defer func() {
		slog.Debug("Deleting connection", "connection", staticExtConn.Name)
		if err := testCtx.kube.Delete(ctx, staticExtConn); err != nil {
			deleteErr := fmt.Errorf("deleting connection %s: %w", staticExtConn.Name, err)
			if returnErr == nil {
				returnErr = deleteErr
			} else {
				returnErr = errors.Join(returnErr, deleteErr)
			}
		}
	}()

	if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
		return false, err
	}
	if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
		return false, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Add address and default route to en2ps1 on the server
	slog.Debug("Adding address and default route to en2ps1 on the server", "server", server)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "hhnet cleanup"); err != nil {
		return false, fmt.Errorf("cleaning up server via hhnet: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip addr add 172.31.255.1/24 dev enp2s1"); err != nil {
		return false, fmt.Errorf("adding address to server: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip link set dev enp2s1 up"); err != nil {
		return false, fmt.Errorf("setting up server interface: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip route add default via 172.31.255.5"); err != nil {
		return false, fmt.Errorf("adding default route to server: %w", err)
	}
	slog.Debug("Adding dummy inteface with address 10.199.0.100/32 to the server", "server", server)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip link add dummy0 type dummy"); err != nil {
		return false, fmt.Errorf("adding dummy interface to server: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip addr add 10.199.0.100/32 dev dummy0"); err != nil {
		return false, fmt.Errorf("adding address to dummy interface on server: %w", err)
	}
	defer func() {
		slog.Debug("Removing address and default route from en2ps1 on the server", "server", server)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip addr del 172.31.255.1/24 dev enp2s1"); err != nil {
			addrDelErr := fmt.Errorf("removing address from %s: %w", server, err)
			if returnErr == nil {
				returnErr = addrDelErr
			} else {
				returnErr = errors.Join(returnErr, addrDelErr)
			}

			return
		}
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip link del dev dummy0"); err != nil {
			dummyDelErr := fmt.Errorf("removing dummy interface from %s: %w", server, err)
			if returnErr == nil {
				returnErr = dummyDelErr
			} else {
				returnErr = errors.Join(returnErr, dummyDelErr)
			}

			return
		}
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "hhnet cleanup"); err != nil {
			cleanupErr := fmt.Errorf("cleaning up %s via hhnet: %w", server, err)
			if returnErr == nil {
				returnErr = cleanupErr
			} else {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()

	srcServer, err := getServer1(ctx, testCtx.kube)
	if err != nil {
		return false, err
	}

	// Ping the addresses from the source server
	slog.Debug("Pinging 172.31.255.1", "source-server", srcServer)
	if err := pingFromServer(testCtx.hhfabBin, testCtx.workDir, srcServer, "172.31.255.1", true); err != nil {
		return false, fmt.Errorf("ping from %s to 172.31.255.1: %w", srcServer, err)
	}
	slog.Debug("Pinging 10.199.0.100", "source-server", srcServer)
	if err := pingFromServer(testCtx.hhfabBin, testCtx.workDir, srcServer, "10.199.0.100", true); err != nil {
		return false, fmt.Errorf("ping from %s to 10.199.0.100: %w", srcServer, err)
	}
	slog.Debug("All good, cleaning up")

	return false, nil
}

// helper to get server-1 (might be called differently depending on the env)
func getServer1(ctx context.Context, kube client.Client) (string, error) {
	serverName := "server-1"
	server := &wiringapi.Server{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: serverName}, server); err != nil {
		slog.Warn("server-1 not found, attempting to fetch server-01")
		serverName = "server-01"
		if err := kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: serverName}, server); err != nil {
			return "", fmt.Errorf("getting server %s: %w", serverName, err)
		}
	}

	return serverName, nil
}

// Test that DNS, NTP and MTU settings for a VPC are correctly propagated to the servers.
// For DNS, we check the content of /etc/resolv.conf;
// for NTP, we check the output of timedatectl show-timesync;
// for MTU, we check the output of "ip link" on the vlan interface.
func (testCtx *VPCPeeringTestCtx) dnsNtpMtuTest(ctx context.Context) (_ bool, returnErr error) {
	// TODO: pick any server, derive other elements (i.e. vpc, hhnet params etc) from it
	serverName, err := getServer1(ctx, testCtx.kube)
	if err != nil {
		return false, err
	}
	// Get the VPC
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		return false, fmt.Errorf("getting VPC vpc-01: %w", err)
	}

	// Set DNS, NTP and MTU
	slog.Debug("Setting DNS, NTP and MTU")
	dhcpOpts := &vpcapi.VPCDHCPOptions{
		DNSServers:   []string{"1.1.1.1"},
		TimeServers:  []string{"1.1.1.1"},
		InterfaceMTU: 1400,
	}

	for _, sub := range vpc.Spec.Subnets {
		sub.DHCP = vpcapi.VPCDHCP{
			Enable:  true,
			Options: dhcpOpts,
		}
	}
	change, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil || !change {
		return false, fmt.Errorf("updating VPC vpc-01: %w", err)
	}

	defer func() {
		slog.Debug("Cleaning up")
		for _, sub := range vpc.Spec.Subnets {
			sub.DHCP = vpcapi.VPCDHCP{
				Enable:  true,
				Options: nil,
			}
		}
		_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		if err != nil {
			if returnErr == nil {
				returnErr = fmt.Errorf("updating VPC vpc-01: %w", err)
			} else {
				returnErr = errors.Join(returnErr, fmt.Errorf("updating VPC vpc-01: %w", err))
			}

			return
		}

		// Wait for convergence
		time.Sleep(5 * time.Second)
		if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
			if returnErr == nil {
				returnErr = fmt.Errorf("waiting for switches to be ready: %w", err)
			} else {
				returnErr = errors.Join(returnErr, fmt.Errorf("waiting for switches to be ready: %w", err))
			}

			return
		}
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "/opt/bin/hhnet cleanup"); err != nil {
			if returnErr == nil {
				returnErr = fmt.Errorf("cleaning up interfaces on %s: %w", serverName, err)
			} else {
				returnErr = errors.Join(returnErr, fmt.Errorf("cleaning up interfaces on %s: %w", serverName, err))
			}

			return
		}
		// TODO: ideally this would be derived rather than hardcoded (extract the code from testing.go)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "/opt/bin/hhnet bond 1001 enp2s1 enp2s2"); err != nil {
			if returnErr == nil {
				returnErr = fmt.Errorf("bonding interfaces on %s: %w", serverName, err)
			} else {
				returnErr = errors.Join(returnErr, fmt.Errorf("bonding interfaces on %s: %w", serverName, err))
			}

			return
		}
	}()

	// Wait for convergence
	time.Sleep(5 * time.Second)
	if err := WaitSwitchesReady(ctx, testCtx.kube, 1*time.Minute, 5*time.Minute); err != nil {
		return false, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Configure network interfaces on target server
	slog.Debug("Configuring network interfaces", "server", serverName)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "/opt/bin/hhnet cleanup"); err != nil {
		return false, fmt.Errorf("cleaning up interfaces on %s: %w", serverName, err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "/opt/bin/hhnet bond 1001 enp2s1 enp2s2"); err != nil {
		return false, fmt.Errorf("bonding interfaces on %s: %w", serverName, err)
	}

	// Check DNS, NTP and MTU
	slog.Debug("Checking DNS, NTP and MTU")
	var dnsFound, ntpFound, mtuFound bool
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "grep \"nameserver 1.1.1.1\" /etc/resolv.conf"); err != nil {
		slog.Error("1.1.1.1 not found in resolv.conf", "error", err)
	} else {
		dnsFound = true
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "timedatectl show-timesync | grep 1.1.1.1"); err != nil {
		slog.Error("1.1.1.1 not found in timesync", "error", err)
	} else {
		ntpFound = true
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "ip link show dev bond0.1001 | grep \"mtu 1400\""); err != nil {
		slog.Error("mtu 1400 not found in dev bond0.1001", "error", err)
	} else {
		mtuFound = true
	}
	if !dnsFound || !ntpFound || !mtuFound {
		return false, fmt.Errorf("DNS: %v, NTP: %v, MTU: %v", dnsFound, ntpFound, mtuFound) //nolint:goerr113
	}

	return false, nil
}

// Utilities and suite runners

func makeTestCtx(kube client.Client, opts SetupVPCsOpts, workDir, cacheDir string, wipeBetweenTests bool, extName string, rtOpts ReleaseTestOpts) *VPCPeeringTestCtx {
	testCtx := new(VPCPeeringTestCtx)
	testCtx.kube = kube
	testCtx.workDir = workDir
	testCtx.cacheDir = cacheDir
	testCtx.opts = opts
	testCtx.tcOpts = TestConnectivityOpts{
		WaitSwitchesReady: false,
		PingsCount:        3,
		IPerfsSeconds:     3,
		IPerfsMinSpeed:    8500,
	}
	if rtOpts.Extended {
		testCtx.tcOpts.CurlsCount = 1
		testCtx.tcOpts.IPerfsSeconds = 10
	}
	testCtx.wipeBetweenTests = wipeBetweenTests
	testCtx.extName = extName
	testCtx.hhfabBin = rtOpts.HhfabBin
	testCtx.extended = rtOpts.Extended
	testCtx.failFast = rtOpts.FailFast
	testCtx.pauseOnFail = rtOpts.PauseOnFail

	return testCtx
}

type JUnitReport struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []JUnitTestSuite `xml:"testsuite"`
}

type JUnitTestSuite struct {
	XMLName   xml.Name        `xml:"testsuite"`
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Skipped   int             `xml:"skipped,attr"`
	Time      float64         `xml:"time,attr"`
	TimeHuman time.Duration   `xml:"-"`
	TestCases []JUnitTestCase `xml:"testcase"`
}

type SkipFlags struct {
	NoVirtualSwitch bool `xml:"-"` // skip if there's any virtual switch in the vlab
	NoNamedExternal bool `xml:"-"` // skip if the named external is not present
	NoExternals     bool `xml:"-"` // skip if there are no externals
	ExtendedOnly    bool `xml:"-"` // skip if extended tests are not enabled
}

type JUnitTestCase struct {
	XMLName   xml.Name                            `xml:"testcase"`
	ClassName string                              `xml:"classname,attr"`
	Name      string                              `xml:"name,attr"`
	Time      float64                             `xml:"time,attr"`
	Failure   *Failure                            `xml:"failure,omitempty"`
	Skipped   *Skipped                            `xml:"skipped,omitempty"`
	F         func(context.Context) (bool, error) `xml:"-"` // function to run
	SkipFlags SkipFlags                           `xml:"-"` // flags to determine whether to skip the test
}

type Failure struct {
	XMLName xml.Name `xml:"failure"`
	Message string   `xml:"message,attr"`
	Type    string   `xml:"type,attr"`
}

type Skipped struct {
	XMLName xml.Name `xml:"skipped"`
	Message string   `xml:"message,attr,omitempty"`
}

func printSuiteResults(ts *JUnitTestSuite) {
	var numFailed, numSkipped, numPassed int
	slog.Info("Test suite results", "suite", ts.Name)
	for _, test := range ts.TestCases {
		if test.Skipped != nil {
			slog.Warn("SKIP", "test", test.Name, "reason", test.Skipped.Message)
			numSkipped++
		} else if test.Failure != nil {
			slog.Error("FAIL", "test", test.Name, "error", strings.Split(test.Failure.Message, "\n")[0])
			numFailed++
		} else {
			slog.Info("PASS", "test", test.Name)
			numPassed++
		}
	}
	slog.Info("Test suite summary", "tests", len(ts.TestCases), "passed", numPassed, "skipped", numSkipped, "failed", numFailed, "duration", ts.TimeHuman)
}

func pauseOnFail() {
	// pause until the user presses enter
	slog.Info("Press enter to continue...")
	var input string
	_, _ = fmt.Scanln(&input)
	slog.Info("Continuing...")
}

func doRunTests(ctx context.Context, testCtx *VPCPeeringTestCtx, ts *JUnitTestSuite) (*JUnitTestSuite, error) {
	suiteStart := time.Now()
	slog.Info("** Running test suite", "suite", ts.Name, "tests", len(ts.TestCases), "start-time", suiteStart.Format(time.RFC3339))

	// initial setup
	if err := testCtx.setupTest(ctx); err != nil {
		return ts, fmt.Errorf("%w: %w", errInitalSetup, err)
	}

	for i, test := range ts.TestCases {
		if test.Skipped != nil {
			slog.Info("SKIP", "test", test.Name, "reason", test.Skipped.Message)

			continue
		}
		slog.Info("* Running test", "test", test.Name)
		if i > 0 && testCtx.wipeBetweenTests {
			if err := testCtx.setupTest(ctx); err != nil {
				ts.TestCases[i].Failure = &Failure{
					Message: fmt.Sprintf("Failed to setupTest between tests: %s", err.Error()),
				}
				ts.Failures++
				slog.Error("FAIL", "test", test.Name, "error", fmt.Sprintf("Failed to setupTest between tests: %s", err.Error()))
				if testCtx.pauseOnFail {
					pauseOnFail()
				}
				if testCtx.failFast {
					return ts, fmt.Errorf("setupTest failed: %w", err)
				}

				continue
			}
		}
		testStart := time.Now()
		skip, err := test.F(ctx)
		ts.TestCases[i].Time = time.Since(testStart).Seconds()
		if skip {
			var skipMsg string
			if err != nil {
				skipMsg = err.Error()
			}
			ts.TestCases[i].Skipped = &Skipped{
				Message: skipMsg,
			}
			ts.Skipped++
			slog.Warn("SKIP", "test", test.Name, "reason", skipMsg)
		} else if err != nil {
			ts.TestCases[i].Failure = &Failure{
				Message: err.Error(),
			}
			ts.Failures++
			slog.Error("FAIL", "test", test.Name, "error", err.Error())
			if testCtx.pauseOnFail {
				pauseOnFail()
			}
			if testCtx.failFast {
				return ts, fmt.Errorf("test %s failed: %w", test.Name, err)
			}
		} else {
			slog.Info("PASS", "test", test.Name)
		}
	}

	ts.TimeHuman = time.Since(suiteStart).Round(time.Second)
	ts.Time = ts.TimeHuman.Seconds()
	slog.Info("** Finished test suite", "suite", ts.Name, "duration", ts.TimeHuman.String())
	printSuiteResults(ts)

	return ts, nil
}

func regexpSelection(regexes []*regexp.Regexp, invertRegex bool, suite *JUnitTestSuite) *JUnitTestSuite {
	if len(regexes) == 0 {
		return suite
	}

	for i, test := range suite.TestCases {
		matched := false
		for _, regex := range regexes {
			if regex.MatchString(test.Name) {
				matched = true

				break
			}
		}
		// we skip the test:
		// - if it matched and we are inverting the regex (match == true, invertRegex == true)
		// - if it didn't match and we are not inverting the regex (match == false, invertRegex == false)
		if matched == invertRegex {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "Regex selection",
			}
			suite.Skipped++
		}
	}

	return suite
}

func failAllTests(suite *JUnitTestSuite, err error) *JUnitTestSuite {
	for i := range suite.TestCases {
		if suite.TestCases[i].Skipped != nil {
			continue
		}
		suite.TestCases[i].Failure = &Failure{
			Message: err.Error(),
		}
		suite.Failures++
	}

	return suite
}

func selectAndRunSuite(ctx context.Context, testCtx *VPCPeeringTestCtx, suite *JUnitTestSuite, regexes []*regexp.Regexp, invertRegex bool, skipFlags SkipFlags) (*JUnitTestSuite, error) {
	suite = regexpSelection(regexes, invertRegex, suite)
	if suite.Skipped == suite.Tests {
		slog.Info("All tests in suite skipped, skipping suite", "suite", suite.Name)

		return suite, nil
	}
	for i, test := range suite.TestCases {
		if test.Skipped != nil {
			continue
		}
		if test.SkipFlags.ExtendedOnly && skipFlags.ExtendedOnly {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "Extended tests are not enabled",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoVirtualSwitch && skipFlags.NoVirtualSwitch {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are virtual switches",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoNamedExternal && skipFlags.NoNamedExternal {
			suite.TestCases[i].Skipped = &Skipped{
				Message: fmt.Sprintf("The named external (%s) is not present", testCtx.extName),
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoExternals && skipFlags.NoExternals {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are no externals",
			}
			suite.Skipped++

			continue
		}
	}

	suite, err := doRunTests(ctx, testCtx, suite)
	if err != nil {
		// We could get here because:
		// 1) the initial test setup has failed and we didn't run any tests (regardless of failFast)
		// 2) one of the tests has failed and failFast is set
		// we only return the error if we are in failFast mode
		if errors.Is(err, errInitalSetup) {
			suite = failAllTests(suite, err)
		}
		if testCtx.failFast {
			return suite, err
		}
	}

	return suite, nil
}

func makeVpcPeeringsSingleVPCSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Single VPC Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "No restrictions",
			F:    testCtx.noRestrictionsTest,
		},
		{
			Name: "Single VPC with restrictions",
			F:    testCtx.singleVPCWithRestrictionsTest,
			SkipFlags: SkipFlags{
				NoVirtualSwitch: true,
			},
		},
		{
			Name: "DNS/NTP/MTU",
			F:    testCtx.dnsNtpMtuTest,
		},
		{
			Name: "StaticExternal",
			F:    testCtx.staticExternalTest,
			SkipFlags: SkipFlags{
				NoVirtualSwitch: true,
			},
		},
		{
			Name: "MCLAG Failover",
			F:    testCtx.mclagTest,
			SkipFlags: SkipFlags{
				NoVirtualSwitch: true,
			},
		},
		{
			Name: "ESLAG Failover",
			F:    testCtx.eslagTest,
			SkipFlags: SkipFlags{
				NoVirtualSwitch: true,
			},
		},
		{
			Name: "Bundled Failover",
			F:    testCtx.bundledFailoverTest,
			SkipFlags: SkipFlags{
				NoVirtualSwitch: true,
			},
		},
		{
			Name: "Spine Failover",
			F:    testCtx.spineFailoverTest,
			SkipFlags: SkipFlags{
				NoVirtualSwitch: true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

func makeVpcPeeringsMultiVPCSuiteRun(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Multi-Subnets VPC Suite",
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
				NoVirtualSwitch: true,
			},
		},
		{
			Name: "Multi-Subnets with filtering",
			F:    testCtx.multiSubnetsSubnetFilteringTest,
			SkipFlags: SkipFlags{
				NoVirtualSwitch: true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

func makeVpcPeeringsBasicSuiteRun(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Basic VPC Peering Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Starter Test",
			F:    testCtx.vpcPeeringsStarterTest,
			SkipFlags: SkipFlags{
				NoNamedExternal: true,
			},
		},
		{
			Name: "Only Externals",
			F:    testCtx.vpcPeeringsOnlyExternalsTest,
			SkipFlags: SkipFlags{
				NoExternals: true,
			},
		},
		{
			Name: "Full Mesh All Externals",
			F:    testCtx.vpcPeeringsFullMeshAllExternalsTest,
		},
		{
			Name: "Full Loop All Externals",
			F:    testCtx.vpcPeeringsFullLoopAllExternalsTest,
		},
		{
			Name: "Sergei's Special Test",
			F:    testCtx.vpcPeeringsSergeisSpecialTest,
			SkipFlags: SkipFlags{
				NoNamedExternal: true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

func RunReleaseTestSuites(ctx context.Context, workDir, cacheDir string, rtOtps ReleaseTestOpts) error {
	testStart := time.Now()
	// TODO: make this configurable
	extName := "default"

	cacheCancel, kube, err := GetKubeClientWithCache(ctx, workDir)
	if err != nil {
		return err
	}
	defer cacheCancel()

	opts := SetupVPCsOpts{
		WaitSwitchesReady: true,
		ForceCleanup:      true,
		ServersPerSubnet:  3,
		SubnetsPerVPC:     3,
		VLANNamespace:     "default",
		IPv4Namespace:     "default",
	}

	regexesCompiled := make([]*regexp.Regexp, 0)
	for _, regex := range rtOtps.Regexes {
		compiled, err := regexp.Compile(regex)
		if err != nil {
			return fmt.Errorf("compiling regex %s: %w", regex, err)
		}
		regexesCompiled = append(regexesCompiled, compiled)
	}

	// detect if any of the skipFlags conditions are true
	skipFlags := SkipFlags{
		ExtendedOnly: rtOtps.Extended,
	}
	swList := &wiringapi.SwitchList{}
	if err := kube.List(ctx, swList, client.MatchingLabels{}); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}
	for _, sw := range swList.Items {
		if sw.Spec.Profile == meta.SwitchProfileVS {
			slog.Info("Virtual switch found", "switch", sw.Name)
			skipFlags.NoVirtualSwitch = true

			break
		}
	}
	extList := &vpcapi.ExternalList{}
	if err := kube.List(ctx, extList); err != nil {
		return fmt.Errorf("listing externals: %w", err)
	}
	if len(extList.Items) == 0 {
		slog.Info("No externals found")
		skipFlags.NoExternals = true
		skipFlags.NoNamedExternal = true
	} else {
		ext := &vpcapi.External{}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: extName}, ext); err != nil {
			slog.Info("Named External not found", "external", extName)
			skipFlags.NoNamedExternal = true
		}
	}

	singleVpcTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, false, extName, rtOtps)
	singleVpcSuite := makeVpcPeeringsSingleVPCSuite(singleVpcTestCtx)
	singleVpcResults, err := selectAndRunSuite(ctx, singleVpcTestCtx, singleVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running single VPC suite: %w", err)
	}

	opts.ServersPerSubnet = 1
	multiVpcTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, false, extName, rtOtps)
	multiVpcSuite := makeVpcPeeringsMultiVPCSuiteRun(multiVpcTestCtx)
	multiVpcResults, err := selectAndRunSuite(ctx, multiVpcTestCtx, multiVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running multi VPC suite: %w", err)
	}

	opts.SubnetsPerVPC = 1
	basicTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, true, extName, rtOtps)
	basicVpcSuite := makeVpcPeeringsBasicSuiteRun(basicTestCtx)
	basicResults, err := selectAndRunSuite(ctx, basicTestCtx, basicVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running basic VPC suite: %w", err)
	}

	slog.Info("*** Recap of the test results ***")
	printSuiteResults(singleVpcResults)
	printSuiteResults(multiVpcResults)
	printSuiteResults(basicResults)

	if rtOtps.ResultsFile != "" {
		report := JUnitReport{
			Suites: []JUnitTestSuite{*singleVpcResults, *multiVpcResults, *basicResults},
		}
		output, err := xml.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling XML: %w", err)
		}
		if err := os.WriteFile(rtOtps.ResultsFile, output, 0600); err != nil {
			return fmt.Errorf("writing XML file: %w", err)
		}
	}

	slog.Info("All tests completed", "duration", time.Since(testStart).String())
	if singleVpcResults.Failures > 0 || multiVpcResults.Failures > 0 || basicResults.Failures > 0 {
		return fmt.Errorf("some tests failed: singleVpc=%d, multiVpc=%d, basic=%d", singleVpcResults.Failures, multiVpcResults.Failures, basicResults.Failures) //nolint:goerr113
	}

	return nil
}
