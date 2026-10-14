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
	// disruptionMinEntries is how many IRB VLAN entries each leaf of the pair must hold alone:
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

func makeDisruptionTestSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Disruption Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name:      "Disruption Under Traffic",
			F:         disruptionTest,
			SkipFlags: SkipFlags{NoServers: true},
		},
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

// disruptionTest runs the common changes under background traffic and fails on any disruption
// the change was not expected to cause, on a spec change it was not expected to make and on an
// Agent that doesn't report the generation as applied.
func disruptionTest(ctx context.Context, testCtx *VPCPeeringTestCtx, _ *ConnectivityMatrix) (bool, []RevertFunc, error) {
	kube := testCtx.kube

	if testCtx.setupOpts.VPCMode != vpcapi.VPCModeL2VNI {
		return true, nil, fmt.Errorf("VPC mode is %q, ESLAG servers only take part in l2vni", testCtx.setupOpts.VPCMode) //nolint:err113
	}
	if testCtx.extName == "" {
		return true, nil, errNoExternals
	}

	sc, err := discoverDisruptionScenario(ctx, kube)
	if err != nil {
		return true, nil, err
	}
	slog.Info("Disruption scenario", "scenario", sc.describe())

	env := &disruptionEnv{
		testCtx:     testCtx,
		kube:        kube,
		sc:          sc,
		probeClass:  map[string]string{},
		touched:     map[string]bool{},
		noted:       map[string]bool{},
		evidenceDir: filepath.Join(testCtx.vlabCfg.WorkDir, ShowTechOutputDir, sanitizeNameForFolder("Disruption Suite"), sanitizeNameForFolder("Disruption Under Traffic"), "disruption"),
	}

	reverts := []RevertFunc{env.cleanup}

	if err := env.setup(ctx); err != nil {
		return false, reverts, err
	}

	env.aw = newAgentWatch(kube)
	env.aw.Start(ctx)
	defer env.aw.Stop()
	defer func() {
		if err := env.tw.Stop(context.WithoutCancel(ctx)); err != nil {
			slog.Warn("Stopping background traffic failed", "err", err)
		}
	}()

	if err := env.tw.Start(ctx); err != nil {
		return false, reverts, fmt.Errorf("starting background traffic: %w", err)
	}

	if err := env.baseline(ctx); err != nil {
		return false, reverts, err
	}

	var errs []error
	for _, group := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"create and delete an unattached VPC", env.opCreateDeleteVPC},
		{"touch label on the leaves of the pair", env.opTouchLeaves},
		{"fabric-ctrl restart", env.opRestartController},
		{"agent reboot", env.opReboot},
		{"agent reinstall", env.opReinstall},
	} {
		slog.Info("Disruption operation", "operation", group.name)
		if err := group.run(ctx); err != nil {
			slog.Error("FAIL", "operation", group.name, "error", err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", group.name, err))

			continue
		}
		slog.Info("PASS", "operation", group.name)
	}

	if drift, err := env.tw.ClockDrift(ctx); err != nil {
		slog.Warn("Measuring server clock drift failed", "err", err)
	} else if drift > disruptionMaxClockSkew {
		slog.Warn("Server clocks drifted during the run, the times of lost pings are approximate", "drift", drift)
	}

	return false, reverts, errors.Join(errs...)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting: %w", ctx.Err())
	case <-time.After(d):
		return nil
	}
}
