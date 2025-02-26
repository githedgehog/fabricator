// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errTestRun = errors.New("test run error")
var errNoExternals = errors.New("no external peers found")
var errNoMclags = errors.New("no MCLAG connections found")
var errNoEslags = errors.New("no ESLAG connections found")
var errNoBundled = errors.New("no bundled connections found")
var errNoUnbundled = errors.New("no unbundled connections found")
var errNotEnoughSpines = errors.New("not enough spines found")

type VPCPeeringTestCtx struct {
	workDir          string
	cacheDir         string
	kube             client.Client
	wipeBetweenTests bool
	opts             SetupVPCsOpts
	tcOpts           TestConnectivityOpts
	extName          string
	hhfabBin         string
}

// prepare for a test: wipe the fabric and then create the VPCs according to the
// options in the test context
func (testCtx *VPCPeeringTestCtx) setupTest(ctx context.Context) error {
	if err := hhfctl.VPCWipeWithClient(ctx, testCtx.kube); err != nil {
		return errors.Wrap(err, "WipeBetweenTests")
	}
	if err := DoVLABSetupVPCs(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.opts); err != nil {
		return errors.Wrap(err, "DoVLABSetupVPCs")
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
		return errors.Wrapf(err, "kube.List")
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
		return errors.Wrapf(err, "kube.List")
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
		return errors.Wrapf(err, "kube.List")
	}
	exts := &vpcapi.ExternalList{}
	if err := kube.List(ctx, exts); err != nil {
		return errors.Wrapf(err, "kube.List")
	}
	for i := 0; i < len(vpcs.Items); i++ {
		for j := 0; j < len(exts.Items); j++ {
			appendExtPeeringSpec(extPeerings, i+1, exts.Items[j].Name, []string{"subnet-01"}, []string{})
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
		slog.Error("Error configuring switch", "switch", swName, "error", err)
		slog.Debug("Output of errored command", "output", string(out))

		return errors.Wrapf(err, "configuring switch %s", swName)
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
		slog.Error("Error running command", "command", command, "node", nodeName, "error", err)
		slog.Debug("Output of errored command", "output", string(out))

		return errors.Wrapf(err, "running command %s on node %s", command, nodeName)
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
	pingOK := err == nil && strings.Contains(string(out), "0% packet loss")
	if expectSuccess && !pingOK {
		slog.Error("Ping failed, expected success", "source", nodeName, "target", ip, "error", err)
		slog.Debug("Output of ping", "output", string(out))

		return errors.New("ping failed, expected success")
	} else if !expectSuccess && pingOK {
		slog.Error("Ping succeeded, expected failure", "source", nodeName, "target", ip, "error", err)

		return errors.New("ping succeeded, expected failure")
	}

	return nil
}

// Test functions

// The starter test is presumably an arbitrary point in the space of possible VPC peering configurations.
// It was presumably chosen because going from this to a full mesh configuration could trigger
// the gNMI bug. Note that in order to reproduce it one should disable the forced cleanup between
// tests.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsStarterTest(ctx context.Context) (bool, error) {
	// TODO: skip test if we're not in env-1 or env-ci-1 (decide how to deduce that)
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
		return false, errors.Wrap(err, "DoSetupPeerings")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	return false, nil
}

// Test connectivity between all VPCs in a full mesh configuration, including all externals.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullMeshAllExternalsTest(ctx context.Context) (bool, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 15)
	if err := populateFullMeshVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, errors.Wrap(err, "populateFullMeshVpcPeerings")
	}

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, errors.Wrap(err, "populateAllExternalVpcPeerings")
	}

	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, errors.Wrap(err, "DoSetupPeerings")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	return false, nil
}

// Test connectivity between all VPCs with no peering except of the external ones.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsOnlyExternalsTest(ctx context.Context) (bool, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, errors.Wrap(err, "populateAllExternalVpcPeerings")
	}
	if len(externalPeerings) == 0 {
		slog.Info("No external peerings found, skipping test")

		return true, errNoExternals
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, errors.Wrap(err, "DoSetupPeerings")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	return false, nil
}

// Test connectivity between all VPCs in a full loop configuration, including all externals.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsFullLoopAllExternalsTest(ctx context.Context) (bool, error) {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 6)
	if err := populateFullLoopVpcPeerings(ctx, testCtx.kube, vpcPeerings); err != nil {
		return false, errors.Wrap(err, "populateFullLoopVpcPeerings")
	}
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(ctx, testCtx.kube, externalPeerings); err != nil {
		return false, errors.Wrap(err, "populateAllExternalVpcPeerings")
	}
	if err := DoSetupPeerings(ctx, testCtx.kube, vpcPeerings, externalPeerings, true); err != nil {
		return false, errors.Wrap(err, "DoSetupPeerings")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	return false, nil
}

// Arbitrary configuration which again was shown to occasionally trigger the gNMI bug.
func (testCtx *VPCPeeringTestCtx) vpcPeeringsSergeisSpecialTest(ctx context.Context) (bool, error) {
	// TODO: skip test if we're not in env-1 or env-ci-1 (decide how to deduce that)
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
		return false, errors.Wrap(err, "DoSetupPeerings")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	return false, nil
}

// disable agent, shutdown port, test connectivity, enable agent, set port up
func shutDownPortAndTest(ctx context.Context, testCtx *VPCPeeringTestCtx, deviceName string, nosPortName string) error {
	// disable agent
	if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, false); err != nil {
		return errors.Wrap(err, "failed to disable agent")
	}
	defer func() {
		if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, true); err != nil {
			slog.Error("Failed to enable agent", "error", err)
		}
	}()

	// set port down
	if err := changeSwitchPortStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, nosPortName, false); err != nil {
		return errors.Wrap(err, "failed to set port down")
	}
	defer func() {
		if err := changeSwitchPortStatus(testCtx.hhfabBin, testCtx.workDir, deviceName, nosPortName, true); err != nil {
			slog.Error("Failed to set port up", "error", err)
		}
	}()

	// test connectivity
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, TestConnectivityOpts{WaitSwitchesReady: false}); err != nil {
		return errors.Wrap(err, "test connectivity failed")
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
		return false, errors.Wrap(err, "kube.List")
	}
	if len(conns.Items) == 0 {
		slog.Info("No MCLAG connections found, skipping test")

		return true, errNoMclags
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing MCLAG connection", "connection", conn.Name)
		if len(conn.Spec.MCLAG.Links) != 2 {
			return false, errors.Errorf("MCLAG connection %s has %d links, expected 2", conn.Name, len(conn.Spec.MCLAG.Links))
		}
		for _, link := range conn.Spec.MCLAG.Links {
			switchPort := link.Switch
			deviceName := switchPort.DeviceName()
			// get switch profile to find the port name in sonic-cli
			sw := &wiringapi.Switch{}
			if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: switchPort.DeviceName()}, sw); err != nil {
				return false, errors.Wrap(err, "kube.Get")
			}
			profile := &wiringapi.SwitchProfile{}
			if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: sw.Spec.Profile}, profile); err != nil {
				return false, errors.Wrap(err, "kube.Get")
			}
			portMap, err := profile.Spec.GetAPI2NOSPortsFor(&sw.Spec)
			if err != nil {
				return false, errors.Wrap(err, "GetAPI2NOSPortsFor")
			}
			nosPortName, ok := portMap[switchPort.LocalPortName()]
			if !ok {
				return false, errors.Errorf("Port %s not found in switch profile %s for switch %s", switchPort.LocalPortName(), profile.Name, deviceName)
			}
			if err := shutDownPortAndTest(ctx, testCtx, deviceName, nosPortName); err != nil {
				return false, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
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
		return false, errors.Wrap(err, "kube.List")
	}
	if len(conns.Items) == 0 {
		slog.Info("No ESLAG connections found, skipping test")

		return true, errNoEslags
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing ESLAG connection", "connection", conn.Name)
		if len(conn.Spec.ESLAG.Links) != 2 {
			return false, errors.Errorf("ESLAG connection %s has %d links, expected 2", conn.Name, len(conn.Spec.ESLAG.Links))
		}
		for _, link := range conn.Spec.ESLAG.Links {
			switchPort := link.Switch
			deviceName := switchPort.DeviceName()
			// get switch profile to find the port name in sonic-cli
			sw := &wiringapi.Switch{}
			if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: switchPort.DeviceName()}, sw); err != nil {
				return false, errors.Wrap(err, "kube.Get")
			}
			profile := &wiringapi.SwitchProfile{}
			if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: sw.Spec.Profile}, profile); err != nil {
				return false, errors.Wrap(err, "kube.Get")
			}
			portMap, err := profile.Spec.GetAPI2NOSPortsFor(&sw.Spec)
			if err != nil {
				return false, errors.Wrap(err, "GetAPI2NOSPortsFor")
			}
			nosPortName, ok := portMap[switchPort.LocalPortName()]
			if !ok {
				return false, errors.Errorf("Port %s not found in switch profile %s for switch %s", switchPort.LocalPortName(), profile.Name, deviceName)
			}
			if err := shutDownPortAndTest(ctx, testCtx, deviceName, nosPortName); err != nil {
				return false, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
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
		return false, errors.Wrap(err, "kube.List")
	}
	if len(conns.Items) == 0 {
		slog.Info("No bundled connections found, skipping test")

		return true, errNoBundled
	}
	for _, conn := range conns.Items {
		slog.Debug("Testing Bundled connection", "connection", conn.Name)
		if len(conn.Spec.Bundled.Links) < 2 {
			return false, errors.Errorf("MCLAG connection %s has %d links, expected at least 2", conn.Name, len(conn.Spec.Bundled.Links))
		}
		for _, link := range conn.Spec.Bundled.Links {
			switchPort := link.Switch
			deviceName := switchPort.DeviceName()
			// get switch profile to find the port name in sonic-cli
			sw := &wiringapi.Switch{}
			if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: switchPort.DeviceName()}, sw); err != nil {
				return false, errors.Wrap(err, "kube.Get")
			}
			profile := &wiringapi.SwitchProfile{}
			if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: sw.Spec.Profile}, profile); err != nil {
				return false, errors.Wrap(err, "kube.Get")
			}
			portMap, err := profile.Spec.GetAPI2NOSPortsFor(&sw.Spec)
			if err != nil {
				return false, errors.Wrap(err, "GetAPI2NOSPortsFor")
			}
			nosPortName, ok := portMap[switchPort.LocalPortName()]
			if !ok {
				return false, errors.Errorf("Port %s not found in switch profile %s for switch %s", switchPort.LocalPortName(), profile.Name, deviceName)
			}
			if err := shutDownPortAndTest(ctx, testCtx, deviceName, nosPortName); err != nil {
				return false, err
			}
			// TODO: set other link down too and make sure that connectivity is lost
		}
	}

	return false, nil
}

// Basic test for spine failover.
// Iterate over the spine switches (skip the first one), and shut down all links towards them.
// Test connectivity, then re-enable the links.
func (testCtx *VPCPeeringTestCtx) spineFailoverTest(ctx context.Context) (bool, error) {
	// list spines. FIXME: figure a way to filter this directly, if possible
	switches := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, switches); err != nil {
		return false, errors.Wrap(err, "kube.List")
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

	for i, spine := range spines {
		if i == 0 {
			continue
		}
		slog.Debug("Disabling links to spine", "spine", spine.Name)
		// get switch profile to find the port name in sonic-cli
		profile := &wiringapi.SwitchProfile{}
		if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: spine.Spec.Profile}, profile); err != nil {
			return false, errors.Wrap(err, "kube.Get")
		}
		portMap, err := profile.Spec.GetAPI2NOSPortsFor(&spine.Spec)
		if err != nil {
			return false, errors.Wrap(err, "GetAPI2NOSPortsFor")
		}
		// disable agent on spine
		if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, spine.Name, false); err != nil {
			return false, errors.Wrap(err, "failed to disable agent")
		}
		defer func() {
			slog.Debug("Re-enabling agent on spine", "spine", spine.Name)
			if err := changeAgentStatus(testCtx.hhfabBin, testCtx.workDir, spine.Name, true); err != nil {
				slog.Error("Failed to enable agent", "error", err)
			}
		}()

		// look for connections that have this spine as a switch
		conns := &wiringapi.ConnectionList{}
		if err := testCtx.kube.List(ctx, conns, client.MatchingLabels{wiringapi.ListLabelSwitch(spine.Name): "true", wiringapi.LabelConnectionType: wiringapi.ConnectionTypeFabric}); err != nil {
			return false, errors.Wrap(err, "kube.List")
		}
		slog.Debug(fmt.Sprintf("Found %d connections to spine %s", len(conns.Items), spine.Name))
		for _, conn := range conns.Items {
			for _, link := range conn.Spec.Fabric.Links {
				spinePort := link.Spine.LocalPortName()
				nosPortName, ok := portMap[spinePort]
				if !ok {
					return false, errors.Errorf("Port %s not found in switch profile %s for switch %s", spinePort, profile.Name, spine.Name)
				}
				if err := changeSwitchPortStatus(testCtx.hhfabBin, testCtx.workDir, spine.Name, nosPortName, false); err != nil {
					return false, errors.Wrap(err, "failed to set port down")
				}
				// XXX: do we need to set ports back up? restarting the agent should eventually take care of that
			}
		}
	}

	// wait a bit to make sure that the fabric has converged; can't rely on agents as we disabled them
	slog.Debug("Waiting 30 seconds for fabric to converge")
	time.Sleep(30 * time.Second)
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, TestConnectivityOpts{WaitSwitchesReady: false}); err != nil {
		return false, errors.Wrap(err, "test connectivity failed")
	}

	return false, nil
}

// Vanilla test for VPC peering, just test connectivity without any further restriction
func (testCtx *VPCPeeringTestCtx) noRestrictionsTest(ctx context.Context) (bool, error) {
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
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
	// FIXME: make sure to reset all changes after the test regardles of early failures
	// modify vpc-01 to have isolated subnets
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		return false, errors.Wrap(err, "kube.Get")
	}
	if len(vpc.Spec.Subnets) != 3 {
		return false, errors.Errorf("VPC vpc-01 has %d subnets, expected 3", len(vpc.Spec.Subnets))
	}

	var permitList []string
	for subName, sub := range vpc.Spec.Subnets {
		if subName == "subnet-01" {
			slog.Debug("Isolating subnet subnet-01")
			sub.Isolated = pointer.To(true)
		}
		permitList = append(permitList, subName)
	}
	_, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	// override isolation with explicit permit list
	vpc.Spec.Permit = make([][]string, 1)
	vpc.Spec.Permit[0] = make([]string, 3)
	copy(vpc.Spec.Permit[0], permitList)
	slog.Debug("Permitting subnets", "subnets", permitList)
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	// set restricted flags in vpc-02
	vpc2 := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-02"}, vpc2); err != nil {
		return false, errors.Wrap(err, "kube.Get")
	}
	if len(vpc2.Spec.Subnets) != 3 {
		return false, errors.Errorf("VPC vpc-02 has %d subnets, expected 3", len(vpc2.Spec.Subnets))
	}
	subnet2, ok := vpc2.Spec.Subnets["subnet-02"]
	if !ok {
		return false, errors.Errorf("Subnet subnet-02 not found in VPC vpc-02")
	}
	slog.Debug("Restricting subnet 'subnet-02'")
	subnet2.Restricted = pointer.To(true)
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc2)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
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
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc2)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}

	return false, nil
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
		return false, errors.Wrap(err, "DoSetupPeerings")
	}

	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
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
	// isolate subnet-01
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		return false, errors.Wrap(err, "kube.Get")
	}
	if len(vpc.Spec.Subnets) != 3 {
		return false, errors.Errorf("VPC vpc-01 has %d subnets, expected 3", len(vpc.Spec.Subnets))
	}
	permitList := []string{"subnet-01", "subnet-02", "subnet-03"}
	slog.Debug("Isolating subnet 'subnet-01'")
	subnet1, ok := vpc.Spec.Subnets["subnet-01"]
	if !ok {
		return false, errors.New("Subnet subnet-01 not found in VPC vpc-01")
	}
	subnet1.Isolated = pointer.To(true)
	_, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	// set restricted flags for subnet-02
	subnet2, ok := vpc.Spec.Subnets["subnet-02"]
	if !ok {
		return false, errors.New("Subnet subnet-02 not found in VPC vpc-01")
	}
	slog.Debug("Restricting subnet 'subnet-02'")
	subnet2.Restricted = pointer.To(true)
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	// subnet-03 is isolated and restricted
	subnet3, ok := vpc.Spec.Subnets["subnet-03"]
	if !ok {
		return false, errors.New("Subnet subnet-03 not found in VPC vpc-01")
	}
	slog.Debug("Isolating and restricting subnet 'subnet-03'")
	subnet3.Isolated = pointer.To(true)
	subnet3.Restricted = pointer.To(true)
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
	}

	// override isolation with explicit permit list
	vpc.Spec.Permit = make([][]string, 1)
	vpc.Spec.Permit[0] = make([]string, 3)
	copy(vpc.Spec.Permit[0], permitList)
	slog.Debug("Permitting subnets", "subnets", permitList)
	_, err = CreateOrUpdateVpc(ctx, testCtx.kube, vpc)
	if err != nil {
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}
	if err := DoVLABTestConnectivity(ctx, testCtx.workDir, testCtx.cacheDir, testCtx.tcOpts); err != nil {
		return false, errors.Wrap(err, "DoVLABTestConnectivity")
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
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
	}

	return false, nil
}

/* test following the manual steps to test static external attachments:
 * 1. find an unbundled connection, take note of params (server, switch, switch port, server port)
 * 2. delete the existing VPC attachement associated with the connection
 * 3. delete the connection
 * 4. create a new connection with the static external specified, using the port which is connected to the server
 * 4a. specify that the static external is within an existing vpc, i.e. vpc-01
 * 5. ssh into server, cleanup with hhfctl, then add the address specified in the static external, i.e. 172.30.50.1/24, to en2ps1 + set it up
 * 6. ssh into server and add a default route via the nexthop specified in the static external, i.e. 172.30.50.5
 * 7. ssh into a server in the specified vpc, e.g. server-1, and ping the address specified in the static external, i.e. 172.30.50.1
 * 8. add dummy interfaces within the subnets specified in the static external and ping them from server-1
 * 9. cleanup everything and restore the original state
 */
func (testCtx *VPCPeeringTestCtx) staticExternalTest(ctx context.Context) (bool, error) {
	// find an unbundled connection
	connList := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, connList, client.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeUnbundled}); err != nil {
		return false, errors.Wrap(err, "kube.List")
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

	// Get the corresponding VPCAttachment
	vpcAttList := &vpcapi.VPCAttachmentList{}
	if err := testCtx.kube.List(ctx, vpcAttList, client.MatchingLabels{wiringapi.LabelConnection: conn.Name}); err != nil {
		return false, errors.Wrap(err, "kube.List")
	}
	if len(vpcAttList.Items) != 1 {
		return false, errors.Errorf("Expected 1 VPCAttachment for connection %s, got %d", conn.Name, len(vpcAttList.Items))
	}
	vpcAtt := vpcAttList.Items[0]
	subnetName := vpcAtt.Spec.SubnetName()
	vpcName := vpcAtt.Spec.VPCName()
	slog.Debug("Found VPCAttachment", "attachment", vpcAtt.Name, "subnet", subnetName, "vpc", vpcName)
	// Get the VPCAttachment's VPC so we can extract the VLAN (for hhnet config)
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: vpcName}, vpc); err != nil {
		return false, errors.Wrap(err, "kube.Get")
	}
	vlan := vpc.Spec.Subnets[subnetName].VLAN
	slog.Debug("VLAN for VPCAttachment", "vlan", vlan)
	// Delete the VPCAttachment
	slog.Debug("Deleting VPCAttachment", "attachment", vpcAtt.Name)
	if err := testCtx.kube.Delete(ctx, &vpcAtt); err != nil {
		return false, errors.Wrap(err, "kube.Delete")
	}
	defer func() {
		newVpcAtt := &vpcapi.VPCAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vpcAtt.Name,
				Namespace: vpcAtt.Namespace,
			},
			Spec: vpcAtt.Spec,
		}
		slog.Debug("Creating VPCAttachment", "attachment", newVpcAtt.Name)
		if err := testCtx.kube.Create(ctx, newVpcAtt); err != nil {
			slog.Error("Error creating VPCAttachment", "error", err)
		}
		// Unsure if this is needed
		slog.Debug("Waiting 5 seconds")
		time.Sleep(5 * time.Second)
		if err := WaitSwitchesReady(ctx, testCtx.kube, 0, 5*time.Minute); err != nil {
			slog.Error("Error waiting for switches to be ready", "error", err)
		}
		slog.Debug("Invoking hhnet cleanup on server", "server", server)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "/opt/bin/hhnet cleanup"); err != nil {
			slog.Error("Error cleaning up server via hhnet", "error", err)
		}
		slog.Debug("Configuring VLAN on server", "server", server, "vlan", vlan, "port", serverPortName)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, fmt.Sprintf("/opt/bin/hhnet vlan %d %s", vlan, serverPortName)); err != nil {
			slog.Error("Error setting VLAN on server", "error", err)
		}
		slog.Debug("All state restored")
	}()

	slog.Debug("Deleting connection", "connection", conn.Name)
	if err := testCtx.kube.Delete(ctx, &conn); err != nil {
		return false, errors.Wrap(err, "kube.Delete")
	}

	defer func() {
		newConn := &wiringapi.Connection{
			ObjectMeta: metav1.ObjectMeta{
				Name:      conn.Name,
				Namespace: conn.Namespace,
			},
			Spec: conn.Spec,
		}
		slog.Debug("Creating connection", "connection", newConn.Name)
		if err := testCtx.kube.Create(ctx, newConn); err != nil {
			slog.Error("Error creating connection", "error", err)
		}
		if err := WaitSwitchesReady(ctx, testCtx.kube, 0, 5*time.Minute); err != nil {
			slog.Error("Error waiting for switches to be ready", "error", err)
		}
	}()

	// If we do not wait, sometimes the agent thinks it has converged because it has not seen the changes yet
	slog.Debug("Waiting 5 seconds")
	time.Sleep(5 * time.Second)
	// Wait for convergence as a workaround for the "Tagged VLANs:1003 configuration exists on interface Ethernet1" error
	if err := WaitSwitchesReady(ctx, testCtx.kube, 0, 5*time.Minute); err != nil {
		return false, errors.Wrap(err, "WaitSwitchesReady")
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
				IP:           "172.30.50.5/24",
				Subnets:      []string{"10.99.0.0/24", "10.199.0.100/32"},
				NextHop:      "172.30.50.1",
			},
		},
	}
	slog.Debug("Creating connection", "connection", staticExtConn.Name)
	if err := testCtx.kube.Create(ctx, staticExtConn); err != nil {
		return false, errors.Wrap(err, "kube.Create")
	}
	defer func() {
		slog.Debug("Deleting connection", "connection", staticExtConn.Name)
		if err := testCtx.kube.Delete(ctx, staticExtConn); err != nil {
			slog.Error("Error deleting connection", "error", err)
		}
	}()

	slog.Debug("Waiting 5 seconds")
	time.Sleep(5 * time.Second)
	if err := WaitSwitchesReady(ctx, testCtx.kube, 0, 5*time.Minute); err != nil {
		return false, errors.Wrap(err, "WaitSwitchesReady")
	}

	// Add address and default route to en2ps1 on the server
	slog.Debug("Adding address and default route to en2ps1 on the server", "server", server)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "hhnet cleanup"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip addr add 172.30.50.1/24 dev enp2s1"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip link set dev enp2s1 up"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip route add default via 172.30.50.5"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}
	slog.Debug("Adding dummy inteface with address 10.199.0.100/32 to the server", "server", server)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip link add dummy0 type dummy"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip addr add 10.199.0.100/32 dev dummy0"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}
	defer func() {
		slog.Debug("Removing address and default route from en2ps1 on the server", "server", server)
		_ = execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip addr del 172.30.50.1/24 dev enp2s1")
		_ = execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "sudo ip link del dev dummy0")
		_ = execNodeCmd(testCtx.hhfabBin, testCtx.workDir, server, "hhnet cleanup")
	}()

	// This could probably be removed
	slog.Debug("Waiting 5 seconds")
	time.Sleep(5 * time.Second)

	// Ping the addresses from server-1 (FIXME: ensure server is not server-1, although with our current setup it can't be)
	slog.Debug("Pinging 172.30.5.1 from server-1")
	if err := pingFromServer(testCtx.hhfabBin, testCtx.workDir, "server-1", "172.30.50.1", true); err != nil {
		return false, errors.Wrap(err, "ping from server-1 to 172.30.5.1")
	}
	slog.Debug("Pinging 10.199.0.100 from server-1")
	if err := pingFromServer(testCtx.hhfabBin, testCtx.workDir, "server-1", "10.199.0.100", true); err != nil {
		return false, errors.Wrap(err, "ping from server-1 to 10.199.0.100")
	}
	slog.Debug("All good, cleaning up")

	return false, nil
}

// Test that DNS, NTP and MTU settings for a VPC are correctly propagated to the servers.
// For DNS, we check the content of /etc/resolv.conf;
// for NTP, we check the output of timedatectl show-timesync;
// for MTU, we check the output of "ip link" on the vlan interface.
func (testCtx *VPCPeeringTestCtx) dnsNtpMtuTest(ctx context.Context) (bool, error) {
	// Get the VPC
	vpc := &vpcapi.VPC{}
	if err := testCtx.kube.Get(ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		return false, errors.Wrap(err, "kube.Get")
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
		return false, errors.Wrap(err, "CreateOrUpdateVpc")
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
			slog.Error("Error cleaning up", "error", err)

			return
		}

		// Wait for convergence
		time.Sleep(5 * time.Second)
		if err := WaitSwitchesReady(ctx, testCtx.kube, 0, 5*time.Minute); err != nil {
			slog.Error("Error waiting for switches to be ready", "error", err)

			return
		}
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, "server-1", "/opt/bin/hhnet cleanup"); err != nil {
			slog.Error("Error cleaning up server-1", "error", err)
		}
		// FIXME: ideally this would be derived rather than hardcoded (extract the code from testing.go)
		if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, "server-1", "/opt/bin/hhnet bond 1001 enp2s1 enp2s2"); err != nil {
			slog.Error("Error bonding interfaces on server-1", "error", err)
		}
	}()

	// Wait for convergence
	time.Sleep(5 * time.Second)
	if err := WaitSwitchesReady(ctx, testCtx.kube, 0, 5*time.Minute); err != nil {
		return false, errors.Wrap(err, "WaitSwitchesReady")
	}

	// Configure network on server-1
	slog.Debug("Configuring network on server-1")
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, "server-1", "/opt/bin/hhnet cleanup"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}
	// FIXME: ideally this would be derived rather than hardcoded (extract the code from testing.go)
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, "server-1", "/opt/bin/hhnet bond 1001 enp2s1 enp2s2"); err != nil {
		return false, errors.Wrap(err, "execNodeCmd")
	}

	// Check DNS, NTP and MTU
	slog.Debug("Checking DNS, NTP and MTU")
	var dnsFound, ntpFound, mtuFound bool
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, "server-1", "grep \"nameserver 1.1.1.1\" /etc/resolv.conf"); err != nil {
		slog.Error("1.1.1.1 not found in resolv.conf", "error", err)
	} else {
		dnsFound = true
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, "server-1", "timedatectl show-timesync | grep 1.1.1.1"); err != nil {
		slog.Error("1.1.1.1 not found in timesync", "error", err)
	} else {
		ntpFound = true
	}
	if err := execNodeCmd(testCtx.hhfabBin, testCtx.workDir, "server-1", "ip link show dev bond0.1001 | grep \"mtu 1400\""); err != nil {
		slog.Error("mtu 1400 not found in dev bond0.1001", "error", err)
	} else {
		mtuFound = true
	}
	if !dnsFound || !ntpFound || !mtuFound {
		return false, errors.Wrapf(errTestRun, "DNS: %v, NTP: %v, MTU: %v", dnsFound, ntpFound, mtuFound)
	}

	return false, nil
}

// Utilities and suite runners

func makeTestCtx(kube client.Client, opts SetupVPCsOpts, workDir, cacheDir string, wipeBetweenTests bool, extName, hhfabBin string) *VPCPeeringTestCtx {
	testCtx := new(VPCPeeringTestCtx)
	testCtx.kube = kube
	testCtx.workDir = workDir
	testCtx.cacheDir = cacheDir
	testCtx.opts = opts
	testCtx.tcOpts = TestConnectivityOpts{WaitSwitchesReady: true}
	testCtx.wipeBetweenTests = wipeBetweenTests
	testCtx.extName = extName
	testCtx.hhfabBin = hhfabBin

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
	TestCases []JUnitTestCase `xml:"testcase"`
}

type JUnitTestCase struct {
	XMLName   xml.Name                            `xml:"testcase"`
	ClassName string                              `xml:"classname,attr"`
	Name      string                              `xml:"name,attr"`
	Time      float64                             `xml:"time,attr"`
	Failure   *Failure                            `xml:"failure,omitempty"`
	Skipped   *Skipped                            `xml:"skipped,omitempty"`
	F         func(context.Context) (bool, error) `xml:"-"` // function to run
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
	slog.Info(fmt.Sprintf("Results for %s:", ts.Name))
	for _, test := range ts.TestCases {
		if test.Skipped != nil {
			slog.Info(fmt.Sprintf("SKIP %s", test.Name), "skip-message", test.Skipped.Message)
			numSkipped++
		} else if test.Failure != nil {
			slog.Warn(fmt.Sprintf("FAIL %s", test.Name), "error", strings.Split(test.Failure.Message, "\n")[0])
			numFailed++
		} else {
			slog.Info(fmt.Sprintf("PASS %s", test.Name))
			numPassed++
		}
	}
	slog.Info(fmt.Sprintf("Total tests: %d, Passed: %d, Skipped: %d, Failed: %d, Time: %fs", len(ts.TestCases), numPassed, numSkipped, numFailed, ts.Time))
}

func doRunTests(ctx context.Context, testCtx *VPCPeeringTestCtx, ts *JUnitTestSuite) (*JUnitTestSuite, error) {
	suiteStart := time.Now()
	slog.Info(fmt.Sprintf("Running %s with %d tests", ts.Name, len(ts.TestCases)), "start-time", suiteStart.Format(time.RFC3339))

	// initial setup
	if err := testCtx.setupTest(ctx); err != nil {
		return ts, errors.Wrap(err, "failed initial setupTest")
	}

	for i, test := range ts.TestCases {
		if test.Skipped != nil {
			slog.Info(fmt.Sprintf("Skipping test %s: %s", test.Name, test.Skipped.Message))

			continue
		}
		slog.Info(fmt.Sprintf("Running test %s", test.Name))
		if i > 0 && testCtx.wipeBetweenTests {
			if err := testCtx.setupTest(ctx); err != nil {
				ts.TestCases[i].Failure = &Failure{
					Message: fmt.Sprintf("Failed to setupTest between tests: %s", err.Error()),
				}
				ts.Failures++

				continue
			}
		}
		testStart := time.Now()
		skip, err := test.F(ctx)
		if skip {
			var skipMsg string
			if err != nil {
				skipMsg = err.Error()
			}
			ts.TestCases[i].Skipped = &Skipped{
				Message: skipMsg,
			}
			ts.Skipped++
		} else if err != nil {
			ts.TestCases[i].Failure = &Failure{
				Message: err.Error(),
			}
			ts.Failures++
		}
		ts.TestCases[i].Time = time.Since(testStart).Seconds()
	}

	tsDuration := time.Since(suiteStart)
	ts.Time = tsDuration.Seconds()
	slog.Info(fmt.Sprintf("Finished %s, took %s", ts.Name, tsDuration.String()))
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
				Message: "Skipped by regex selection",
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

func selectAndRunSuite(ctx context.Context, testCtx *VPCPeeringTestCtx, suite *JUnitTestSuite, regexes []*regexp.Regexp, invertRegex bool) *JUnitTestSuite {
	suite = regexpSelection(regexes, invertRegex, suite)
	if suite.Skipped == suite.Tests {
		slog.Info("All tests in suite skipped, skipping suite", "suite", suite.Name)

		return suite
	}

	suite, err := doRunTests(ctx, testCtx, suite)
	if err != nil {
		return failAllTests(suite, err)
	}

	return suite
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
		},
		{
			Name: "DNS/NTP/MTU",
			F:    testCtx.dnsNtpMtuTest,
		},
		{
			Name: "StaticExternal",
			F:    testCtx.staticExternalTest,
		},
		{
			Name: "MCLAG Failover",
			F:    testCtx.mclagTest,
		},
		{
			Name: "ESLAG Failover",
			F:    testCtx.eslagTest,
		},
		{
			Name: "Bundled Failover",
			F:    testCtx.bundledFailoverTest,
		},
		{
			Name: "Spine Failover",
			F:    testCtx.spineFailoverTest,
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
		},
		{
			Name: "Multi-Subnets with filtering",
			F:    testCtx.multiSubnetsSubnetFilteringTest,
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
		},
		{
			Name: "Only Externals",
			F:    testCtx.vpcPeeringsOnlyExternalsTest,
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
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

func RunReleaseTestSuites(ctx context.Context, workDir, cacheDir string, rtOtps ReleaseTestOpts) error {
	// FIXME: make this configurable
	extName := "default"

	kube, err := GetKubeClient(ctx, workDir)
	if err != nil {
		return err
	}

	opts := SetupVPCsOpts{
		WaitSwitchesReady: true,
		ForceCleanup:      true,
		ServersPerSubnet:  3,
		SubnetsPerVPC:     3,
		VLANNamespace:     "default",
		IPv4Namespace:     "default",
	}

	var regexesCompiled []*regexp.Regexp
	for _, regex := range rtOtps.Regexes {
		compiled, err := regexp.Compile(regex)
		if err != nil {
			return errors.Wrap(err, "regexp.Compile")
		}
		regexesCompiled = append(regexesCompiled, compiled)
	}

	singleVpcTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, false, extName, rtOtps.HhfabBin)
	singleVpcSuite := makeVpcPeeringsSingleVPCSuite(singleVpcTestCtx)
	singleVpcResults := selectAndRunSuite(ctx, singleVpcTestCtx, singleVpcSuite, regexesCompiled, rtOtps.InvertRegex)

	opts.ServersPerSubnet = 1
	multiVpcTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, false, extName, rtOtps.HhfabBin)
	multiVpcSuite := makeVpcPeeringsMultiVPCSuiteRun(multiVpcTestCtx)
	multiVpcResults := selectAndRunSuite(ctx, multiVpcTestCtx, multiVpcSuite, regexesCompiled, rtOtps.InvertRegex)

	opts.SubnetsPerVPC = 1
	basicTestCtx := makeTestCtx(kube, opts, workDir, cacheDir, true, extName, rtOtps.HhfabBin)
	basicVpcSuite := makeVpcPeeringsBasicSuiteRun(basicTestCtx)
	basicResults := selectAndRunSuite(ctx, basicTestCtx, basicVpcSuite, regexesCompiled, rtOtps.InvertRegex)

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
			return errors.Wrapf(err, "xml.MarshalIndent")
		}
		if err := os.WriteFile(rtOtps.ResultsFile, output, 0600); err != nil {
			return errors.Wrapf(err, "os.WriteFile")
		}
	}

	return nil
}
