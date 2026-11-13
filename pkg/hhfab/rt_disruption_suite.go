// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	disruptionBaseline     = 2 * time.Minute
	disruptionTouchRounds  = 12
	disruptionVPCCycles    = 5
	disruptionCtrlRestarts = 3

	// disruptionMaxServersPerLeaf is how many of the servers on a leaf of the pair, and of the
	// ESLAG servers, get a VPC
	disruptionMaxServersPerLeaf = 2

	// disruptionFillerPasses is how many times setup adds VPCs before a leaf still short of
	// disruptionMinEntries fails it
	disruptionFillerPasses = 3

	disruptionRolloutMax   = 5 * time.Minute
	disruptionPollInterval = 2 * time.Second
	// disruptionEvidenceMax bounds reading the traffic for the evidence of a test that ended
	disruptionEvidenceMax = time.Minute

	// disruptionMinEntries is how many VPC IRB VLAN entries each leaf of the pair must hold alone:
	// with fewer the allocator hands the lone entry the lowest free id every time and a
	// renumbering never shows.
	disruptionMinEntries   = 3
	disruptionSettleQuiet  = 10 * time.Second
	disruptionSettleMax    = 5 * time.Minute
	disruptionWarmup       = 5 * time.Second
	disruptionTrailGrace   = 2 * time.Second
	disruptionMaxClockSkew = 500 * time.Millisecond
	disruptionReinstallMax = 30 * time.Minute
	disruptionRebootMax    = 20 * time.Minute
	// disruptionESLAGMaxLoss is the longest loss window allowed on a pair that fails over to the
	// other leaf, where lost pings less than three intervals apart are one window: a leaf going
	// away costs two pings at most
	disruptionESLAGMaxLoss = time.Second
	// once the leaf that was away has applied its config, the dataplane gets disruptionRecovery
	// to converge; after that, loss is checked for disruptionWatchAfter
	disruptionRecovery   = 5 * time.Second
	disruptionWatchAfter = 30 * time.Second

	disruptionExternalTarget = "1.0.0.1"
	disruptionTouchLabel     = meta.BenchTouchLabel
)

const (
	disruptionSuiteName = "Disruption Suite"

	testDisruptionSetup     = "Disruption Setup and Baseline"
	testDisruptionVPC       = "Disruption VPC Create/Delete"
	testDisruptionTouch     = "Disruption Leaf Touch"
	testDisruptionCtrl      = "Disruption Fabric Controller Restart"
	testDisruptionReboot    = "Disruption Agent Reboot"
	testDisruptionReinstall = "Disruption Agent Reinstall"
)

// errDisruptionHardwareOnly makes a test skip: it can't run on the leaves of the pair.
var errDisruptionHardwareOnly = errors.New("hardware only")

// makeDisruptionTestSuite is one test per operation. The first builds the VPCs, starts the
// traffic and watches it with no change; the others each apply one kind of change to the same
// running traffic and check only what happens from their own start. The traffic is stopped and
// the VPCs removed once, after the last test, by the suite teardown.
func makeDisruptionTestSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: disruptionSuiteName,
		Teardown: func(ctx context.Context, testCtx *VPCPeeringTestCtx) error {
			return testCtx.teardownDisruption(ctx)
		},
	}
	suite.TestCases = []JUnitTestCase{
		{Name: testDisruptionSetup, F: disruptionSetupTest, SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionVPC, F: disruptionOpTest(testDisruptionVPC, (*disruptionEnv).opCreateDeleteVPC), SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionTouch, F: disruptionOpTest(testDisruptionTouch, (*disruptionEnv).opTouchLeaves), SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionCtrl, F: disruptionOpTest(testDisruptionCtrl, (*disruptionEnv).opRestartController), SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionReboot, F: disruptionOpTest(testDisruptionReboot, (*disruptionEnv).opReboot), SkipFlags: SkipFlags{NoServers: true}},
		{Name: testDisruptionReinstall, F: disruptionOpTest(testDisruptionReinstall, (*disruptionEnv).opReinstall), SkipFlags: SkipFlags{NoServers: true}},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// Probe classes: how much loss a probe may show while the second leaf of the pair is away.
const (
	probeStrict = "strict" // doesn't cross the leaf: no loss allowed at any time
	probeHard   = "hard"   // an endpoint is attached to that leaf only: down while the leaf is away
	probeSoft   = "soft"   // fails over to the other leaf, or its path isn't known: short loss only
)

type disruptionEnv struct {
	testCtx *VPCPeeringTestCtx
	kube    kclient.Client
	sc      *disruptionScenario
	alloc   *vpcSubnetAllocator
	tw      *trafficWatch
	aw      *agentWatch
	// probeClass maps a probe name to its class (probeStrict, probeHard, probeSoft)
	probeClass map[string]string
	// cursor is where the next traffic check starts, so no moment is checked twice
	cursor      time.Time
	evidenceDir string
	// touched holds the leaves that got a touch label, to remove it in the end
	touched map[string]bool
	// noted holds the notes already in the result
	noted map[string]bool
}

// disruptionState is what the setup produced, kept on the test context for the tests that follow.
type disruptionState struct {
	env *disruptionEnv
	// skip is why the suite can't run on this fabric: every test of it skips with the same reason
	skip error
	// err is why the setup or the baseline did not succeed: the other tests have no verdict
	err error
	// started is set once the traffic runs
	started bool
}

// ensureDisruption builds the environment on the first call, and returns it on the others, so a
// selection of tests that leaves out the setup test still works.
func (testCtx *VPCPeeringTestCtx) ensureDisruption(ctx context.Context) *disruptionState {
	if testCtx.disruption == nil {
		testCtx.disruption = testCtx.setupDisruption(ctx)
	}

	return testCtx.disruption
}

func (testCtx *VPCPeeringTestCtx) setupDisruption(ctx context.Context) *disruptionState {
	st := &disruptionState{}
	kube := testCtx.kube

	if testCtx.setupOpts.VPCMode != vpcapi.VPCModeL2VNI {
		st.skip = fmt.Errorf("VPC mode is %q, ESLAG servers only take part in l2vni", testCtx.setupOpts.VPCMode) //nolint:err113

		return st
	}
	if testCtx.extName == "" {
		st.skip = errNoExternals

		return st
	}

	sc, err := discoverDisruptionScenario(ctx, kube)
	switch {
	case errors.Is(err, errDisruptionNoPair), errors.Is(err, errDisruptionNoESLAGServer), errors.Is(err, errDisruptionNoSingleLeaf):
		st.skip = err

		return st
	case err != nil:
		st.err = err

		return st
	}
	slog.Info("Disruption scenario", "scenario", sc.describe())

	env := &disruptionEnv{
		testCtx:    testCtx,
		kube:       kube,
		sc:         sc,
		probeClass: map[string]string{},
		touched:    map[string]bool{},
		noted:      map[string]bool{},
	}
	st.env = env

	if err := env.setup(ctx); err != nil {
		st.err = err

		return st
	}

	env.aw = newAgentWatch(kube)
	env.aw.Start(ctx)
	if err := env.tw.Start(ctx); err != nil {
		st.err = fmt.Errorf("starting background traffic: %w", err)

		return st
	}
	st.started = true

	if err := env.baseline(ctx); err != nil {
		st.err = err
	}

	return st
}

// disruptionSetupTest builds the VPCs, starts the traffic and watches it with no change. Loss in
// the baseline makes the whole suite inconclusive, and it is reported here.
//
// Builds the entries that only one leaf holds, which the renumbering in
// fabric#1649 needs to show.
func disruptionSetupTest(ctx context.Context, testCtx *VPCPeeringTestCtx, _ *ConnectivityMatrix) (bool, []RevertFunc, error) {
	st := testCtx.ensureDisruption(ctx)
	if st.skip != nil {
		return true, nil, st.skip
	}

	return false, nil, st.err
}

// disruptionOpTest makes a test of one operation under the running traffic. It checks loss only
// from its own start, so one test's loss is never charged to the next. Without a clean setup it
// has no verdict.
func disruptionOpTest(name string, run func(*disruptionEnv, context.Context) error) TestFunc {
	return func(ctx context.Context, testCtx *VPCPeeringTestCtx, _ *ConnectivityMatrix) (bool, []RevertFunc, error) {
		st := testCtx.ensureDisruption(ctx)
		if st.skip != nil {
			return true, nil, st.skip
		}
		if st.err != nil {
			return false, nil, fmt.Errorf("%w: setup and baseline did not succeed: %w", errInconclusive, st.err)
		}

		env := st.env
		env.evidenceDir = filepath.Join(testCtx.vlabCfg.WorkDir, ShowTechOutputDir, sanitizeNameForFolder(disruptionSuiteName), sanitizeNameForFolder(name), "disruption")
		started := time.Now()
		env.cursor = started

		err := run(env, ctx)
		switch {
		case err == nil:
			return false, nil, nil
		case errors.Is(err, errDisruptionHardwareOnly):
			return true, nil, err
		}

		// whatever ended the test, keep the raw traffic and the Agent timeline: the teardown removes
		// the ping logs from the servers. The test's context may be done by now.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), disruptionEvidenceMax)
		defer cancel()
		files := map[string]string{"agent-timeline.txt": formatAgentRows(env.aw.Rows(started, time.Now()))}
		if tl, snapErr := env.tw.Snapshot(saveCtx); snapErr != nil {
			slog.Warn("Reading the traffic for evidence failed", "test", name, "err", snapErr)
		} else {
			for _, p := range tl {
				files["ping-"+sanitizeNameForFolder(p.Probe)+".log"] = p.Raw
			}
		}
		env.writeEvidence("", files)

		return false, nil, err
	}
}

// teardownDisruption stops the traffic and removes what the setup created. It runs once, after the last test.
func (testCtx *VPCPeeringTestCtx) teardownDisruption(ctx context.Context) error {
	st := testCtx.disruption
	if st == nil || st.env == nil {
		return nil
	}
	testCtx.disruption = nil
	env := st.env

	var errs []error
	if env.tw != nil {
		if st.started {
			if drift, err := env.tw.ClockDrift(ctx); err != nil {
				slog.Warn("Measuring server clock drift failed", "err", err)
			} else if drift > disruptionMaxClockSkew {
				slog.Warn("Server clocks drifted during the run, the times of lost pings are approximate", "drift", drift)
			}
		}
		if err := env.tw.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping background traffic: %w", err))
		}
	}
	if env.aw != nil {
		env.aw.Stop()
	}
	if err := env.cleanup(ctx); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting: %w", ctx.Err())
	case <-time.After(d):
		return nil
	}
}
