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
	"time"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RunnerConfig struct {
	StepHelper StepHelper
	TestFiles  []string
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
	Kube() client.Client
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
	for name, test := range r.tests {
		for _, step := range test.steps {
			if err := step.Run(ctx, r.cfg.StepHelper); err != nil {
				return errors.Wrapf(err, "error running test %s", name)
			}
		}
	}

	return nil
}
