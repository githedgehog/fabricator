// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package testing

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"slices"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/exp/maps"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RunnerConfig struct {
	StepHelper  StepHelper
	Timeout     time.Duration
	TestTimeout time.Duration
	TestFiles   []string
	TestNames   []string
	RandomOrder bool
	RepeatTimes uint
	RepeatFor   time.Duration
}

type Runner struct {
	cfg   RunnerConfig
	tests map[string]*Test
}

type Test struct {
	labels map[string]string
	steps  []Step
}

type Step interface {
	Run(ctx context.Context, h StepHelper) error
}

type StepHelper interface {
	Kube() client.WithWatch
	ServerExec(ctx context.Context, server, cmd string, timeout time.Duration) (string, error)
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if len(cfg.TestFiles) < 1 {
		return nil, errors.Errorf("test files are not specified")
	}

	runner := &Runner{
		cfg: cfg,
	}

	if err := runner.loadTests(); err != nil {
		return nil, errors.Wrapf(err, "error loading tests")
	}

	return runner, nil
}

func (r *Runner) Run(ctx context.Context) error {
	if r.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.Timeout)
		defer cancel()
	}

	allStart := time.Now()

	if r.cfg.RepeatFor > 0 {
		return errors.Errorf("repeat for is not implemented yet")
	}

	if r.cfg.RepeatTimes < 1 {
		r.cfg.RepeatTimes = 1
	}

	testNames := maps.Keys(r.tests)
	if r.cfg.RandomOrder {
		rand.Shuffle(len(testNames), func(i, j int) {
			testNames[i], testNames[j] = testNames[j], testNames[i]
		})
	}

	for run := uint(1); run <= r.cfg.RepeatTimes; run++ {
		for _, name := range testNames {
			test := r.tests[name]

			if len(r.cfg.TestNames) > 0 && !slices.Contains(r.cfg.TestNames, name) {
				continue
			}

			if err := r.runTest(ctx, name, test); err != nil {
				return errors.Wrapf(err, "error running test %s", name)
			}
		}

		if r.cfg.RepeatTimes > 1 {
			slog.Info("Repeat completed", "run", fmt.Sprintf("%d/%d", run, r.cfg.RepeatTimes), "took", time.Since(allStart))
		}
	}

	slog.Info("All tests completed", "took", time.Since(allStart))

	return nil
}

func (r *Runner) runTest(ctx context.Context, name string, test *Test) error {
	if r.cfg.TestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.TestTimeout)
		defer cancel()
	}

	testStart := time.Now()

	slog.Info("Running test", "name", name)

	for _, step := range test.steps {
		stepStart := time.Now()

		if err := step.Run(ctx, r.cfg.StepHelper); err != nil {
			return errors.Wrapf(err, "error running test %s", name)
		}

		slog.Info("Step completed", "took", time.Since(stepStart))
	}

	slog.Info("Test completed", "name", name, "took", time.Since(testStart))

	return nil
}
