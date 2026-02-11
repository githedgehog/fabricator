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
	"regexp"
	"strings"
	"time"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabricatorapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	"golang.org/x/term"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	waitAppliedFor = 15 * time.Second
	waitTimeout    = 5 * time.Minute
)

var (
	errNoExternals     = errors.New("no external peers found")
	errNoMclags        = errors.New("no MCLAG connections found")
	errNoEslags        = errors.New("no ESLAG connections found")
	errNoBundled       = errors.New("no bundled connections found")
	errNoUnbundled     = errors.New("no unbundled connections found")
	errNotEnoughSpines = errors.New("not enough spines found")
	errNotEnoughLeaves = errors.New("not enough leaves found")
	errNotEnoughVPCs   = errors.New("not enough VPCs found")
	errNoRoceLeaves    = errors.New("no leaves supporting RoCE found")
	errInitialSetup    = errors.New("initial setup failed")
)

type VPCPeeringTestCtx struct {
	vlabCfg          *Config
	vlab             *VLAB
	kube             kclient.Client
	wipeBetweenTests bool
	setupOpts        SetupVPCsOpts
	tcOpts           TestConnectivityOpts
	wrOpts           WaitReadyOpts
	extName          string
	extended         bool
	failFast         bool
	pauseOnFail      bool
	roceLeaves       []string
	noSetup          bool
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

// Utilities and suite runners

func makeTestCtx(kube kclient.Client, setupOpts SetupVPCsOpts, vlabCfg *Config, vlab *VLAB, wipeBetweenTests bool, rtOpts ReleaseTestOpts) *VPCPeeringTestCtx {
	testCtx := new(VPCPeeringTestCtx)
	testCtx.kube = kube
	testCtx.vlabCfg = vlabCfg
	testCtx.vlab = vlab
	testCtx.setupOpts = setupOpts
	testCtx.tcOpts = TestConnectivityOpts{
		WaitSwitchesReady: false,
		PingsCount:        5,
		IPerfsSeconds:     3,
		IPerfsMinSpeed:    10000, // Temporarily increased to induce failure for diagnostics debugging
		CurlsCount:        1,
		RequireAllServers: setupOpts.VPCMode == vpcapi.VPCModeL2VNI, // L3VNI will skip eslag servers
	}
	testCtx.wrOpts = WaitReadyOpts{
		AppliedFor: waitAppliedFor,
		Timeout:    waitTimeout,
	}
	if rtOpts.Extended {
		testCtx.tcOpts.IPerfsSeconds = 10
		testCtx.tcOpts.CurlsCount = 3
	}
	testCtx.wipeBetweenTests = wipeBetweenTests
	testCtx.extended = rtOpts.Extended
	testCtx.failFast = rtOpts.FailFast
	testCtx.pauseOnFail = rtOpts.PauseOnFailure

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
	NoExternals   bool `xml:"-"` // skip if there are no externals
	ExtendedOnly  bool `xml:"-"` // skip if extended tests are not enabled
	RoCE          bool `xml:"-"` // skip if RoCE is not supported by any of the leaf switches
	SubInterfaces bool `xml:"-"` // skip if subinterfaces are not supported by some of the switches
	NoFabricLink  bool `xml:"-"` // skip if there's no fabric (i.e. spine-leaf) link between the switches
	NoMeshLink    bool `xml:"-"` // skip if there's no mesh (i.e. leaf-leaf) link between the switches
	NoGateway     bool `xml:"-"` // skip if gateway is not enabled or no gateways available
	NoLoki        bool `xml:"-"` // skip if Loki is not configured or available
	NoProm        bool `xml:"-"` // skip if Prometheus is not configured or available
	NoServers     bool `xml:"-"` // skip if there are no servers in the fabric

	/* Note about subinterfaces; they are required in the following cases:
	 * 1. when using VPC loopback workaround - it's applied when we have a pair of vpcs or vpc and external both attached on a switch with peering between them
	 * 2. when attaching External on a VLAN - we'll create a subinterface for it, but if no VLAN specified we'll configure on the interface itself
	 * 3. when using StaticExternal connection - same thing - if VLAN it'll be a subinterface, if no VLAN - just interface itself gets a config
	 */
}

func (sf *SkipFlags) PrettyPrint() string {
	var parts []string
	if sf.VirtualSwitch {
		parts = append(parts, "VS")
	}
	if sf.NoExternals {
		parts = append(parts, "NoExt")
	}
	if sf.ExtendedOnly {
		parts = append(parts, "EO")
	}
	if sf.RoCE {
		parts = append(parts, "RoCE")
	}
	if sf.SubInterfaces {
		parts = append(parts, "SubIf")
	}
	if sf.NoFabricLink {
		parts = append(parts, "NoFab")
	}
	if sf.NoMeshLink {
		parts = append(parts, "NoMesh")
	}
	if sf.NoGateway {
		parts = append(parts, "NoGW")
	}
	if sf.NoServers {
		parts = append(parts, "NoSrvs")
	}
	if sf.NoLoki {
		parts = append(parts, "NoLoki")
	}
	if sf.NoProm {
		parts = append(parts, "NoProm")
	}
	if len(parts) == 0 {
		return "None"
	}

	return strings.Join(parts, ", ")
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

func printTestSuite(ts *JUnitTestSuite) {
	slog.Info("*** Test suite", "suite", ts.Name, "tests", ts.Tests)
	for _, test := range ts.TestCases {
		slog.Info("* Test", "name", test.Name, "skipFlags", test.SkipFlags.PrettyPrint())
	}
}

func printSuiteResults(ts *JUnitTestSuite) {
	var numFailed, numSkipped, numPassed int
	slog.Info("Test suite results", "suite", ts.Name)
	for _, test := range ts.TestCases {
		if test.Skipped != nil { //nolint:gocritic
			slog.Warn("SKIP", "test", test.Name, "reason", test.Skipped.Message)
			numSkipped++
		} else if test.Failure != nil {
			slog.Error("FAIL", "test", test.Name, "error", strings.ReplaceAll(test.Failure.Message, "\n", "; "))
			numFailed++
		} else {
			slog.Info("PASS", "test", test.Name)
			numPassed++
		}
	}
	slog.Info("Test suite summary", "tests", len(ts.TestCases), "passed", numPassed, "skipped", numSkipped, "failed", numFailed, "duration", ts.TimeHuman)
}

func pauseOnFailure(ctx context.Context) error {
	slog.Warn("Test failed, pausing execution. Note that reverts might still need to apply, so if you intend to continue, please make sure to leave the environment in the same state as you found it")

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// CI environment - pause for a long time to allow debugging
		pauseDuration := 60 * time.Minute
		slog.Info("Test will automatically continue due to the non-interactive env after the pause duration", "duration", pauseDuration)
		slog.Info("You can connect to debug the VLAB state during this pause")

		select {
		case <-ctx.Done():
			return fmt.Errorf("sleeping for pause on failure: %w", ctx.Err())
		case <-time.After(pauseDuration):
		}
	} else {
		// pause until the user presses enter
		slog.Info("Press enter to continue...")
		var input string
		if _, err := fmt.Scanln(&input); err != nil {
			return fmt.Errorf("waiting for enter: %w", err)
		}
	}

	slog.Info("Continuing...")

	return nil
}

// prepare for a test: create the VPCs according to the options in the test context
func (testCtx *VPCPeeringTestCtx) setupTest(ctx context.Context, initialSuiteSetup bool) error {
	if testCtx.noSetup {
		// nothing to setup, but we still want to wait for the switches to be ready
		if err := WaitReady(ctx, testCtx.kube, testCtx.wrOpts); err != nil {
			return fmt.Errorf("waiting for switches to be ready: %w", err)
		}

		return nil
	}
	// if it is the first setup of the suite, we also want to remove the old VPCs (might have different parameters)
	opts := testCtx.setupOpts
	opts.ForceCleanup = initialSuiteSetup
	// this will also remove all peerings
	if err := DoVLABSetupVPCs(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir, opts); err != nil {
		return fmt.Errorf("setting up VPCs: %w", err)
	}
	// in case of L3 VPC mode, we need to give it time to switch to the longer lease time and switches to learn the routes
	if opts.VPCMode == vpcapi.VPCModeL3VNI || opts.VPCMode == vpcapi.VPCModeL3Flat {
		time.Sleep(10 * time.Second)
	}

	return nil
}

func doRunSuite(ctx context.Context, testCtx *VPCPeeringTestCtx, ts *JUnitTestSuite) (*JUnitTestSuite, error) {
	suiteStart := time.Now()
	ranSomeTests := false
	slog.Info("** Running test suite", "suite", ts.Name, "tests", len(ts.TestCases), "start-time", suiteStart.Format(time.RFC3339))

	// initial setup
	if err := testCtx.setupTest(ctx, true); err != nil {
		slog.Error("Initial test suite setup failed", "suite", ts.Name, "error", err.Error())
		if testCtx.pauseOnFail {
			if err := pauseOnFailure(ctx); err != nil {
				slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
			}
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
			if err := testCtx.setupTest(ctx, false); err != nil {
				ts.TestCases[i].Failure = &Failure{
					Message: fmt.Sprintf("Failed to run setupTest between tests: %s", err.Error()),
				}
				ts.Failures++
				slog.Error("FAIL", "test", test.Name, "error", fmt.Sprintf("Failed to run setupTest between tests: %s", err.Error()))
				if testCtx.pauseOnFail {
					if err := pauseOnFailure(ctx); err != nil {
						slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
					}
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
				if err := pauseOnFailure(ctx); err != nil {
					slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
				}
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
					if err := pauseOnFailure(ctx); err != nil {
						slog.Warn("Pause on failure failed, ignoring", "err", err.Error())
					}
				}

				break
			}
		}
		if !skip && err == nil && revertErr == nil {
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
		if test.SkipFlags.ExtendedOnly && !skipFlags.ExtendedOnly {
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
		if test.SkipFlags.NoFabricLink && skipFlags.NoFabricLink {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are no fabric (i.e. spine-leaf) links between the switches",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoMeshLink && skipFlags.NoMeshLink {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are no mesh (i.e. leaf-leaf) links between the switches",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoGateway && skipFlags.NoGateway {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "Gateway is not enabled or no gateways available",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoLoki && skipFlags.NoLoki {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "Loki is not configured or available",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoProm && skipFlags.NoProm {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "Prometheus is not configured or available",
			}
			suite.Skipped++

			continue
		}
		if test.SkipFlags.NoServers && skipFlags.NoServers {
			suite.TestCases[i].Skipped = &Skipped{
				Message: "There are no servers in the fabric",
			}
			suite.Skipped++

			continue
		}
	}
	if suite.Skipped == suite.Tests {
		slog.Info("All tests in suite skipped, skipping suite", "suite", suite.Name)

		return suite, nil
	}

	suite, err := doRunSuite(ctx, testCtx, suite)
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

func RunReleaseTestSuites(ctx context.Context, vlabCfg *Config, vlab *VLAB, rtOtps ReleaseTestOpts) error {
	testStart := time.Now()

	cacheCancel, kube, err := getKubeClientWithCache(ctx, vlabCfg.WorkDir)
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
	noServers := len(servers.Items) == 0
	if noServers {
		slog.Warn("No servers found, server-requiring tests will be skipped")
	}
	subnetsPerVpc := 3
	serversPerSubnet := int(math.Ceil(float64(len(servers.Items)) / float64(subnetsPerVpc)))
	slog.Debug("Calculated servers per subnet for single VPC", "servers", len(servers.Items), "subnets-per-vpc", subnetsPerVpc, "servers-per-subnet", serversPerSubnet)

	setupOpts := SetupVPCsOpts{
		WaitSwitchesReady: true,
		ForceCleanup:      false,
		ServersPerSubnet:  serversPerSubnet,
		SubnetsPerVPC:     subnetsPerVpc,
		VLANNamespace:     "default",
		IPv4Namespace:     "default",
		HashPolicy:        rtOtps.HashPolicy,
		VPCMode:           rtOtps.VPCMode,
	}

	testCtx := makeTestCtx(kube, setupOpts, vlabCfg, vlab, false, rtOtps)
	noVpcSuite := makeNoVpcsSuite(testCtx)
	singleVpcSuite := makeSingleVPCSuite(testCtx)
	multiVPCMultiSubnetSuite := makeMultiVPCMultiSubnetSuite(testCtx)
	multiVPCSingleSubnetSuite := makeMultiVPCSingleSubnetSuite(testCtx)
	suites := []*JUnitTestSuite{noVpcSuite, singleVpcSuite, multiVPCMultiSubnetSuite, multiVPCSingleSubnetSuite}

	if rtOtps.ListTests {
		for _, suite := range suites {
			printTestSuite(suite)
		}

		return nil
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
		NoServers:    noServers,
	}
	connList := &wiringapi.ConnectionList{}
	if err := kube.List(ctx, connList, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeMesh}); err != nil {
		return fmt.Errorf("listing mesh connections: %w", err)
	}
	if len(connList.Items) == 0 {
		slog.Info("No mesh connections found")
		skipFlags.NoMeshLink = true
	}
	connList = &wiringapi.ConnectionList{}
	if err := kube.List(ctx, connList, kclient.MatchingLabels{wiringapi.LabelConnectionType: wiringapi.ConnectionTypeFabric}); err != nil {
		return fmt.Errorf("listing fabric connections: %w", err)
	}
	if len(connList.Items) == 0 {
		slog.Info("No fabric connections found")
		skipFlags.NoFabricLink = true
	}

	f, _, _, err := fab.GetFabAndNodes(ctx, kube, fab.GetFabAndNodesOpts{AllowNotHydrated: true})
	if err != nil {
		return fmt.Errorf("getting fab: %w", err)
	}

	if !f.Spec.Config.Gateway.Enable {
		slog.Info("Gateway not enabled, gateway tests will be skipped")
		skipFlags.NoGateway = true
	} else {
		gateways := &gwapi.GatewayList{}
		if err := kube.List(ctx, gateways); err != nil {
			return fmt.Errorf("listing gateways: %w", err)
		}
		if len(gateways.Items) == 0 {
			slog.Info("No gateways found, gateway tests will be skipped")
			skipFlags.NoGateway = true
		}
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
				slog.Warn("Virtual switch found, some tests will be skipped", "switch", sw.Name)
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
			slog.Warn("Subinterfaces not supported on leaf switch, some tests will be skipped", "switch-profile", sw.Spec.Profile, "switch", sw.Name)
			skipFlags.SubInterfaces = true
		}
		// exclude virtual switches from RoCE check, they do not implement counters
		if profile.Spec.Features.RoCE && sw.Spec.Profile != meta.SwitchProfileVS {
			roceLeaves = append(roceLeaves, sw.Name)
		}
	}
	testCtx.roceLeaves = roceLeaves
	if len(roceLeaves) == 0 {
		slog.Warn("No RoCE capable leaves found, some tests will be skipped")
		skipFlags.RoCE = true
	}
	extList := &vpcapi.ExternalList{}
	if err := kube.List(ctx, extList); err != nil {
		return fmt.Errorf("listing externals: %w", err)
	}
	extAttachList := &vpcapi.ExternalAttachmentList{}
	if err := kube.List(ctx, extAttachList); err != nil {
		return fmt.Errorf("listing external attachments: %w", err)
	}
	if len(extList.Items) == 0 || len(extAttachList.Items) == 0 {
		slog.Warn("No externals found, some tests will be skipped")
		skipFlags.NoExternals = true
	} else {
		testCtx.extName = ""
		// look first for hardware externals with at least one attachment
		for _, ext := range extList.Items {
			if !isHardware(&ext) {
				slog.Debug("Skipping non-hardware external", "external", ext.Name)

				continue
			}
			for _, extAttach := range extAttachList.Items {
				if extAttach.Spec.External != ext.Name {
					continue
				}
				testCtx.extName = ext.Name

				break
			}
			if testCtx.extName == "" {
				slog.Debug("No external attachments found for hardware external, skipping it", "external", ext.Name)

				continue
			}
			slog.Info("Using hardware external as the \"default\"", "external", testCtx.extName)

			break
		}
		if testCtx.extName == "" {
			slog.Debug("No viable hardware externals found, checking for virtual externals attached to hw switches")
			for _, ext := range extList.Items {
				extAttach := &vpcapi.ExternalAttachmentList{}
				if err := kube.List(ctx, extAttach, kclient.MatchingLabels{wiringapi.LabelName("external"): ext.Name}); err != nil {
					return fmt.Errorf("listing external attachments for %s: %w", ext.Name, err)
				}
				if len(extAttach.Items) == 0 {
					continue
				}
				// check if all of the attachments are via hardware connections
				someNotHW := false
				for _, attach := range extAttach.Items {
					conn := &wiringapi.Connection{}
					if err := kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: attach.Spec.Connection}, conn); err != nil {
						return fmt.Errorf("getting connection %s: %w", attach.Spec.Connection, err)
					}
					if !isHardware(conn) {
						slog.Debug("Skipping virtual external due to non-hardware attachment", "external", ext.Name, "connection", conn.Name)
						someNotHW = true

						break
					}
				}
				if !someNotHW {
					testCtx.extName = ext.Name
					slog.Info("Using virtual external as the \"default\"", "external", testCtx.extName)

					break
				}
			}
			if testCtx.extName == "" {
				slog.Warn("No viable external found, some tests will be skipped")
				skipFlags.NoExternals = true
			}
		}
	}

	fabricator := &fabricatorapi.Fabricator{}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: kmetav1.NamespaceDefault}, fabricator); err != nil {
		slog.Warn("Unable to get Fabricator object, observability tests will be skipped", "error", err)
		skipFlags.NoLoki = true
		skipFlags.NoProm = true
	} else {
		// Check for any Loki targets with valid URLs
		skipFlags.NoLoki = true
		if fabricator.Spec.Config.Observability.Targets.Loki != nil {
			for _, lokiTarget := range fabricator.Spec.Config.Observability.Targets.Loki {
				if lokiTarget.URL != "" {
					skipFlags.NoLoki = false

					break
				}
			}
		}

		// Check for any Prometheus targets with valid URLs
		skipFlags.NoProm = true
		if fabricator.Spec.Config.Observability.Targets.Prometheus != nil {
			for _, promTarget := range fabricator.Spec.Config.Observability.Targets.Prometheus {
				if promTarget.URL != "" {
					skipFlags.NoProm = false

					break
				}
			}
		}
	}

	if skipFlags.NoLoki {
		slog.Info("No Loki target found, Loki test will be skipped")
	}

	if skipFlags.NoProm {
		slog.Info("No Prometheus target found, Prometheus test will be skipped")
	}

	results := []JUnitTestSuite{}
	testCtx.noSetup = true
	noVpcResults, err := selectAndRunSuite(ctx, testCtx, noVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running no VPC suite: %w", err)
	}
	results = append(results, *noVpcResults)

	testCtx.noSetup = false
	singleVpcResults, err := selectAndRunSuite(ctx, testCtx, singleVpcSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running single VPC suite: %w", err)
	}
	results = append(results, *singleVpcResults)

	testCtx.setupOpts.ServersPerSubnet = 1
	multiVpcResults, err := selectAndRunSuite(ctx, testCtx, multiVPCMultiSubnetSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running multi VPC suite: %w", err)
	}
	results = append(results, *multiVpcResults)

	testCtx.setupOpts.SubnetsPerVPC = 1
	testCtx.wipeBetweenTests = true
	basicResults, err := selectAndRunSuite(ctx, testCtx, multiVPCSingleSubnetSuite, regexesCompiled, rtOtps.InvertRegex, skipFlags)
	if err != nil && rtOtps.FailFast {
		return fmt.Errorf("running basic VPC suite: %w", err)
	}
	results = append(results, *basicResults)

	slog.Info("*** Recap of the test results ***")
	for _, suite := range results {
		printSuiteResults(&suite)
	}

	if rtOtps.ResultsFile != "" {
		report := JUnitReport{
			Suites: results,
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
