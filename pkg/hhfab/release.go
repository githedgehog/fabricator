// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	"golang.org/x/sync/errgroup"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	waitAppliedFor = 1 * time.Minute
	waitTimeout    = 5 * time.Minute
)

var (
	errNoServers       = errors.New("no servers found")
	errNoExternals     = errors.New("no external peers found")
	errNoMclags        = errors.New("no MCLAG connections found")
	errNoEslags        = errors.New("no ESLAG connections found")
	errNoBundled       = errors.New("no bundled connections found")
	errNoUnbundled     = errors.New("no unbundled connections found")
	errNotEnoughSpines = errors.New("not enough spines found")
	errNotEnoughVPCs   = errors.New("not enough VPCs found")
	errNoRoceLeaves    = errors.New("no leaves supporting RoCE found")
	errInitialSetup    = errors.New("initial setup failed")
)

const (
	StaticExternalNH         = "172.31.255.1"
	StaticExternalIP         = "172.31.255.5"
	StaticExternalPL         = "24"
	StaticExternalDummyIface = "10.199.0.100"
)

const extName = "default"

type VPCPeeringTestCtx struct {
	workDir          string
	cacheDir         string
	kube             kclient.Client
	wipeBetweenTests bool
	opts             SetupVPCsOpts
	tcOpts           TestConnectivityOpts
	extName          string
	hhfabBin         string
	extended         bool
	failFast         bool
	pauseOnFail      bool
	roceLeaves       []string
	noSetup          bool
}

var AllZeroPrefix = []string{"0.0.0.0/0"}

// prepare for a test: wipe the fabric and then create the VPCs according to the
// options in the test context
func (testCtx *VPCPeeringTestCtx) setupTest(ctx context.Context) error {
	if testCtx.noSetup {
		// nothing to setup, but we still want to wait for the switches to be ready
		if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
			return fmt.Errorf("waiting for switches to be ready: %w", err)
		}

		return nil
	}
	if err := hhfctl.VPCWipeWithClient(ctx, testCtx.kube); err != nil {
		return fmt.Errorf("wiping fabric: %w", err)
	}
	if err := DoVLABSetupVPCs(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.opts); err != nil {
		return fmt.Errorf("setting up VPCs: %w", err)
	}
	// in case of L3 VPC mode, we need to give it time to switch to the longer lease time and switches to learn the routes
	if testCtx.opts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.opts.VPCMode == vpcapi.VPCModeL3Flat {
		time.Sleep(10 * time.Second)
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
func execConfigCmd(hhfabBin, workDir, swName string, cmds ...string) error {
	cmd := exec.Command(
		hhfabBin,
		"vlab",
		"ssh",
		"-n",
		swName,
		"-b",
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
		"-b",
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

// Like the above, but return the output.
func execNodeCmdWOutput(hhfabBin, workDir, nodeName string, command string) (string, error) {
	cmd := exec.Command(
		hhfabBin,
		"vlab",
		"ssh",
		"-n",
		nodeName,
		"-b",
		command,
	)
	cmd.Dir = workDir
	bytes, err := cmd.CombinedOutput()
	out := string(bytes)
	if err != nil {
		return out, fmt.Errorf("running command %s on node %s: %w", command, nodeName, err)
	}

	return out, nil
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
func pingFromFabricNode(hhfabBin, workDir, nodeName, ip string, sourceIP string, expectSuccess bool) error {
	cmd := exec.Command(
		hhfabBin,
		"vlab",
		"ssh",
		"-n",
		nodeName,
		"ping",
		"-c", "3",
		"-W", "1",
	)
	if sourceIP != "" {
		cmd.Args = append(cmd.Args, "-I", sourceIP)
	}
	cmd.Args = append(cmd.Args, ip)
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

// Test function types

// A revert function is a function that undoes a step taken by the test. It is meant
// to be run after the test is done, regardless of whether it succeeded or failed.
type RevertFunc func(context.Context) error

// A test function is a function that runs a test. It takes a context and returns
// a boolean indicating whether the test was skipped (e.g. due to missing resources),
// a list of revert functions to be run after the test, and an error if the test failed.
// note that the error contains the reason for the skip if the test was skipped.
type TestFunc func(context.Context) (bool, []RevertFunc, error)

// Test functions

// The starter test is presumably an arbitrary point in the space of possible VPC peering configurations.
// It was presumably chosen because going from this to a full mesh configuration could trigger
// the gNMI bug. Note that in order to reproduce it one should disable the forced cleanup between
// tests.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsStarterTest(ctx context.Context) (bool, []RevertFunc, error) {
	// 1+2:r=border 1+3 3+5 2+4 4+6 5+6 6+7 7+8 8+9  5~default--5835:s=subnet-01 6~default--5835:s=subnet-01  1~default--5835:s=subnet-01  2~default--5835:s=subnet-01  9~default--5835:s=subnet-01  7~default--5835:s=subnet-01
	// check whether border switchgroup exists
	remote := "border"
	swGroup := &wiringapi.SwitchGroup{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: remote}, swGroup); err != nil {
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
	appendExtPeeringSpec(externalPeerings, 5, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 6, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 1, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 2, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 9, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	appendExtPeeringSpec(externalPeerings, 7, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil, nil
}

// Test connectivity between all VPCs in a full mesh configuration, including all externals
// Then, remove one external peering and test connectivity again.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullMeshAllExternalsTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 15)
	if err := populateFullMeshVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, nil, fmt.Errorf("populating full mesh VPC peerings: %w", err)
	}

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, nil, fmt.Errorf("populating all external VPC peerings: %w", err)
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity: %w", err)
	}

	// bonus: remove one external to make sure we do not leek access to it, test again
	if len(externalPeerings) < 2 {
		slog.Warn("Not enough external peerings to remove one and check for leaks, skipping next step")
	} else {
		slog.Debug("Removing one external peering...")
		for key := range externalPeerings {
			delete(externalPeerings, key)

			break
		}
		if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
			return false, nil, fmt.Errorf("setting up peerings: %w", err)
		}
		if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
			return false, nil, fmt.Errorf("testing connectivity: %w", err)
		}
	}

	return false, nil, nil
}

// Test connectivity between all VPCs with no peering except of the external ones.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsOnlyExternalsTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, nil, fmt.Errorf("populating all external VPC peerings: %w", err)
	}
	if len(externalPeerings) == 0 {
		slog.Info("No external peerings found, skipping test")

		return true, nil, errNoExternals
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil, nil
}

// Test connectivity between all VPCs in a full loop configuration, including all externals.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullLoopAllExternalsTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 6)
	if err := populateFullLoopVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, nil, fmt.Errorf("populating full loop VPC peerings: %w", err)
	}
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, nil, fmt.Errorf("populating all external VPC peerings: %w", err)
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil, nil
}

// Arbitrary configuration which again was shown to occasionally trigger the gNMI bug.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsSergeisSpecialTest(ctx context.Context) (bool, []RevertFunc, error) {
	// 1+2 2+3 2+4:r=border 6+5 1~default--5835:s=subnet-01
	// check whether border switchgroup exists
	remote := "border"
	swGroup := &wiringapi.SwitchGroup{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: remote}, swGroup); err != nil {
		slog.Warn("Border switch group not found, not using remote", "error", err)
		remote = ""
	}

	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 4)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 4, remote, []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 6, 5, "", []string{}, []string{})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	appendExtPeeringSpec(externalPeerings, 1, testCtx.extName, []string{"subnet-01"}, AllZeroPrefix)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil, nil
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
			returnErr = errors.Join(returnErr, fmt.Errorf("could not enable HH agent on switch %s after %d attempts", deviceName, maxRetries)) //nolint:goerr113
		} else {
			returnErr = fmt.Errorf("could not enable HH agent on switch %s after %d attempts", deviceName, maxRetries) //nolint:goerr113
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
	for _, sw := range switches.Items {
		if sw.Spec.Role == wiringapi.SwitchRoleSpine {
			spines = append(spines, sw)
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
		if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, spine.Name, false); err != nil {
			return false, nil, fmt.Errorf("disabling HH agent: %w", err)
		}

		// look for connections that have this spine as a switch
		conns := &wiringapi.ConnectionList{}
		if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{wiringapi.ListLabelSwitch(spine.Name): "true", wiringapi.LabelConnectionType: wiringapi.ConnectionTypeFabric}); err != nil {
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
			returnErr = errors.Join(returnErr, fmt.Errorf("could not enable HH agent on switch %s after %d attempts", spine.Name, maxRetries)) //nolint:goerr113
		} else {
			returnErr = fmt.Errorf("could not enable HH agent on switch %s after %d attempts", spine.Name, maxRetries) //nolint:goerr113
		}
	}

	return false, nil, returnErr
}

// Vanilla test for VPC peering, just test connectivity without any further restriction
func (testCtx *VPCPeeringTestCtx) noRestrictionsTest(ctx context.Context) (bool, []RevertFunc, error) {
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil, nil
}

// Test VPC peering with multiple subnets and with restrictions.
// Assumes the scenario has 3 subnets for VPC vpc-01 and vpc-02.
// 1. Isolate subnet-01 in vpc-01, test connectivity
// 2. Override isolation with explicit permit list, test connectivity
// 3. Set restricted flag in subnet-02 in vpc-02, test connectivity
// 4. Remove all restrictions
func (testCtx *VPCPeeringTestCtx) multiSubnetsIsolationTest(ctx context.Context) (bool, []RevertFunc, error) {
	var returnErr error

	// modify vpc-01 to have isolated subnets
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: "vpc-01"}, vpc); err != nil {
		return false, nil, fmt.Errorf("getting VPC vpc-01: %w", err)
	}
	if len(vpc.Spec.Subnets) != 3 {
		return false, nil, fmt.Errorf("VPC vpc-01 has %d subnets, expected 3", len(vpc.Spec.Subnets)) //nolint:goerr113
	}

	// this is going to be used later, let's get it out of the way
	vpc2 := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: "vpc-02"}, vpc2); err != nil {
		return false, nil, fmt.Errorf("getting VPC vpc-02: %w", err)
	}
	if len(vpc2.Spec.Subnets) != 3 {
		return false, nil, fmt.Errorf("VPC vpc-02 has %d subnets, expected 3", len(vpc2.Spec.Subnets)) //nolint:goerr113
	}
	subnet2, ok := vpc2.Spec.Subnets["subnet-02"]
	if !ok {
		return false, nil, fmt.Errorf("subnet subnet-02 not found in VPC vpc-02") //nolint:goerr113
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
		return false, nil, fmt.Errorf("updating VPC vpc-01: %w", err)
	}
	reverts := make([]RevertFunc, 0)
	reverts = append(reverts, func(ctx context.Context) error {
		slog.Debug("Removing all restrictions")
		vpc.Spec.Permit = make([][]string, 0)
		for _, sub := range vpc.Spec.Subnets {
			sub.Isolated = pointer.To(false)
		}
		for _, sub := range vpc2.Spec.Subnets {
			sub.Restricted = pointer.To(false)
		}
		_, err1 := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
		_, err2 := CreateOrUpdateVpc(ctx, testCtx.kube, vpc2)
		if err1 != nil || err2 != nil {
			return errors.Join(err1, err2)
		}
		time.Sleep(5 * time.Second)
		if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}

		return nil
	})

	// TODO: agent generation check to ensure that the change was picked up
	// (tricky as we need to derive switch name from vpc, which involves quite a few steps)
	time.Sleep(5 * time.Second)
	if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
		returnErr = fmt.Errorf("waiting for ready: %w", err)
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
			if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
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
			if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity with restricted subnet-02: %w", err)
			}
		}
	}

	return false, reverts, returnErr
}

// Test VPC peering with multiple subnets and with subnet filtering.
// Assumes the scenario has 3 VPCs and at least 2 subnets in each VPC.
// It creates peering between all VPCs, but restricts the peering to only one subnet
// between 1-3 and 2-3. It then tests connectivity.
func (testCtx *VPCPeeringTestCtx) multiSubnetsSubnetFilteringTest(ctx context.Context) (bool, []RevertFunc, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 3)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 1, 3, "", []string{}, []string{"subnet-01"})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, "", []string{}, []string{"subnet-02"})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, nil, true); err != nil {
		return false, nil, fmt.Errorf("setting up peerings: %w", err)
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity: %w", err)
	}

	return false, nil, nil
}

// Test VPC peering with multiple subnets and with restrictions.
// Assumes the scenario has 3 subnets for VPC vpc-01.
// 1. Isolate subnet-01, test connectivity
// 2. Set restricted flag in subnet-02, test connectivity
// 3. Set both isolated and restricted flags in subnet-03, test connectivity
// 4. Override isolation with explicit permit list, test connectivity
// 5. Remove all restrictions
func (testCtx *VPCPeeringTestCtx) singleVPCWithRestrictionsTest(ctx context.Context) (bool, []RevertFunc, error) {
	var returnErr error

	// isolate subnet-01
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: "vpc-01"}, vpc); err != nil {
		return false, nil, fmt.Errorf("getting VPC vpc-01: %w", err)
	}
	if len(vpc.Spec.Subnets) != 3 {
		return false, nil, fmt.Errorf("VPC vpc-01 has %d subnets, expected 3", len(vpc.Spec.Subnets)) //nolint:goerr113
	}
	subnet1, ok := vpc.Spec.Subnets["subnet-01"]
	if !ok {
		return false, nil, errors.New("subnet subnet-01 not found in VPC vpc-01") //nolint:goerr113
	}
	subnet2, ok := vpc.Spec.Subnets["subnet-02"]
	if !ok {
		return false, nil, errors.New("subnet subnet-02 not found in VPC vpc-01") //nolint:goerr113
	}
	subnet3, ok := vpc.Spec.Subnets["subnet-03"]
	if !ok {
		return false, nil, errors.New("subnet subnet-03 not found in VPC vpc-01") //nolint:goerr113
	}
	permitList := []string{"subnet-01", "subnet-02", "subnet-03"}

	slog.Debug("Isolating subnet 'subnet-01'")
	subnet1.Isolated = pointer.To(true)
	_, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, nil, fmt.Errorf("updating VPC vpc-01: %w", err)
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
			return fmt.Errorf("updating VPC vpc-01: %w", err)
		}
		time.Sleep(5 * time.Second)
		if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}

		return nil
	})

	time.Sleep(5 * time.Second)
	if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
		returnErr = fmt.Errorf("waiting for ready: %w", err)
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
			if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
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
			if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
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
			if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
				returnErr = fmt.Errorf("waiting for ready: %w", err)
			} else if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
				returnErr = fmt.Errorf("testing connectivity: %w", err)
			}
		}
	}

	return false, reverts, returnErr
}

func (testCtx *VPCPeeringTestCtx) pingStaticExternal(sourceNode string, sourceIP string, expected bool) error {
	slog.Debug("Pinging static external next hop", "sourceNode", sourceNode, "next-hop", StaticExternalNH, "expected", expected)
	if err := pingFromFabricNode(testCtx.hhfabBin, testCtx.workDir, sourceNode, StaticExternalNH, sourceIP, expected); err != nil {
		return fmt.Errorf("ping from %s to %s: %w", sourceNode, StaticExternalNH, err)
	}
	slog.Debug("Pinging static external dummy interface", "sourceNode", sourceNode, "dummy-interface", StaticExternalDummyIface, "expected", expected)
	if err := pingFromFabricNode(testCtx.hhfabBin, testCtx.workDir, sourceNode, StaticExternalDummyIface, sourceIP, expected); err != nil {
		return fmt.Errorf("ping from %s to %s: %w", sourceNode, StaticExternalDummyIface, err)
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
	for _, c := range connList.Items {
		swName := c.Spec.Unbundled.Link.Switch.DeviceName()
		if _, ok := mclagSwitches[swName]; !ok {
			conn = &c

			break
		}
	}
	if conn == nil {
		slog.Info("No unbundled connections found that are not attached to an MCLAG switch, skipping test")

		return true, nil, errNoUnbundled
	}
	targetServer := conn.Spec.Unbundled.Link.Server.DeviceName()
	switchName := conn.Spec.Unbundled.Link.Switch.DeviceName()
	switchPortName := conn.Spec.Unbundled.Link.Switch.PortName()
	serverPortName := conn.Spec.Unbundled.Link.Server.LocalPortName()
	slog.Debug("Found unbundled connection", "connection", conn.Name, "server", targetServer, "switch", switchName, "port", switchPortName)

	// find two VPCs with at least a server attached to each, we'll need them later for testing
	vpcList := &vpcapi.VPCList{}
	if err := testCtx.kube.List(ctx, vpcList); err != nil {
		return false, nil, fmt.Errorf("listing VPCs: %w", err)
	}
	if len(vpcList.Items) < 2 {
		slog.Info("Not enough VPCs found, skipping test")

		return true, nil, errNotEnoughVPCs
	}
	var vpc1, vpc2 *vpcapi.VPC
	var server1, server2 string
	vpcAttachList := &vpcapi.VPCAttachmentList{}
	for _, vpc := range vpcList.Items {
		if err := testCtx.kube.List(ctx, vpcAttachList, kclient.MatchingLabels{wiringapi.LabelVPC: vpc.Name}); err != nil {
			return false, nil, fmt.Errorf("listing VPCAttachments for VPC %s: %w", vpc.Name, err)
		}
		for _, vpcAttach := range vpcAttachList.Items {
			conn := &wiringapi.Connection{}
			connName := vpcAttach.Spec.Connection
			if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: connName}, conn); err != nil {
				return false, nil, fmt.Errorf("getting connection %s for VPC Attach %s: %w", connName, vpcAttach.Name, err)
			}
			_, servers, _, _, _ := conn.Spec.Endpoints()
			if len(servers) != 1 {
				return false, nil, fmt.Errorf("expected 1 server for connection %s, got %d", conn.Name, len(servers)) //nolint:goerr113
			}
			if servers[0] == targetServer {
				slog.Debug("Skipping target server", "vpc", vpc.Name, "server", targetServer)

				continue
			}
			if vpc1 == nil {
				vpc1 = &vpc
				server1 = servers[0]
			} else {
				vpc2 = &vpc
				server2 = servers[0]

				break
			}
		}
	}
	if vpc1 == nil || vpc2 == nil || server1 == "" || server2 == "" {
		slog.Info("Not enough VPCs with attached servers found, skipping test")

		return true, nil, errNotEnoughVPCs
	}
	slog.Debug("Found VPCs and servers", "vpc1", vpc1.Name, "server1", server1, "vpc2", vpc2.Name, "server2", server2)

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
		if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}
		slog.Debug("Invoking hhnet cleanup on server", "server", targetServer)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, "/opt/bin/hhnet cleanup"); err != nil {
			return fmt.Errorf("cleaning up %s via hhnet: %w", targetServer, err)
		}
		slog.Debug("Configuring VLAN on server", "server", targetServer, "vlan", vlan, "port", serverPortName)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, fmt.Sprintf("/opt/bin/hhnet vlan %d %s", vlan, serverPortName)); err != nil {
			return fmt.Errorf("configuring VLAN on %s: %w", targetServer, err)
		}
		// in case of L3 VPC mode, we need to give it time to switch to the longer lease time and switches to learn the routes
		if testCtx.opts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.opts.VPCMode == vpcapi.VPCModeL3Flat {
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
		if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}

		return nil
	})

	if err := waitAgentGen(ctx, testCtx.kube, switchName, gen); err != nil {
		return false, reverts, err
	}
	if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
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
		WithinVPC: vpc1.Name,
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
	if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready: %w", err)
	}

	// Add address and default route to en2ps1 on the server
	slog.Debug("Adding address and default route to en2ps1 on the server", "server", targetServer)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, "hhnet cleanup"); err != nil {
		return false, reverts, fmt.Errorf("cleaning up server via hhnet: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, fmt.Sprintf("sudo ip addr add %s/%s dev enp2s1", StaticExternalNH, StaticExternalPL)); err != nil {
		return false, reverts, fmt.Errorf("adding address to server: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, "sudo ip link set dev enp2s1 up"); err != nil {
		return false, reverts, fmt.Errorf("setting up server interface: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, fmt.Sprintf("sudo ip route add default via %s", StaticExternalIP)); err != nil {
		return false, reverts, fmt.Errorf("adding default route to server: %w", err)
	}
	slog.Debug("Adding dummy inteface to the server", "server", targetServer, "address", fmt.Sprintf("%s/32", StaticExternalDummyIface))
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, "sudo ip link add dummy0 type dummy"); err != nil {
		return false, reverts, fmt.Errorf("adding dummy interface to server: %w", err)
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, fmt.Sprintf("sudo ip addr add %s/32 dev dummy0", StaticExternalDummyIface)); err != nil {
		return false, reverts, fmt.Errorf("adding address to dummy interface on server: %w", err)
	}
	reverts = append(reverts, func(_ context.Context) error {
		slog.Debug("Removing address and default route from en2ps1 on the server", "server", targetServer)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, fmt.Sprintf("sudo ip addr del %s/%s dev enp2s1", StaticExternalNH, StaticExternalPL)); err != nil {
			return fmt.Errorf("removing address from %s: %w", targetServer, err)
		}
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, "sudo ip link del dev dummy0"); err != nil {
			return fmt.Errorf("removing dummy interface from %s: %w", targetServer, err)
		}
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, targetServer, "hhnet cleanup"); err != nil {
			return fmt.Errorf("cleaning up %s via hhnet: %w", targetServer, err)
		}

		return nil
	})
	time.Sleep(5 * time.Second)

	// Ping the addresses from server1 which is in the static external VPC, expect success
	if err := testCtx.pingStaticExternal(server1, "", true); err != nil {
		return false, reverts, fmt.Errorf("pinging static external from %s in the SE VPC: %w", server1, err)
	}
	// Ping the addresses from server2 which is in a different VPC, expect failure
	if err := testCtx.pingStaticExternal(server2, "", false); err != nil {
		return false, reverts, fmt.Errorf("pinging static external from %s in a different VPC: %w", server2, err)
	}

	slog.Debug("Deleting static external")
	// NOTE: just changing the WithinVPC field to an empty string causes this error in the agent:
	// "failed to run agent: failed to process agent config from k8s: failed to process agent config loaded from k8s: failed to apply actions: GNMI set request failed: gnmi set request failed: rpc error: code = InvalidArgument desc = L3 Configuration exists for Interface: Ethernet0"
	// so we need to remove the whole StaticExternal config and then update it again
	if err := testCtx.kube.Delete(ctx, staticExtConn); err != nil {
		return false, reverts, fmt.Errorf("deleting static external connection %s: %w", staticExtConn.Name, err)
	}
	time.Sleep(5 * time.Second)
	if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
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
	if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
		return false, reverts, fmt.Errorf("waiting for switches to be ready: %w", err)
	}

	// Ping the addresses from server1, this should now fail
	if err := testCtx.pingStaticExternal(server1, "", false); err != nil {
		return false, reverts, fmt.Errorf("pinging static external from %s after removing VPC: %w", server1, err)
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
		sourceIP := strings.SplitN(sw.Spec.VTEPIP, "/", 2)[0]
		if err := testCtx.pingStaticExternal(sw.Name, sourceIP, true); err != nil {
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

// helper to get server-1 (might be called differently depending on the env)
func getServer1(ctx context.Context, kube kclient.Client) (string, error) {
	serverName := "server-1"
	server := &wiringapi.Server{}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: serverName}, server); err != nil {
		slog.Warn("server-1 not found, attempting to fetch server-01")
		serverName = "server-01"
		if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: serverName}, server); err != nil {
			return "", fmt.Errorf("getting server %s: %w", serverName, err)
		}
	}

	return serverName, nil
}

// check that the DHPC lease is within the expected range.
// note: tried to awk but for some reason it is not working if the pipe is passed as part of
// the ssh command string, so tokenize here. string will be in the format:
// valid_lft 3098sec preferred_lft 3098sec
func checkDHCPLease(grepString string, expectedLease int, tolerance int) error {
	tokens := strings.Split(strings.TrimLeft(grepString, " \t"), " ")
	if len(tokens) < 4 {
		return fmt.Errorf("DHCP lease string %s is too short, expected at least 4 tokens", grepString) //nolint:goerr113
	}
	stripped, found := strings.CutSuffix(tokens[1], "sec")
	if !found {
		return fmt.Errorf("DHCP lease %s does not end with 'sec'", tokens[1]) //nolint:goerr113
	}
	lease, err := strconv.Atoi(stripped)
	if err != nil {
		return fmt.Errorf("parsing DHCP lease %s: %w", stripped, err) //nolint:goerr113
	}
	if lease > expectedLease {
		return fmt.Errorf("DHCP lease %d is greater than expected %d", lease, expectedLease) //nolint:goerr113
	}
	if lease < expectedLease-tolerance {
		return fmt.Errorf("DHCP lease %d is less than expected %d (tolerance %d)", lease, expectedLease, tolerance) //nolint:goerr113
	}
	slog.Debug("DHCP lease check passed", "lease", lease, "expected", expectedLease, "tolerance", tolerance)

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

// Test that DNS, NTP, MTU and DHCP Lease settings for a VPC are correctly propagated to the servers.
// For DNS, we check the content of /etc/resolv.conf;
// for NTP, we check the output of timedatectl show-timesync;
// for MTU, we check the output of "ip link" on the vlan interface;
// for DHCP Lease, we check the output of "ip addr" on the server.
func (testCtx *VPCPeeringTestCtx) dnsNtpMtuTest(ctx context.Context) (bool, []RevertFunc, error) {
	// TODO: pick any server, derive other elements (i.e. vpc, hhnet params etc) from it
	serverName, err := getServer1(ctx, testCtx.kube)
	if err != nil {
		return false, nil, err
	}
	// Get the VPC
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: "vpc-01"}, vpc); err != nil {
		return false, nil, fmt.Errorf("getting VPC vpc-01: %w", err)
	}
	// Get the first subnet, where the target server is connected
	subnet, ok := vpc.Spec.Subnets["subnet-01"]
	if !ok {
		return false, nil, errors.New("subnet subnet-01 not found in VPC vpc-01") //nolint:goerr113
	}
	// Get the connection used to attach the server to the VPC
	conns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, conns, wiringapi.MatchingLabelsForListLabelServer(serverName)); err != nil {
		return false, nil, fmt.Errorf("listing connections for server %q: %w", serverName, err)
	}

	if len(conns.Items) == 0 {
		return false, nil, fmt.Errorf("no connections for server %q", serverName) //nolint:goerr113
	}
	if len(conns.Items) > 1 {
		return false, nil, fmt.Errorf("multiple connections for server %q", serverName) //nolint:goerr113
	}
	conn := conns.Items[0]

	// Set DNS, NTP and MTU
	slog.Debug("Setting DNS, NTP, MTU and DHCP lease time")
	l3mode := testCtx.opts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.opts.VPCMode == vpcapi.VPCModeL3Flat
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
		if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
			return fmt.Errorf("waiting for ready: %w", err)
		}
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "/opt/bin/hhnet cleanup"); err != nil {
			return fmt.Errorf("cleaning up interfaces on %s: %w", serverName, err)
		}
		netconfCmd, netconfErr := GetServerNetconfCmd(&conn, subnet.VLAN, testCtx.opts.HashPolicy)
		if netconfErr != nil {
			return fmt.Errorf("getting netconf command for server %s: %w", serverName, netconfErr)
		}
		cmd := fmt.Sprintf("/opt/bin/hhnet %s", netconfCmd)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, cmd); err != nil {
			return fmt.Errorf("bonding interfaces on %s: %w", serverName, err)
		}
		// in case of L3 VPC mode, we need to give it time to switch to the longer lease time and switches to learn the routes
		if testCtx.opts.VPCMode == vpcapi.VPCModeL3VNI || testCtx.opts.VPCMode == vpcapi.VPCModeL3Flat {
			time.Sleep(10 * time.Second)
		}

		return nil
	})

	// Wait for convergence
	time.Sleep(5 * time.Second)
	if err := WaitReady(ctx, testCtx.kube, WaitReadyOpts{AppliedFor: waitAppliedFor, Timeout: waitTimeout}); err != nil {
		return false, reverts, fmt.Errorf("waiting for ready: %w", err)
	}

	// Configure network interfaces on target server
	slog.Debug("Configuring network interfaces", "server", serverName)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, "/opt/bin/hhnet cleanup"); err != nil {
		return false, reverts, fmt.Errorf("cleaning up interfaces on %s: %w", serverName, err)
	}
	cmd := fmt.Sprintf("/opt/bin/hhnet bond 1001 %s enp2s1 enp2s2", testCtx.opts.HashPolicy)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, serverName, cmd); err != nil {
		return false, reverts, fmt.Errorf("bonding interfaces on %s: %w", serverName, err)
	}

	// Check DNS, NTP, MTU and DHCP lease
	slog.Debug("Checking DNS, NTP, MTU and DHCP lease")
	var dnsFound, ntpFound, mtuFound, leaseCheck, advRoutes bool
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

	// make sure to check the DHCP lease time after initial short lease time for L3 VPC modes
	if l3mode {
		time.Sleep(10 * time.Second)
	}

	out, leaseErr := execNodeCmdWOutput(testCtx.hhfabBin, testCtx.workDir, serverName, "ip addr show dev bond0.1001 proto 4 | grep valid_lft")
	if leaseErr != nil {
		slog.Error("failed to get lease time", "error", leaseErr)
	} else if err := checkDHCPLease(out, 1800, 120); err != nil {
		slog.Error("DHCP lease time check failed", "error", err)
	} else {
		leaseCheck = true
	}
	out, advRoutesErr := execNodeCmdWOutput(testCtx.hhfabBin, testCtx.workDir, serverName, "ip route show")
	if advRoutesErr != nil {
		slog.Error("failed to get IP routes from server", "error", advRoutesErr)
	} else if err := checkDHCPAdvRoutes(out, "9.9.9.9", subnet.Gateway, dhcpOpts.DisableDefaultRoute, l3mode, subnet.Subnet); err != nil {
		slog.Error("DHCP advertised routes check failed", "error", err)
	} else {
		advRoutes = true
	}

	if !dnsFound || !ntpFound || !mtuFound || !leaseCheck || !advRoutes {
		return false, reverts, fmt.Errorf("DNS: %v, NTP: %v, MTU: %v, DHCP lease: %v, Advertised Routes: %v", dnsFound, ntpFound, mtuFound, leaseCheck, advRoutes) //nolint:goerr113
	}

	return false, reverts, nil
}

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

func (testCtx *VPCPeeringTestCtx) roceBasicTest(ctx context.Context) (bool, []RevertFunc, error) {
	// this should never fail
	if len(testCtx.roceLeaves) == 0 {
		slog.Error("RoCE leaves not specified, skipping RoCE basic test")

		return true, nil, errNoRoceLeaves
	}
	swName := testCtx.roceLeaves[0]
	sw := &wiringapi.Switch{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: swName}, sw); err != nil {
		return false, nil, fmt.Errorf("getting switch %s: %w", swName, err)
	}
	// enable RoCE on the switch if not already enabled
	if err := setRoCE(ctx, testCtx.kube, swName, true); err != nil {
		return false, nil, fmt.Errorf("enabling RoCE on switch %s: %w", swName, err)
	}

	dscpOpts := testCtx.tcOpts
	dscpOpts.IPerfsDSCP = 24 // Mapped to traffic class 3

	slog.Debug("Clearing queue counters on switch", "switch", swName)
	if err := execConfigCmd(testCtx.hhfabBin, testCtx.workDir, swName, "clear queue counters"); err != nil {
		return false, nil, fmt.Errorf("clearing queue counters on switch %s: %w", swName, err)
	}

	slog.Debug("Testing connectivity with DSCP options", "dscpOpts", dscpOpts)
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, dscpOpts); err != nil {
		return false, nil, fmt.Errorf("testing connectivity with DSCP opts: %w", err)
	}

	// check counters on the RoCE enabled switch for UC3 traffic. they are stored as part of the switch agent status
	uc3Map := make(map[string]uint64) // map of interface name to UC3 transmit bits
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
		if uc3.TransmitBits > 0 {
			uc3Map[iface] = uc3.TransmitBits
		}
	}

	if len(uc3Map) == 0 {
		return false, nil, fmt.Errorf("no UC3 transmit bits found on switch %s", swName) //nolint:goerr113
	}
	slog.Debug("UC3 transmit bits found on switch", "switch", swName, "uc3Map", uc3Map)

	return false, nil, nil
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

// test breakout ports. for each switch in the fabric:
// 1. take the first unused breakout port (to avoiid conflicts)
// 2. change breakout to some non default mode
// 3. wait for all switches to be ready for 1 minute
// 4. check that all agents report the breakout to be completed and that the port is in the expected mode
func (testCtx *VPCPeeringTestCtx) breakoutTest(ctx context.Context) (bool, []RevertFunc, error) {
	// get all agents in the fabric
	agents := &agentapi.AgentList{}
	if err := testCtx.kube.List(ctx, agents); err != nil {
		return false, nil, fmt.Errorf("listing agents: %w", err)
	}
	g := &errgroup.Group{}
	for _, agent := range agents.Items {
		g.Go(func() error {
			// first of all, disable RoCE if it is enabled, as breakout operations are forbidden while RoCE is enabled
			if err := setRoCE(ctx, testCtx.kube, agent.Name, false); err != nil {
				return fmt.Errorf("disabling RoCE on switch %s: %w", agent.Name, err)
			}
			// get which ports are used on this switch
			conns := &wiringapi.ConnectionList{}
			if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{
				wiringapi.ListLabelSwitch(agent.Name): wiringapi.ListLabelValue,
			}); err != nil {
				return fmt.Errorf("listing connections for switch %s: %w", agent.Name, err)
			}

			usedPorts := make(map[string]bool, len(conns.Items))
			for _, conn := range conns.Items {
				_, _, connPorts, _, err := conn.Spec.Endpoints()
				if err != nil {
					return fmt.Errorf("getting endpoints for connection %s: %w", conn.Name, err)
				}
				for _, connPort := range connPorts {
					if !strings.HasPrefix(connPort, agent.Name+"/") {
						continue
					}

					portName := strings.SplitN(connPort, "/", 2)[1]
					if strings.Count(portName, "/") == 2 {
						breakoutName := portName[:strings.LastIndex(portName, "/")]
						usedPorts[breakoutName] = true
					}
					usedPorts[portName] = true
				}
			}

			// get default breakout modes for each port
			defaultBreakouts, err := agent.Spec.SwitchProfile.GetBreakoutDefaults(&agent.Spec.Switch)
			if err != nil {
				return fmt.Errorf("getting default breakouts for switch %s: %w", agent.Name, err)
			}

			// now go over all the ports in the switch profile and find the first unused port
			swProfile := &wiringapi.SwitchProfile{}
			if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: agent.Spec.Switch.Profile}, swProfile); err != nil {
				return fmt.Errorf("getting switch profile %s for switch %s: %w", agent.Spec.Switch.Profile, agent.Name, err)
			}
			var unusedPort string
			var breakoutProfile *wiringapi.SwitchProfilePortProfileBreakout
			for portName, port := range swProfile.Spec.Ports {
				if _, ok := usedPorts[portName]; !ok {
					// this port is not used, but can it be used for breakout?
					if port.Profile == "" {
						continue
					}
					portProfile, ok := swProfile.Spec.PortProfiles[port.Profile]
					if !ok {
						return fmt.Errorf("port profile %s of port %s not found in switch profile %s for switch %s: %w", port.Profile, portName, swProfile.Name, agent.Name, err)
					}
					if portProfile.Breakout == nil {
						continue
					}
					unusedPort = portName
					breakoutProfile = portProfile.Breakout
					slog.Debug("Found unused port for breakout", "port", unusedPort, "switch", agent.Name, "switchProfile", swProfile.Name)

					break
				}
			}
			if unusedPort == "" || breakoutProfile == nil {
				slog.Warn("No unused ports found on switch", "switch", agent.Name)

				return nil
			}

			// pick a random non-default supported breakout mode
			targetMode := ""
			for mode := range breakoutProfile.Supported {
				if mode != defaultBreakouts[unusedPort] {
					targetMode = mode

					break
				}
			}
			if targetMode == "" {
				slog.Warn("No non-default breakout modes found for port", "port", unusedPort, "switch", agent.Name)

				return nil
			}
			currState, exists := agent.Status.State.Breakouts[unusedPort]
			currMode := ""
			if exists {
				currMode = currState.Mode
			} else {
				currMode = defaultBreakouts[unusedPort]
			}
			// we had a bug where the breakout mode could only be set the first time, so we need to check if the current mode is different from the default
			if currMode == defaultBreakouts[unusedPort] {
				slog.Debug("Unused port is in default breakout mode, setting it to target mode", "port", unusedPort, "switch", agent.Name, "defaultMode", defaultBreakouts[unusedPort], "targetMode", targetMode)
				if err := setPortBreakout(ctx, testCtx.kube, agent.Name, unusedPort, targetMode, true); err != nil {
					// revert change anyway
					slog.Debug("Setting breakout mode failed, reverting to default", "port", unusedPort, "switch", agent.Name, "defaultMode", defaultBreakouts[unusedPort])
					_ = setPortBreakout(ctx, testCtx.kube, agent.Name, unusedPort, defaultBreakouts[unusedPort], false)

					return fmt.Errorf("setting breakout mode for port %s on switch %s: %w", unusedPort, agent.Name, err)
				}
				currMode = targetMode
			}
			// now set it to default mode again
			slog.Debug("Setting breakout mode back to default", "port", unusedPort, "switch", agent.Name, "defaultMode", defaultBreakouts[unusedPort], "currentMode", currMode)
			if err := setPortBreakout(ctx, testCtx.kube, agent.Name, unusedPort, defaultBreakouts[unusedPort], true); err != nil {
				return fmt.Errorf("setting breakout mode back to default for port %s on switch %s: %w", unusedPort, agent.Name, err)
			}
			slog.Debug("Breakout test passed for switch", "switch", agent.Name, "port", unusedPort, "mode", targetMode)

			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return false, nil, fmt.Errorf("running breakout test for switches: %w", err)
	}

	return false, nil, nil
}

// Utilities and suite runners

func makeTestCtx(kube kclient.Client, opts SetupVPCsOpts, workDir, cacheDir string, wipeBetweenTests bool, rtOpts ReleaseTestOpts, roceLeaves []string) *VPCPeeringTestCtx {
	testCtx := new(VPCPeeringTestCtx)
	testCtx.kube = kube
	testCtx.workDir = workDir
	testCtx.cacheDir = cacheDir
	testCtx.opts = opts
	testCtx.tcOpts = TestConnectivityOpts{
		WaitSwitchesReady: false,
		PingsCount:        3,
		IPerfsSeconds:     3,
		IPerfsMinSpeed:    8200,
		CurlsCount:        1,
	}
	if rtOpts.Extended {
		testCtx.tcOpts.IPerfsSeconds = 10
		testCtx.tcOpts.CurlsCount = 3
	}
	testCtx.wipeBetweenTests = wipeBetweenTests
	testCtx.extName = extName
	testCtx.hhfabBin = rtOpts.HhfabBin
	testCtx.extended = rtOpts.Extended
	testCtx.failFast = rtOpts.FailFast
	testCtx.pauseOnFail = rtOpts.PauseOnFail
	testCtx.roceLeaves = roceLeaves

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
	VirtualSwitch bool `xml:"-"` // skip if there's any virtual switch in the vlab
	NamedExternal bool `xml:"-"` // skip if the named external is not present
	NoExternals   bool `xml:"-"` // skip if there are no externals
	ExtendedOnly  bool `xml:"-"` // skip if extended tests are not enabled
	RoCE          bool `xml:"-"` // skip if RoCE is not supported by any of the leaf switches
	SubInterfaces bool `xml:"-"` // skip if subinterfaces are not supported by some of the switches

	/* Note about subinterfaces; they are required in the following cases:
	 * 1. when using VPC loopback workaround - it's applied when we have a pair of vpcs or vpc and external both attached on a switch with peering between them
	 * 2. when attaching External on a VLAN - we'll create a subinterface for it, but if no VLAN specified we'll configure on the interface itself
	 * 3. when using StaticExternal connection - same thing - if VLAN it'll be a subinterface, if no VLAN - just interface itself gets a config
	 */
}

type JUnitTestCase struct {
	XMLName   xml.Name  `xml:"testcase"`
	ClassName string    `xml:"classname,attr"`
	Name      string    `xml:"name,attr"`
	Time      float64   `xml:"time,attr"`
	Failure   *Failure  `xml:"failure,omitempty"`
	Skipped   *Skipped  `xml:"skipped,omitempty"`
	F         TestFunc  `xml:"-"` // function to run
	SkipFlags SkipFlags `xml:"-"` // flags to determine whether to skip the test
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
		if test.Skipped != nil { //nolint:gocritic
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
	slog.Warn("Test failed, pausing execution. Note that reverts might still need to apply, so if you intend to continue, please make sure to leave the environment in the same state as you found it")
	slog.Info("Press enter to continue...")
	var input string
	_, _ = fmt.Scanln(&input)
	slog.Info("Continuing...")
}

func doRunTests(ctx context.Context, testCtx *VPCPeeringTestCtx, ts *JUnitTestSuite) (*JUnitTestSuite, error) {
	suiteStart := time.Now()
	ranSomeTests := false
	slog.Info("** Running test suite", "suite", ts.Name, "tests", len(ts.TestCases), "start-time", suiteStart.Format(time.RFC3339))

	// initial setup
	if err := testCtx.setupTest(ctx); err != nil {
		slog.Error("Initial test suite setup failed", "suite", ts.Name, "error", err.Error())
		if testCtx.pauseOnFail {
			pauseOnFail()
		}

		return ts, fmt.Errorf("%w: %w", errInitialSetup, err)
	}

	prevRevertsFailed := false
	for i, test := range ts.TestCases {
		if test.Skipped != nil {
			slog.Info("SKIP", "test", test.Name, "reason", test.Skipped.Message)

			continue
		}
		slog.Info("* Running test", "test", test.Name)
		if (ranSomeTests && testCtx.wipeBetweenTests) || prevRevertsFailed {
			if err := testCtx.setupTest(ctx); err != nil {
				ts.TestCases[i].Failure = &Failure{
					Message: fmt.Sprintf("Failed to run setupTest between tests: %s", err.Error()),
				}
				ts.Failures++
				slog.Error("FAIL", "test", test.Name, "error", fmt.Sprintf("Failed to run setupTest between tests: %s", err.Error()))
				if testCtx.pauseOnFail {
					pauseOnFail()
				}
				if testCtx.failFast {
					return ts, fmt.Errorf("setupTest failed: %w", err)
				}

				continue
			}
		}
		prevRevertsFailed = false
		testStart := time.Now()
		skip, reverts, err := test.F(ctx)
		ts.TestCases[i].Time = time.Since(testStart).Seconds()
		ranSomeTests = true
		// logic is getting complex, so let's make a recap:
		// - if skip is true, we mark the test as skipped, use the error as the skip message, and nullify it
		// - if err is not nil, we mark the test as failed, use the error message as the failure message, and pause if configured to do so
		// - we then apply reverts in reverse order, and if any of them fails, we mark the test as failed, and pause (potentially a second time) if configured to do so.
		//   we also stop applying reverts at the first failure
		// - finally, if we get to the end without any errors, we log the test as passed
		if skip {
			var skipMsg string
			if err != nil {
				skipMsg = err.Error()
			} else {
				skipMsg = "Skipped by test function (unspecified reason)"
			}
			// error message is only used to convey skipping reason
			err = nil
			ts.TestCases[i].Skipped = &Skipped{
				Message: skipMsg,
			}
			ts.Skipped++
			slog.Warn("SKIP", "test", test.Name, "reason", skipMsg)
		}
		if err != nil {
			ts.TestCases[i].Failure = &Failure{
				Message: err.Error(),
			}
			ts.Failures++
			slog.Error("FAIL", "test", test.Name, "error", err.Error())
			if testCtx.pauseOnFail {
				pauseOnFail()
			}
		}
		var revertErr error
		for i := len(reverts) - 1; i >= 0; i-- {
			revertErr = reverts[i](ctx)
			if revertErr != nil {
				slog.Error("REVERT FAIL", "test", test.Name, "error", revertErr.Error())
				if err == nil {
					// the test had passed, but now we must mark it as failed
					err = revertErr
					ts.Failures++
				} else {
					// the test had failed, let's keep track of both errors in the message
					err = errors.Join(err, revertErr)
				}
				ts.TestCases[i].Failure = &Failure{
					Message: err.Error(),
				}
				prevRevertsFailed = true
				if testCtx.pauseOnFail {
					pauseOnFail()
				}

				break
			}
		}
		if err == nil && revertErr == nil {
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
		if test.SkipFlags.VirtualSwitch && skipFlags.VirtualSwitch {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are virtual switches",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NamedExternal && skipFlags.NamedExternal {
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
		if test.SkipFlags.SubInterfaces && skipFlags.SubInterfaces {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are switches that do not support subinterfaces",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.RoCE && skipFlags.RoCE {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are no switches that support RoCE",
			}
			suite.Skipped++

			continue
		}
	}
	if suite.Skipped == suite.Tests {
		slog.Info("All tests in suite skipped, skipping suite", "suite", suite.Name)

		return suite, nil
	}

	suite, err := doRunTests(ctx, testCtx, suite)
	if err != nil {
		// We could get here because:
		// 1) the initial test setup has failed and we didn't run any tests (regardless of failFast)
		// 2) one of the tests has failed and failFast is set
		// we only return the error if we are in failFast mode
		if errors.Is(err, errInitialSetup) {
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
				VirtualSwitch: true,
			},
		},
		{
			Name: "DNS/NTP/MTU/DHCP lease",
			F:    testCtx.dnsNtpMtuTest,
		},
		{
			Name: "MCLAG Failover",
			F:    testCtx.mclagTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
			},
		},
		{
			Name: "ESLAG Failover",
			F:    testCtx.eslagTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
			},
		},
		{
			Name: "Bundled Failover",
			F:    testCtx.bundledFailoverTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
			},
		},
		{
			Name: "Spine Failover",
			F:    testCtx.spineFailoverTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
			},
		},
		{
			Name: "RoCE flag and basic traffic marking",
			F:    testCtx.roceBasicTest,
			SkipFlags: SkipFlags{
				RoCE: true,
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
				VirtualSwitch: true,
			},
		},
		{
			Name: "Multi-Subnets with filtering",
			F:    testCtx.multiSubnetsSubnetFilteringTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
				SubInterfaces: true,
			},
		},
		{
			Name: "StaticExternal",
			F:    testCtx.staticExternalTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

func makeNoVpcsSuiteRun(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "No VPCs Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Breakout ports",
			F:    testCtx.breakoutTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
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
				NamedExternal: true,
				SubInterfaces: true,
			},
		},
		{
			Name: "Only Externals",
			F:    testCtx.vpcPeeringsOnlyExternalsTest,
			SkipFlags: SkipFlags{
				NoExternals:   true,
				SubInterfaces: true,
			},
		},
		{
			Name: "Full Mesh All Externals",
			F:    testCtx.vpcPeeringsFullMeshAllExternalsTest,
			SkipFlags: SkipFlags{
				SubInterfaces: true,
			},
		},
		{
			Name: "Full Loop All Externals",
			F:    testCtx.vpcPeeringsFullLoopAllExternalsTest,
			SkipFlags: SkipFlags{
				SubInterfaces: true,
			},
		},
		{
			Name: "Sergei's Special Test",
			F:    testCtx.vpcPeeringsSergeisSpecialTest,
			SkipFlags: SkipFlags{
				NamedExternal: true,
				SubInterfaces: true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

func RunReleaseTestSuites(ctx context.Context, workDir, cacheDir string, rtOtps ReleaseTestOpts) error {
	testStart := time.Now()

	cacheCancel, kube, err := getKubeClientWithCache(ctx, workDir)
	if err != nil {
		return err
	}
	defer cacheCancel()

	// figure how many servers per subnet we need to have a single VPC cover all of them,
	// given a fixed number of subnets per VPC (3)
	servers := &wiringapi.ServerList{}
	if err := kube.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}
	if len(servers.Items) == 0 {
		return errNoServers
	}
	subnetsPerVpc := 3
	serversPerSubnet := int(math.Ceil(float64(len(servers.Items)) / float64(subnetsPerVpc)))
	slog.Debug("Calculated servers per subnet for single VPC", "servers", len(servers.Items), "subnets-per-vpc", subnetsPerVpc, "servers-per-subnet", serversPerSubnet)

	opts := SetupVPCsOpts{
		WaitSwitchesReady: true,
		ForceCleanup:      true,
		ServersPerSubnet:  serversPerSubnet,
		SubnetsPerVPC:     subnetsPerVpc,
		VLANNamespace:     "default",
		IPv4Namespace:     "default",
		HashPolicy:        rtOtps.HashPolicy,
		VPCMode:           rtOtps.VPCMode,
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
	if err := kube.List(ctx, swList, kclient.MatchingLabels{}); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}
	profileMap := make(map[string]wiringapi.SwitchProfile, 0)
	roceLeaves := make([]string, 0)
	for _, sw := range swList.Items {
		// check for virtual switches
		if !skipFlags.VirtualSwitch {
			if sw.Spec.Profile == meta.SwitchProfileVS {
				slog.Info("Virtual switch found", "switch", sw.Name)
				skipFlags.VirtualSwitch = true
			}
		}
		// check for leaf switches supporting subinterfaces and/or RoCE
		if !sw.Spec.Role.IsLeaf() {
			continue
		}
		profile := &wiringapi.SwitchProfile{}
		// did we already check this profile for another leaf?
		if p, ok := profileMap[sw.Spec.Profile]; ok {
			profile = &p
		} else {
			if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: sw.Spec.Profile}, profile); err != nil {
				return fmt.Errorf("getting switch profile %s: %w", sw.Spec.Profile, err)
			}
			profileMap[sw.Spec.Profile] = *profile
		}
		if !profile.Spec.Features.Subinterfaces && !skipFlags.SubInterfaces {
			slog.Info("Subinterfaces not supported on leaf switch", "switch-profile", sw.Spec.Profile, "switch", sw.Name)
			skipFlags.SubInterfaces = true
		}
		// exclude virtual switches from RoCE check, they do not implement counters
		if profile.Spec.Features.RoCE && sw.Spec.Profile != meta.SwitchProfileVS {
			roceLeaves = append(roceLeaves, sw.Name)
		}
	}
	if len(roceLeaves) == 0 {
		slog.Info("No RoCE capable leaves found")
		skipFlags.RoCE = true
	}
	extList := &vpcapi.ExternalList{}
	if err := kube.List(ctx, extList); err != nil {
		return fmt.Errorf("listing externals: %w", err)
	}
	if len(extList.Items) == 0 {
		slog.Info("No externals found")
		skipFlags.NoExternals = true
		skipFlags.NamedExternal = true
	} else {
		ext := &vpcapi.External{}
		if err := kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: extName}, ext); err != nil {
			slog.Info("Named External not found", "external", extName)
			skipFlags.NamedExternal = true
		}
	}

	noVpcTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, true, rtOtps, roceLeaves)
	noVpcTestCtx.noSetup = true
	noVpcSuite := makeNoVpcsSuiteRun(noVpcTestCtx)
	noVpcResults, err := selectAndRunSuite(ctx, noVpcTestCtx, noVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running no VPC suite: %w", err)
	}

	singleVpcTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, false, rtOtps, roceLeaves)
	singleVpcSuite := makeVpcPeeringsSingleVPCSuite(singleVpcTestCtx)
	singleVpcResults, err := selectAndRunSuite(ctx, singleVpcTestCtx, singleVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running single VPC suite: %w", err)
	}

	opts.ServersPerSubnet = 1
	multiVpcTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, false, rtOtps, roceLeaves)
	multiVpcSuite := makeVpcPeeringsMultiVPCSuiteRun(multiVpcTestCtx)
	multiVpcResults, err := selectAndRunSuite(ctx, multiVpcTestCtx, multiVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running multi VPC suite: %w", err)
	}

	opts.SubnetsPerVPC = 1
	basicTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, true, rtOtps, roceLeaves)
	basicVpcSuite := makeVpcPeeringsBasicSuiteRun(basicTestCtx)
	basicResults, err := selectAndRunSuite(ctx, basicTestCtx, basicVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running basic VPC suite: %w", err)
	}

	slog.Info("*** Recap of the test results ***")
	printSuiteResults(noVpcResults)
	printSuiteResults(singleVpcResults)
	printSuiteResults(multiVpcResults)
	printSuiteResults(basicResults)

	if rtOtps.ResultsFile != "" {
		report := JUnitReport{
			Suites: []JUnitTestSuite{*singleVpcResults, *multiVpcResults, *basicResults, *noVpcResults},
		}
		output, err := xml.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling XML: %w", err)
		}
		if err := os.WriteFile(rtOtps.ResultsFile, output, 0o600); err != nil {
			return fmt.Errorf("writing XML file: %w", err)
		}
	}

	slog.Info("All tests completed", "duration", time.Since(testStart).String())
	if singleVpcResults.Failures > 0 || multiVpcResults.Failures > 0 || basicResults.Failures > 0 || noVpcResults.Failures > 0 {
		return fmt.Errorf("some tests failed: singleVpc=%d, multiVpc=%d, basic=%d, noVpc=%d", singleVpcResults.Failures, multiVpcResults.Failures, basicResults.Failures, noVpcResults.Failures) //nolint:goerr113
	}

	return nil
}
