// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRegexpSelection(t *testing.T) {
	tsr := JUnitTestSuite{
		Name: "TestSuite1",
		TestCases: []JUnitTestCase{
			{
				Name: "No Restrictions",
			},
			{
				Name: "Single VPC with Restrictions",
			},
			{
				Name: "DNS/NTP/MTU",
			},
			{
				Name: "Static External",
			},
			{
				Name: "MCLAG",
			},
			{
				Name: "ESLAG",
			},
		},
	}

	// Test cases
	tests := []struct {
		name        string
		regexes     []string
		invertRegex bool
		indexes     []int
	}{
		{
			name:        "No Regexes",
			regexes:     []string{},
			invertRegex: false,
			indexes:     []int{0, 1, 2, 3, 4, 5},
		},
		{
			name:        "No Regexes Inverted (no effect)",
			regexes:     []string{},
			invertRegex: true,
			indexes:     []int{0, 1, 2, 3, 4, 5},
		},
		{
			name:        "LAG",
			regexes:     []string{"LAG$"},
			invertRegex: false,
			indexes:     []int{4, 5},
		},
		{
			name:        "LAG Inverted",
			regexes:     []string{"LAG$"},
			invertRegex: true,
			indexes:     []int{0, 1, 2, 3},
		},
		{
			name:        "Restrictions + LAG",
			regexes:     []string{"LAG$", "Restrictions"},
			invertRegex: false,
			indexes:     []int{0, 1, 4, 5},
		},
		{
			name:        "Restrictions + LAG Inverted",
			regexes:     []string{"LAG$", "Restrictions"},
			invertRegex: true,
			indexes:     []int{2, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newSuite := &JUnitTestSuite{
				Name:      tsr.Name,
				TestCases: make([]JUnitTestCase, len(tsr.TestCases)),
			}
			copy(newSuite.TestCases, tsr.TestCases)
			var regexes []*regexp.Regexp
			for _, regex := range test.regexes {
				r, err := regexp.Compile(regex)
				if err != nil {
					t.Errorf("Error compiling regex %s: %v", regex, err)
				}
				regexes = append(regexes, r)
			}
			newSuite = regexpSelection(regexes, test.invertRegex, newSuite)
			for i, tc := range newSuite.TestCases {
				if slices.Contains(test.indexes, i) {
					if tc.Skipped != nil {
						t.Errorf("Test case %s should not be skipped", tc.Name)
					}
				} else {
					if tc.Skipped == nil {
						t.Errorf("Test case %s should be skipped", tc.Name)
					}
				}
			}
		})
	}
}

func TestRunSuiteOutcomes(t *testing.T) {
	errBoom := errors.New("boom")                    //nolint:err113
	errRevert := errors.New("revert failed")         //nolint:err113
	errNotApplicable := errors.New("not applicable") //nolint:err113
	errPrecondition := fmt.Errorf("%w: precondition", errInconclusive)
	failingRevert := []RevertFunc{func(context.Context) error { return errRevert }}

	pass := func(context.Context, *VPCPeeringTestCtx, *ConnectivityMatrix) (bool, []RevertFunc, error) {
		return false, nil, nil
	}
	fail := func(context.Context, *VPCPeeringTestCtx, *ConnectivityMatrix) (bool, []RevertFunc, error) {
		return false, nil, errBoom
	}
	skip := func(context.Context, *VPCPeeringTestCtx, *ConnectivityMatrix) (bool, []RevertFunc, error) {
		return true, nil, errNotApplicable
	}
	inconclusive := func(context.Context, *VPCPeeringTestCtx, *ConnectivityMatrix) (bool, []RevertFunc, error) {
		return false, nil, errPrecondition
	}
	inconclusiveRevertFails := func(context.Context, *VPCPeeringTestCtx, *ConnectivityMatrix) (bool, []RevertFunc, error) {
		return false, failingRevert, errPrecondition
	}
	skipRevertFails := func(context.Context, *VPCPeeringTestCtx, *ConnectivityMatrix) (bool, []RevertFunc, error) {
		return true, failingRevert, errNotApplicable
	}
	failRevertFails := func(context.Context, *VPCPeeringTestCtx, *ConnectivityMatrix) (bool, []RevertFunc, error) {
		return false, failingRevert, errBoom
	}

	// setupTest runs WaitReady on a client holding the dev Fabricator: ready with its conditions
	// set, never ready without them
	kubeFor := func(t *testing.T, ready bool) kclient.Client {
		t.Helper()
		l := apiutil.NewLoader()
		require.NoError(t, l.LoadAdd(t.Context(), apiutil.FabricatorGVKs, []byte(devConfig(t))))
		kube := l.GetClient()
		if ready {
			f := &fabapi.Fabricator{}
			require.NoError(t, kube.Get(t.Context(), kclient.ObjectKey{Name: comp.FabName, Namespace: comp.FabNamespace}, f))
			for _, c := range []string{fabapi.ConditionApplied, fabapi.ConditionReady, fabapi.ConditionGatewayReady} {
				kmeta.SetStatusCondition(&f.Status.Conditions, kmetav1.Condition{Type: c, Status: kmetav1.ConditionTrue, Reason: "Test"})
			}
			require.NoError(t, kube.Update(t.Context(), f))
		}

		return kube
	}

	type want struct {
		outcome string
		message string // contained in the message of the outcome
	}
	for _, tt := range []struct {
		name       string
		setupFails bool
		failFast   bool
		tests      []JUnitTestCase
		wantErr    bool
		failures   int
		errors     int
		skipped    int
		want       []want
		// doubleCounted lets a test case have two outcomes
		doubleCounted bool
	}{
		{
			name:       "initial setup fails",
			setupFails: true,
			tests:      []JUnitTestCase{{Name: "a", F: pass}, {Name: "b", F: pass}, {Name: "c", Skipped: &Skipped{Message: "Regex selection"}}},
			failures:   2,
			skipped:    1,
			want:       []want{{"fail", errInitialSetup.Error()}, {"fail", errInitialSetup.Error()}, {"skip", "Regex selection"}},
		},
		{
			name:       "initial setup fails with fail-fast",
			setupFails: true,
			failFast:   true,
			tests:      []JUnitTestCase{{Name: "a", F: pass}, {Name: "b", F: pass}},
			wantErr:    true,
			failures:   2,
			want:       []want{{"fail", errInitialSetup.Error()}, {"fail", errInitialSetup.Error()}},
		},
		{
			name:  "passes",
			tests: []JUnitTestCase{{Name: "a", F: pass}},
			want:  []want{{"pass", ""}},
		},
		{
			name:     "fails",
			tests:    []JUnitTestCase{{Name: "a", F: fail}, {Name: "b", F: pass}},
			failures: 1,
			want:     []want{{"fail", "boom"}, {"pass", ""}},
		},
		{
			name:    "is skipped",
			tests:   []JUnitTestCase{{Name: "a", F: skip}},
			skipped: 1,
			want:    []want{{"skip", "not applicable"}},
		},
		{
			name:   "is inconclusive",
			tests:  []JUnitTestCase{{Name: "a", F: inconclusive}},
			errors: 1,
			want:   []want{{"error", "inconclusive: precondition"}},
		},
		{
			name:     "is inconclusive and its revert fails",
			tests:    []JUnitTestCase{{Name: "a", F: inconclusiveRevertFails}},
			failures: 1,
			want:     []want{{"fail", "revert failed; before that: inconclusive: precondition"}},
		},
		{
			name:     "is inconclusive and its revert fails with fail-fast",
			failFast: true,
			tests:    []JUnitTestCase{{Name: "a", F: inconclusiveRevertFails}, {Name: "b", F: pass}},
			wantErr:  true,
			failures: 1,
			skipped:  1,
			want:     []want{{"fail", "revert failed; before that: inconclusive: precondition"}, {"skip", "not run: the suite stopped before it"}},
		},
		{
			name:     "fails and its revert fails",
			tests:    []JUnitTestCase{{Name: "a", F: failRevertFails}},
			failures: 1,
			want:     []want{{"fail", "boom\nrevert failed"}},
		},
		{
			// double-counted, as on master: the test is reported both skipped and failed
			name:          "is skipped and its revert fails",
			tests:         []JUnitTestCase{{Name: "a", F: skipRevertFails}},
			failures:      1,
			skipped:       1,
			want:          []want{{"skip+fail", "revert failed"}},
			doubleCounted: true,
		},
		{
			name:     "is inconclusive with fail-fast",
			failFast: true,
			tests:    []JUnitTestCase{{Name: "a", F: inconclusive}, {Name: "b", F: pass}},
			errors:   1,
			want:     []want{{"error", "inconclusive: precondition"}, {"pass", ""}},
		},
		{
			name:     "fail-fast stops the suite",
			failFast: true,
			tests:    []JUnitTestCase{{Name: "a", F: fail}, {Name: "b", F: pass}},
			wantErr:  true,
			failures: 1,
			skipped:  1,
			want:     []want{{"fail", "boom"}, {"skip", "not run: the suite stopped before it"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wrOpts := WaitReadyOpts{Timeout: 10 * time.Second, PollInterval: time.Millisecond}
			if tt.setupFails {
				wrOpts.Timeout = 50 * time.Millisecond
			}
			testCtx := &VPCPeeringTestCtx{
				kube:     kubeFor(t, !tt.setupFails),
				noSetup:  true,
				wrOpts:   wrOpts,
				failFast: tt.failFast,
			}
			teardowns := 0
			suite := &JUnitTestSuite{Name: "S", Tests: len(tt.tests), TestCases: tt.tests, Teardown: func(context.Context, *VPCPeeringTestCtx) error {
				teardowns++

				return nil
			}}
			for _, tc := range tt.tests {
				if tc.Skipped != nil {
					suite.Skipped++
				}
			}

			got, err := selectAndRunSuite(t.Context(), testCtx, suite, nil, false, SkipFlags{})
			if tt.wantErr {
				require.Error(t, err)
				// a failed test is a failure, whatever came before it
				require.NotErrorIs(t, err, errInconclusive)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, teardowns, "teardown calls")

			require.Equal(t, len(tt.tests), got.Tests, "tests")
			require.Equal(t, tt.failures, got.Failures, "failures")
			require.Equal(t, tt.errors, got.Errors, "errors")
			require.Equal(t, tt.skipped, got.Skipped, "skipped")
			require.Len(t, got.TestCases, len(tt.want))
			passed := 0
			for i, tc := range got.TestCases {
				outcomes := 0
				for _, set := range []bool{tc.Skipped != nil, tc.Failure != nil, tc.Error != nil} {
					if set {
						outcomes++
					}
				}
				if outcomes == 0 {
					passed++
				}
				if !tt.doubleCounted {
					require.LessOrEqual(t, outcomes, 1, "outcomes of %s", tc.Name)
				}

				var outcome, message string
				switch {
				case tc.Skipped != nil && tc.Failure != nil:
					outcome, message = "skip+fail", tc.Failure.Message
				case tc.Skipped != nil:
					outcome, message = "skip", tc.Skipped.Message
				case tc.Error != nil:
					outcome, message = "error", tc.Error.Message
				case tc.Failure != nil:
					outcome, message = "fail", tc.Failure.Message
				default:
					outcome = "pass"
				}
				require.Equal(t, tt.want[i].outcome, outcome, tc.Name)
				require.Contains(t, message, tt.want[i].message, tc.Name)
			}
			if !tt.doubleCounted {
				require.Equal(t, got.Tests, passed+got.Failures+got.Errors+got.Skipped, "passed + failures + errors + skipped")
			}
		})
	}
}
