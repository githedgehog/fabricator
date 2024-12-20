// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VPCPeeringsSuite struct {
	suite.Suite
	workDir          string
	cacheDir         string
	ctx              context.Context
	ctxCancel        context.CancelFunc
	kube             client.Client
	wipeBetweenTests bool
	opts             SetupVPCsOpts
	tcOpts           TestConnectivityOpts
	extName          string
}

func (suite *VPCPeeringsSuite) SetupSuite() {
	err := getEnvVars(&suite.workDir, &suite.cacheDir)
	assert.Nil(suite.T(), err)
	suite.ctx, suite.ctxCancel = context.WithTimeout(context.Background(), 45*time.Minute)
	suite.kube, err = GetKubeClient(suite.ctx, suite.workDir)
	assert.Nil(suite.T(), err)
	suite.opts = SetupVPCsOpts{
		WaitSwitchesReady: true,
		ForceCleanup:      true,
		ServersPerSubnet:  1,
		SubnetsPerVPC:     1,
		VLANNamespace:     "default",
		IPv4Namespace:     "default",
	}
	suite.tcOpts = TestConnectivityOpts{
		WaitSwitchesReady: true,
	}
	suite.extName = "default--5835"
	suite.wipeBetweenTests = true

	// NOTE: here we could do setup VPCs and reuse it throughout the tests, but
	// until the gNMI issue is fixed we will need to wipe all VPCs between tests,
	// so we will do the setup in SetupTest instead
}

// FIXME: duplicated from hhfctl package, but with the kube client passed as
// parameter to circumvent an issue
func WipeAllVPCs(ctx context.Context, kube client.Client) error {
	// delete all external peerings
	extPeers := &vpcapi.ExternalPeeringList{}
	if err := kube.List(ctx, extPeers); err != nil {
		return err
	}
	for _, extPeer := range extPeers.Items {
		if err := kube.Delete(ctx, &extPeer); err != nil {
			return err
		}
	}

	// delete all regular peerings
	peers := &vpcapi.VPCPeeringList{}
	if err := kube.List(ctx, peers); err != nil {
		return err
	}
	for _, peer := range peers.Items {
		if err := kube.Delete(ctx, &peer); err != nil {
			return err
		}
	}

	// delete all attachments
	attachments := &vpcapi.VPCAttachmentList{}
	if err := kube.List(ctx, attachments); err != nil {
		return err
	}
	for _, attach := range attachments.Items {
		if err := kube.Delete(ctx, &attach); err != nil {
			return err
		}
	}

	// delete all vpcs
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return err
	}
	for _, vpc := range vpcs.Items {
		if err := kube.Delete(ctx, &vpc); err != nil {
			return err
		}
	}

	return nil
}

func (suite *VPCPeeringsSuite) SetupTest() {
	if suite.wipeBetweenTests {
		// if err := hhfctl.VPCWipe(suite.ctx); err != nil { FIXME: this is the original call
		if err := WipeAllVPCs(suite.ctx, suite.kube); err != nil {
			suite.T().Fatalf("WipeBetweenTests: %v", err)
		}
	}

	if err := DoVLABSetupVPCs(suite.ctx, suite.workDir, suite.cacheDir, suite.opts); err != nil {
		suite.T().Fatalf("DoVLABSetupVPCs: %v", err)
	}
}

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

func getEnvVars(workDir, cacheDir *string) error {
	*workDir = os.Getenv("HHFAB_WORK_DIR")
	*cacheDir = os.Getenv("HHFAB_CACHE_DIR")

	if *workDir == "" || *cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if *workDir == "" {
			*workDir = filepath.Join(home, "hhfab")
		}
		if *cacheDir == "" {
			*cacheDir = filepath.Join(home, ".hhfab-cache")
		}
	}

	return nil
}

func populateFullMeshVpcPeerings(ctx context.Context, kube client.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec) error {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return err
	}
	for i := 0; i < len(vpcs.Items); i++ {
		for j := i + 1; j < len(vpcs.Items); j++ {
			appendVpcPeeringSpec(vpcPeerings, i+1, j+1, "", []string{}, []string{})
		}
	}
	return nil
}

func populateFullLoopVpcPeerings(ctx context.Context, kube client.Client, vpcPeerings map[string]*vpcapi.VPCPeeringSpec) error {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return err
	}
	for i := 0; i < len(vpcs.Items); i++ {
		appendVpcPeeringSpec(vpcPeerings, i+1, (i+1)%len(vpcs.Items)+1, "", []string{}, []string{})
	}
	return nil
}

func populateAllExternalVpcPeerings(ctx context.Context, kube client.Client, extPeerings map[string]*vpcapi.ExternalPeeringSpec) error {
	vpcs := &vpcapi.VPCList{}
	if err := kube.List(ctx, vpcs); err != nil {
		return err
	}
	exts := &vpcapi.ExternalList{}
	if err := kube.List(ctx, exts); err != nil {
		return err
	}
	for i := 0; i < len(vpcs.Items); i++ {
		for j := 0; j < len(exts.Items); j++ {
			appendExtPeeringSpec(extPeerings, i+1, exts.Items[j].Name, []string{"subnet-01"}, []string{})
		}
	}
	return nil
}

func (suite *VPCPeeringsSuite) TestVPCPeeringsStarter() {
	// 1+2 1+3 3+5 2+4 4+6 5+6 5~default--5835:s=subnet-01 6~default--5835:s=subnet-01  1~default--5835:s=subnet-01  2~default--5835:s=subnet-01
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 6)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 1, 3, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 3, 5, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 4, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 4, 6, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 5, 6, "", []string{}, []string{})

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 4)
	appendExtPeeringSpec(externalPeerings, 5, suite.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 6, suite.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 1, suite.extName, []string{"subnet-01"}, []string{})
	appendExtPeeringSpec(externalPeerings, 2, suite.extName, []string{"subnet-01"}, []string{})

	if err := DoSetupPeerings(suite.ctx, suite.kube, vpcPeerings, externalPeerings, true); err != nil {
		suite.T().Fatalf("DoSetupPeerings: %v", err)
	}
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func (suite *VPCPeeringsSuite) TestVPCPeeringsFullMeshAllExternals() {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 15)
	if err := populateFullMeshVpcPeerings(suite.ctx, suite.kube, vpcPeerings); err != nil {
		suite.T().Fatalf("populateFullMeshVpcPeerings: %v", err)
	}
	suite.T().Logf("VPC peerings: %v", vpcPeerings)

	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(suite.ctx, suite.kube, externalPeerings); err != nil {
		suite.T().Fatalf("populateAllExternalVpcPeerings: %v", err)
	}
	suite.T().Logf("External peerings: %v", externalPeerings)

	if err := DoSetupPeerings(suite.ctx, suite.kube, vpcPeerings, externalPeerings, true); err != nil {
		suite.T().Fatalf("DoSetupPeerings: %v", err)
	}
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func (suite *VPCPeeringsSuite) TestVPCPeeringsOnlyExternals() {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 0)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(suite.ctx, suite.kube, externalPeerings); err != nil {
		suite.T().Fatalf("populateAllExternalVpcPeerings: %v", err)
	}
	suite.T().Logf("External peerings: %v", externalPeerings)
	if err := DoSetupPeerings(suite.ctx, suite.kube, vpcPeerings, externalPeerings, true); err != nil {
		suite.T().Fatalf("DoSetupPeerings: %v", err)
	}
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func (suite *VPCPeeringsSuite) TestVpcPeeringsFullLoopAllExternals() {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 6)
	if err := populateFullLoopVpcPeerings(suite.ctx, suite.kube, vpcPeerings); err != nil {
		suite.T().Fatalf("populateFullLoopVpcPeerings: %v", err)
	}
	suite.T().Logf("VPC peerings: %v", vpcPeerings)
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	if err := populateAllExternalVpcPeerings(suite.ctx, suite.kube, externalPeerings); err != nil {
		suite.T().Fatalf("populateAllExternalVpcPeerings: %v", err)
	}
	suite.T().Logf("External peerings: %v", externalPeerings)
	if err := DoSetupPeerings(suite.ctx, suite.kube, vpcPeerings, externalPeerings, true); err != nil {
		suite.T().Fatalf("DoSetupPeerings: %v", err)
	}
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func (suite *VPCPeeringsSuite) TestVpcPeeringsSergeisSpecial() {
	// 1+2 2+3 2+4 6+5 1~default--5835:s=subnet-01
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 4)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 4, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 6, 5, "", []string{}, []string{})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 6)
	appendExtPeeringSpec(externalPeerings, 1, suite.extName, []string{"subnet-01"}, []string{})
	if err := DoSetupPeerings(suite.ctx, suite.kube, vpcPeerings, externalPeerings, true); err != nil {
		suite.T().Fatalf("DoSetupPeerings: %v", err)
	}
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func TestVPCPeeringsSuite(t *testing.T) {
	//t.Skip("No env running in CI yet")
	suite.Run(t, new(VPCPeeringsSuite))
}

func execConfigCmd(t *testing.T, workDir string, swName string, cmds ...string) error {
	cmd := exec.Command(
		"hhfab",
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
		t.Logf("Error configuring switch %s: %s", swName, err)
		t.Logf("Output: %s", string(out))
		return err
	}
	return nil
}

func execNodeCmd(t *testing.T, workDir string, nodeName string, command string) error {
	cmd := exec.Command(
		"hhfab",
		"vlab",
		"ssh",
		"-n",
		nodeName,
		command,
	)
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("Error running command %s on node %s: %s", command, nodeName, err)
		t.Logf("Output: %s", string(out))
		return err
	}
	return nil
}

func changeAgentStatus(t *testing.T, workDir string, swName string, up bool) error {
	return execNodeCmd(
		t, workDir, swName, fmt.Sprintf("sudo systemctl %s hedgehog-agent.service", map[bool]string{true: "start", false: "stop"}[up]))
}

// NOTE: shutting down the interface stops the agent, setting it back up restarts the agent
func changeSwitchPortStatus(t *testing.T, workDir string, deviceName string, nosPortName string, up bool) error {
	t.Logf("Change switch port status: %s %s %v\n", deviceName, nosPortName, up)
	if up {
		if err := execConfigCmd(
			t,
			workDir,
			deviceName,
			"configure",
			fmt.Sprintf("interface %s", nosPortName),
			"no shutdown",
		); err != nil {
			return err
		}
		if err := changeAgentStatus(t, workDir, deviceName, true); err != nil {
			return err
		}
	} else {
		if err := changeAgentStatus(t, workDir, deviceName, false); err != nil {
			return err
		}
		if err := execConfigCmd(
			t,
			workDir,
			deviceName,
			"configure",
			fmt.Sprintf("interface %s", nosPortName),
			"shutdown",
		); err != nil {
			_ = changeAgentStatus(t, workDir, deviceName, true)
			return err
		}
	}

	return nil
}

func (suite *VPCPeeringsSuite) TestMCLAG() {
	t := suite.T()
	// list connections in the fabric, filter by MC-LAG connection type
	conns := &wiringapi.ConnectionList{}
	if err := suite.kube.List(suite.ctx, conns, client.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeMCLAG}); err != nil {
		t.Fatalf("kube.List: %v", err)
	}
	assert.NotEmpty(t, conns.Items)
	for _, conn := range conns.Items {
		t.Logf("Testing MCLAG connection %s\n", conn.Name)
		assert.Len(t, conn.Spec.MCLAG.Links, 2)
		for _, link := range conn.Spec.MCLAG.Links {
			switchPort := link.Switch
			deviceName := switchPort.DeviceName()
			t.Logf("Disabling link on switch %s\n", deviceName)
			// get switch profile to find the port name in sonic-cli
			sw := &wiringapi.Switch{}
			if err := suite.kube.Get(suite.ctx, client.ObjectKey{Namespace: "default", Name: switchPort.DeviceName()}, sw); err != nil {
				t.Fatalf("kube.Get: %v", err)
			}
			profile := &wiringapi.SwitchProfile{}
			if err := suite.kube.Get(suite.ctx, client.ObjectKey{Namespace: "default", Name: sw.Spec.Profile}, profile); err != nil {
				t.Fatalf("kube.Get: %v", err)
			}
			portMap, err := profile.Spec.GetAPI2NOSPortsFor(&sw.Spec)
			if err != nil {
				t.Fatalf("GetAPI2NOSPortsFor: %v", err)
			}
			nosPortName, ok := portMap[switchPort.LocalPortName()]
			if !ok {
				t.Fatalf("Port %s not found in switch profile %s for switch %s", switchPort.LocalPortName(), profile.Name, deviceName)
			}
			// set port down (after disabling agent)
			if err := changeSwitchPortStatus(t, suite.workDir, deviceName, nosPortName, false); err != nil {
				t.Fatalf("changeSwitchPortStatus: %v", err)
			}
			// test connectivity - TODO maybe just individual pings from the servers
			if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, TestConnectivityOpts{WaitSwitchesReady: false}); err != nil {
				t.Fatalf("DoVLABTestConnectivity: %v", err)
			}
			// TODO: set other link down too and make sure that connectivity is lost
			// set port up (and then re-enable agent)
			if err := changeSwitchPortStatus(t, suite.workDir, deviceName, nosPortName, true); err != nil {
				t.Fatalf("changeSwitchPortStatus: %v", err)
			}
		}
	}
}

func (suite *VPCPeeringsSuite) TestESLAG() {
	t := suite.T()
	// list connections in the fabric, filter by ES-LAG connection type
	conns := &wiringapi.ConnectionList{}
	if err := suite.kube.List(suite.ctx, conns, client.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeESLAG}); err != nil {
		t.Fatalf("kube.List: %v", err)
	}
	assert.NotEmpty(t, conns.Items)
	for _, conn := range conns.Items {
		t.Logf("Testing ESLAG connection %s\n", conn.Name)
		assert.Len(t, conn.Spec.ESLAG.Links, 2)
		for _, link := range conn.Spec.ESLAG.Links {
			switchPort := link.Switch
			deviceName := switchPort.DeviceName()
			t.Logf("Disabling link on switch %s\n", deviceName)
			// get switch profile to find the port name in sonic-cli
			sw := &wiringapi.Switch{}
			if err := suite.kube.Get(suite.ctx, client.ObjectKey{Namespace: "default", Name: switchPort.DeviceName()}, sw); err != nil {
				t.Fatalf("kube.Get: %v", err)
			}
			profile := &wiringapi.SwitchProfile{}
			if err := suite.kube.Get(suite.ctx, client.ObjectKey{Namespace: "default", Name: sw.Spec.Profile}, profile); err != nil {
				t.Fatalf("kube.Get: %v", err)
			}
			portMap, err := profile.Spec.GetAPI2NOSPortsFor(&sw.Spec)
			if err != nil {
				t.Fatalf("GetAPI2NOSPortsFor: %v", err)
			}
			nosPortName, ok := portMap[switchPort.LocalPortName()]
			if !ok {
				t.Fatalf("Port %s not found in switch profile %s for switch %s", switchPort.LocalPortName(), profile.Name, deviceName)
			}
			// set port down (after disabling agent)
			if err := changeSwitchPortStatus(t, suite.workDir, deviceName, nosPortName, false); err != nil {
				t.Fatalf("changeSwitchPortStatus: %v", err)
			}
			// test connectivity - TODO maybe just individual pings from the servers
			if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, TestConnectivityOpts{WaitSwitchesReady: false}); err != nil {
				t.Fatalf("DoVLABTestConnectivity: %v", err)
			}
			// TODO: set other link down too and make sure that connectivity is lost
			// set port up (and then re-enable agent)
			if err := changeSwitchPortStatus(t, suite.workDir, deviceName, nosPortName, true); err != nil {
				t.Fatalf("changeSwitchPortStatus: %v", err)
			}
		}
	}
}

type VPCPeeringsMultiSubnetSuite struct {
	suite.Suite
	workDir   string
	cacheDir  string
	ctx       context.Context
	ctxCancel context.CancelFunc
	kube      client.Client
	opts      SetupVPCsOpts
	tcOpts    TestConnectivityOpts
	extName   string
}

func (suite *VPCPeeringsMultiSubnetSuite) SetupSuite() {
	var err error
	err = getEnvVars(&suite.workDir, &suite.cacheDir)
	assert.Nil(suite.T(), err)
	suite.ctx, suite.ctxCancel = context.WithTimeout(context.Background(), 45*time.Minute)
	suite.kube, err = GetKubeClient(suite.ctx, suite.workDir)
	assert.Nil(suite.T(), err)
	suite.opts = SetupVPCsOpts{
		WaitSwitchesReady: true,
		ForceCleanup:      true,
		ServersPerSubnet:  1,
		SubnetsPerVPC:     3,
		VLANNamespace:     "default",
		IPv4Namespace:     "default",
	}
	suite.tcOpts = TestConnectivityOpts{
		WaitSwitchesReady: true,
	}
	suite.extName = "default--5835"

	// These tests seem to be stable enough that we can reuse the setup between tests
	if err := WipeAllVPCs(suite.ctx, suite.kube); err != nil {
		suite.T().Fatalf("WipeBetweenTests: %v", err)
	}
	if err := DoVLABSetupVPCs(suite.ctx, suite.workDir, suite.cacheDir, suite.opts); err != nil {
		suite.T().Fatalf("DoVLABSetupVPCs: %v", err)
	}
	// full mesh vpc peering, no externals
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 3)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 1, 3, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, "", []string{}, []string{})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	if err := DoSetupPeerings(suite.ctx, suite.kube, vpcPeerings, externalPeerings, true); err != nil {
		suite.T().Fatalf("DoSetupPeerings: %v", err)
	}
}

func (suite *VPCPeeringsMultiSubnetSuite) TestMultiSubnetsNoRestrictions() {
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func (suite *VPCPeeringsMultiSubnetSuite) TestMultiSubnetsIsolation() {
	// modify vpc-01 to have isolated subnets
	vpc := &vpcapi.VPC{}
	if err := suite.kube.Get(suite.ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		suite.T().Fatalf("kube.Get: %v", err)
	}
	assert.Len(suite.T(), vpc.Spec.Subnets, 3)
	var permitList []string
	for subName, sub := range vpc.Spec.Subnets {
		if subName == "subnet-01" {
			suite.T().Logf("Isolating subnet '%s'", subName)
			sub.Isolated = pointer.To(true)
		}
		permitList = append(permitList, subName)
	}
	_, err := CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}

	// override isolation with explicit permit list
	vpc.Spec.Permit = make([][]string, 1)
	vpc.Spec.Permit[0] = make([]string, 3)
	copy(vpc.Spec.Permit[0], permitList)
	suite.T().Logf("Permitting subnets: %v", permitList)
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}

	// set restricted flags in vpc-02
	vpc2 := &vpcapi.VPC{}
	if err := suite.kube.Get(suite.ctx, client.ObjectKey{Namespace: "default", Name: "vpc-02"}, vpc2); err != nil {
		suite.T().Fatalf("kube.Get: %v", err)
	}
	assert.Len(suite.T(), vpc2.Spec.Subnets, 3)
	subnet2, ok := vpc2.Spec.Subnets["subnet-02"]
	assert.True(suite.T(), ok)
	suite.T().Logf("Restricting subnet 'subnet-02'")
	subnet2.Restricted = pointer.To(true)
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc2)
	assert.Nil(suite.T(), err)
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}

	// remove all restrictions for next tests
	suite.T().Log("Removing all restrictions")
	vpc.Spec.Permit = make([][]string, 0)
	for _, sub := range vpc.Spec.Subnets {
		sub.Isolated = pointer.To(false)
	}
	for _, sub := range vpc2.Spec.Subnets {
		sub.Restricted = pointer.To(false)
	}
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc2)
	assert.Nil(suite.T(), err)
}

func (suite *VPCPeeringsMultiSubnetSuite) TestMultiSubnetsSubnetFiltering() {
	vpcPeerings := make(map[string]*vpcapi.VPCPeeringSpec, 3)
	appendVpcPeeringSpec(vpcPeerings, 1, 2, "", []string{}, []string{})
	appendVpcPeeringSpec(vpcPeerings, 1, 3, "", []string{}, []string{"subnet-01"})
	appendVpcPeeringSpec(vpcPeerings, 2, 3, "", []string{}, []string{"subnet-02"})
	externalPeerings := make(map[string]*vpcapi.ExternalPeeringSpec, 0)
	if err := DoSetupPeerings(suite.ctx, suite.kube, vpcPeerings, externalPeerings, true); err != nil {
		suite.T().Fatalf("DoSetupPeerings: %v", err)
	}

	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func TestVPCPeeringsMultiSubnetSuite(t *testing.T) {
	//t.Skip("No env running in CI yet")
	suite.Run(t, new(VPCPeeringsMultiSubnetSuite))
}

type VPCPeeringsSingleVPCSuite struct {
	suite.Suite
	workDir   string
	cacheDir  string
	ctx       context.Context
	ctxCancel context.CancelFunc
	kube      client.Client
	opts      SetupVPCsOpts
	tcOpts    TestConnectivityOpts
	extName   string
}

func (suite *VPCPeeringsSingleVPCSuite) SetupSuite() {
	var err error
	err = getEnvVars(&suite.workDir, &suite.cacheDir)
	assert.Nil(suite.T(), err)
	suite.ctx, suite.ctxCancel = context.WithTimeout(context.Background(), 45*time.Minute)
	suite.kube, err = GetKubeClient(suite.ctx, suite.workDir)
	assert.Nil(suite.T(), err)
	suite.opts = SetupVPCsOpts{
		WaitSwitchesReady: true,
		ForceCleanup:      true,
		ServersPerSubnet:  3,
		SubnetsPerVPC:     3,
		VLANNamespace:     "default",
		IPv4Namespace:     "default",
	}
	suite.tcOpts = TestConnectivityOpts{
		WaitSwitchesReady: true,
	}
	suite.extName = "default--5835"

	// These tests seem to be stable enough that we can reuse the setup between tests
	if err := WipeAllVPCs(suite.ctx, suite.kube); err != nil {
		suite.T().Fatalf("WipeBetweenTests: %v", err)
	}
	if err := DoVLABSetupVPCs(suite.ctx, suite.workDir, suite.cacheDir, suite.opts); err != nil {
		suite.T().Fatalf("DoVLABSetupVPCs: %v", err)
	}
}

func (suite *VPCPeeringsSingleVPCSuite) TestSingleVPCNoRestrictions() {
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}
}

func (suite *VPCPeeringsSingleVPCSuite) TestSingleVPCWithRestrictions() {
	// isolate subnet-01
	vpc := &vpcapi.VPC{}
	if err := suite.kube.Get(suite.ctx, client.ObjectKey{Namespace: "default", Name: "vpc-01"}, vpc); err != nil {
		suite.T().Fatalf("kube.Get: %v", err)
	}
	assert.Len(suite.T(), vpc.Spec.Subnets, 3)
	permitList := []string{"subnet-01", "subnet-02", "subnet-03"}
	suite.T().Logf("Isolating subnet 'subnet-01'")
	subnet1, ok := vpc.Spec.Subnets["subnet-01"]
	assert.True(suite.T(), ok)
	subnet1.Isolated = pointer.To(true)
	_, err := CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}

	// set restricted flags for subnet-02
	subnet2, ok := vpc.Spec.Subnets["subnet-02"]
	assert.True(suite.T(), ok)
	suite.T().Logf("Restricting subnet 'subnet-02'")
	subnet2.Restricted = pointer.To(true)
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}

	// subnet-03 is isolated and restricted
	subnet3, ok := vpc.Spec.Subnets["subnet-03"]
	assert.True(suite.T(), ok)
	suite.T().Logf("Isolating and restricting subnet 'subnet-03'")
	subnet3.Isolated = pointer.To(true)
	subnet3.Restricted = pointer.To(true)
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}

	// override isolation with explicit permit list
	vpc.Spec.Permit = make([][]string, 1)
	vpc.Spec.Permit[0] = make([]string, 3)
	copy(vpc.Spec.Permit[0], permitList)
	suite.T().Logf("Permitting subnets: %v", permitList)
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
	if err := DoVLABTestConnectivity(suite.ctx, suite.workDir, suite.cacheDir, suite.tcOpts); err != nil {
		suite.T().Fatalf("DoVLABTestConnectivity: %v", err)
	}

	// remove all restrictions for next tests
	suite.T().Log("Removing all restrictions")
	vpc.Spec.Permit = make([][]string, 0)
	for _, sub := range vpc.Spec.Subnets {
		sub.Isolated = pointer.To(false)
		sub.Restricted = pointer.To(false)
	}
	_, err = CreateOrUpdateVpc(suite.ctx, suite.kube, vpc)
	assert.Nil(suite.T(), err)
}

func TestVPCPeeringsSingleVPCSuite(t *testing.T) {
	suite.Run(t, new(VPCPeeringsSingleVPCSuite))
}