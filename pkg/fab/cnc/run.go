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

package cnc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
)

var RunOpsList = []RunOp{
	&InstallFile{},
	&ExecCommand{},
	&WaitURL{},
	&PushOCI{},
	&WaitKube{},
}

//
// RunOp InstallFile
//

type InstallFile struct {
	Name       string      `json:"name,omitempty"`
	Target     string      `json:"target,omitempty"`
	TargetName string      `json:"targetName,omitempty"`
	Mode       os.FileMode `json:"mode,omitempty"`
	MkdirMode  os.FileMode `json:"mkdirMode,omitempty"`
}

var _ RunOp = (*InstallFile)(nil)

func (op *InstallFile) Hydrate() error {
	if op.Name == "" {
		return errors.New("name is empty")
	}
	if op.Target == "" {
		return errors.New("dest is empty")
	}
	if op.TargetName == "" {
		op.TargetName = op.Name
	}
	if op.Mode == 0 {
		op.Mode = 0o644
	}
	if op.MkdirMode == 0 {
		op.MkdirMode = 0o755
	}

	return nil
}

func (op *InstallFile) TargetPath() string {
	return filepath.Join(op.Target, op.TargetName)
}

func (op *InstallFile) Summary() string {
	return fmt.Sprintf("file %s", filepath.Join(op.Target, op.TargetName))
}

func (op *InstallFile) Run(basedir string) error {
	err := os.MkdirAll(op.Target, op.MkdirMode)
	if err != nil {
		return errors.Wrapf(err, "failed to create directory %s", op.Target)
	}

	content, err := os.ReadFile(filepath.Join(basedir, op.Name))
	if err != nil {
		return errors.Wrapf(err, "failed to read file %s", op.Name)
	}

	return errors.Wrapf(os.WriteFile(op.TargetPath(), content, op.Mode), "failed to write file %s", op.TargetName)
}

//
// RunOp ExecCommand
//

type ExecCommand struct {
	Name string   `json:"name,omitempty"`
	Args []string `json:"args,omitempty"`
	Env  []string `json:"env,omitempty"`
	Dir  string   `json:"dir,omitempty"`
}

var _ RunOp = (*ExecCommand)(nil)

func (op *ExecCommand) Hydrate() error {
	if op.Name == "" {
		return errors.New("name is empty")
	}

	return nil
}

func (op *ExecCommand) Summary() string {
	return fmt.Sprintf("exec %s", op.Name)
}

func (op *ExecCommand) Run(basedir string) error {
	cmd := exec.Command(op.Name, op.Args...) //nolint:gosec

	cmd.Dir = basedir
	cmd.Env = append(os.Environ(), op.Env...)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout

	return errors.Wrapf(cmd.Run(), "failed to execute command %s", op.Name)
}

//
// RunOp WaitURL
//

type WaitURL struct {
	Wait       WaitParams `json:"wait,omitempty"`
	URL        string     `json:"url,omitempty"`
	StatusCode int        `json:"statusCode,omitempty"`
}

var _ RunOp = (*WaitURL)(nil)

func (op *WaitURL) Hydrate() error {
	_, err := url.ParseRequestURI(op.URL)
	if err != nil {
		return errors.Wrapf(err, "invalid url %s", op.URL)
	}
	if op.StatusCode == 0 {
		op.StatusCode = http.StatusOK
	}

	return op.Wait.Hydrate()
}

func (op *WaitURL) Summary() string {
	return fmt.Sprintf("wait %s", op.URL)
}

func (op *WaitURL) Run(_ string) error {
	return op.Wait.Wait(func() error {
		resp, err := http.Get(op.URL) //nolint:noctx
		if err != nil {
			return errors.Wrapf(err, "failed to get %s", op.URL)
		}
		defer resp.Body.Close()

		if resp.StatusCode != op.StatusCode {
			return errors.Errorf("status code %d, expected %d", resp.StatusCode, op.StatusCode)
		}

		return nil
	})
}

//
// RunOp PushOCI
//

type PushOCI struct {
	Name   string `json:"name,omitempty"`
	Target Ref    `json:"target,omitempty"`
}

var _ RunOp = (*PushOCI)(nil)

func (op *PushOCI) Hydrate() error {
	if op.Name == "" {
		return errors.New("name is empty")
	}

	return op.Target.StrictValidate()
}

func (op *PushOCI) Summary() string {
	return fmt.Sprintf("push %s", op.Target.Name+":"+op.Target.Tag)
}

func (op *PushOCI) Run(basedir string) error {
	err := copyOCI("oci:"+filepath.Join(basedir, op.Name), "docker://"+op.Target.String(), false)
	if err != nil {
		return err
	}

	return nil
}

//
// RunOp WaitKubeConditionReady
//

// temporary implementation, will be replaced with a native k8s go client

type WaitKube struct {
	Name            string        `json:"name,omitempty"`
	Timeout         time.Duration `json:"timeout,omitempty"`
	TimeoutResource time.Duration `json:"timeoutResource,omitempty"`
	Interval        time.Duration `json:"interval,omitempty"`
}

var _ RunOp = (*WaitKube)(nil)

func (op *WaitKube) Hydrate() error {
	if op.Name == "" {
		return errors.New("name is empty")
	}
	if !strings.Contains(op.Name, "/") {
		return errors.New("name should be in form resourcetype/name")
	}
	if op.Timeout == 0 {
		op.Timeout = 10 * time.Minute
	}
	if op.TimeoutResource == 0 {
		op.TimeoutResource = 10 * time.Minute
	}
	if op.Interval == 0 {
		op.Interval = 3 * time.Second
	}

	return nil
}

func (op *WaitKube) Summary() string {
	return fmt.Sprintf("wait %s", op.Name)
}

func (op *WaitKube) waitForResource() error {
	start := time.Now()
	for {
		if time.Since(start) > op.TimeoutResource {
			return errors.Errorf("timeout")
		}

		time.Sleep(op.Interval)

		cmd := exec.Command("kubectl", "get", op.Name) //nolint:gosec

		if slog.Default().Enabled(context.TODO(), slog.LevelDebug) {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stdout
		}

		if cmd.Run() == nil {
			return nil
		}
	}
}

func (op *WaitKube) Run(_ string) error {
	// wait for resource existence first
	err := op.waitForResource()
	if err != nil {
		return errors.Wrapf(err, "error waiting for resource %s", op.Name)
	}

	var cmd *exec.Cmd
	if strings.HasPrefix(op.Name, "deployment") {
		cmd = exec.Command("kubectl", //nolint:gosec
			"wait",
			"--for=condition=available",
			"--timeout="+op.Timeout.String(), op.Name) //nolint:goconst
	} else if strings.HasPrefix(op.Name, "job") {
		cmd = exec.Command("kubectl", //nolint:gosec
			"wait",
			"--for=condition=complete",
			"--timeout="+op.Timeout.String(), op.Name)
	} else if strings.HasPrefix(op.Name, "daemonset") {
		cmd = exec.Command("kubectl", //nolint:gosec
			"rollout", "status",
			"--timeout="+op.Timeout.String(), op.Name)
	} else if strings.HasPrefix(op.Name, "controlagent") {
		cmd = exec.Command("kubectl", //nolint:gosec
			"wait",
			"--for=condition=applied",
			"--timeout="+op.Timeout.String(), op.Name)
	}
	// otherwise we've just waited for the resource to exist

	if cmd != nil {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		return errors.Wrapf(cmd.Run(), "error waiting for condition %s", op.Name)
	}

	return nil
}
