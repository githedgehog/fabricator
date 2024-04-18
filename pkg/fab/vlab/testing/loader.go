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
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/pkg/client/apiabbr"
	"sigs.k8s.io/yaml"
)

var nameChecker = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

type LoaderFile struct {
	Blocks map[string]string `json:"blocks,omitempty"`
	Tests  map[string]LTest  `json:"tests,omitempty"`
}

type LTest struct {
	Labels map[string]string `json:"labels,omitempty"`
	Steps  []LStep           `json:"steps,omitempty"`
}

type LStep struct {
	Name string `json:"name,omitempty"`

	WaitReady        *StepWaitReady        `json:"waitready,omitempty"`
	Enforce          *string               `json:"enforce,omitempty"`
	Update           *string               `json:"update,omitempty"`
	Netconf          *StepNetconf          `json:"netconf,omitempty"`
	TestConnectivity *StepTestConnectivity `json:"connectivity,omitempty"`
}

type LStepTestConnectivity struct{}

func (r *Runner) loadTests() error {
	paths := r.cfg.TestFiles

	blocks := map[string]string{}
	tests := map[string]LTest{}

	for _, path := range paths {
		slog.Info("Loading test file", "path", path)

		data, err := os.ReadFile(path)
		if err != nil {
			return errors.Wrapf(err, "error reading file %s", path)
		}

		loader := &LoaderFile{}
		if err := yaml.UnmarshalStrict(data, loader); err != nil {
			return errors.Wrapf(err, "error unmarshaling file %s", path)
		}

		for name, block := range loader.Blocks {
			if !nameChecker.MatchString(name) {
				return errors.Errorf("%s: block name %q does not match a lowercase RFC 1123 subdomain", path, name)
			}

			if _, exist := blocks[name]; exist {
				return errors.Errorf("%s: block %s already defined", path, name)
			}

			if block == "" {
				return errors.Errorf("%s: block %s is empty", path, name)
			}

			blocks[name] = block
		}

		for name, test := range loader.Tests {
			if !nameChecker.MatchString(name) {
				return errors.Errorf("%s: test name %q does not match a lowercase RFC 1123 subdomain", path, name)
			}

			if _, exist := tests[name]; exist {
				return errors.Errorf("%s: test %s already defined", path, name)
			}

			if len(test.Steps) < 1 {
				return errors.Errorf("%s: test %s has no steps", path, name)
			}

			tests[name] = test
		}
	}

	r.tests = map[string]*Test{}
	for name, lTest := range tests {
		slog.Info("Loading test", "name", name)

		test, err := r.loadTest(lTest, blocks)
		if err != nil {
			return errors.Wrapf(err, "error loading test %s", name)
		}

		r.tests[name] = test
	}

	return nil
}

func (r *Runner) loadTest(lTest LTest, blocks map[string]string) (*Test, error) {
	steps := []Step{}

	for stepIdx, lStep := range lTest.Steps {
		num := 0

		if lStep.WaitReady != nil {
			if lStep.WaitReady.Timeout.Duration == 0 {
				lStep.WaitReady.Timeout.Duration = 5 * time.Minute
			}

			steps = append(steps, lStep.WaitReady)

			num++
		}

		if lStep.Enforce != nil {
			newStep, err := r.loadAbbr(*lStep.Enforce, false, blocks)
			if err != nil {
				return nil, errors.Wrapf(err, "step %d: error loading enforce %s", stepIdx, *lStep.Enforce)
			}
			steps = append(steps, newStep)

			num++
		}

		if lStep.Update != nil {
			newStep, err := r.loadAbbr(*lStep.Update, true, blocks)
			if err != nil {
				return nil, errors.Wrapf(err, "step %d: error loading update %s", stepIdx, *lStep.Update)
			}
			steps = append(steps, newStep)

			num++
		}

		if lStep.Netconf != nil {
			steps = append(steps, lStep.Netconf)

			num++
		}

		if lStep.TestConnectivity != nil {
			if lStep.TestConnectivity.PingCount == 0 {
				lStep.TestConnectivity.PingCount = 10
			}
			if lStep.TestConnectivity.IPerfSeconds == 0 {
				lStep.TestConnectivity.IPerfSeconds = 5
			}
			if lStep.TestConnectivity.IPerfSpeed == 0 {
				lStep.TestConnectivity.IPerfSpeed = 0.01 // 7000 // TODO autodetect for VS
			}

			steps = append(steps, lStep.TestConnectivity)

			num++
		}

		if num == 0 {
			return nil, errors.Errorf("step %d: no action specified", stepIdx)
		}
		if num > 1 {
			return nil, errors.Errorf("step %d: only single action could be specified", stepIdx)
		}
	}

	return &Test{
		labels: lTest.Labels,
		steps:  steps,
	}, nil
}

func (r *Runner) loadAbbr(abbr string, ignoreNotDefined bool, blocks map[string]string) (Step, error) {
	parts := strings.Fields(abbr)
	for idx, part := range parts {
		if strings.HasPrefix(part, "%") {
			blockName := strings.TrimPrefix(part, "%")

			context, exist := blocks[blockName]
			if !exist {
				return nil, errors.Errorf("block %s not defined", blockName)
			}

			parts[idx] = context
		}
	}

	loader := func() (*apiabbr.Enforcer, error) {
		enf, err := apiabbr.NewEnforcer(ignoreNotDefined)
		if err != nil {
			return nil, errors.Wrapf(err, "error creating enforcer")
		}

		if err := enf.Load(abbr); err != nil {
			return nil, errors.Wrapf(err, "error loading abbr %s", abbr)
		}

		return enf, nil
	}

	if _, err := loader(); err != nil {
		return nil, errors.Wrapf(err, "error loading abbr %s", abbr)
	}

	return &StepAPIAbbr{loader}, nil
}
