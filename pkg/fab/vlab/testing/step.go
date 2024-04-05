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
	"log/slog"
	"time"

	"github.com/melbahja/goph"
	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/pkg/client/apiabbr"
	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VLABStepHelper struct {
	kube       client.WithWatch
	sshPorts   map[string]uint
	sshKeyPath string
}

var _ StepHelper = (*VLABStepHelper)(nil)

func NewVLABStepHelper(kube client.WithWatch, sshPorts map[string]uint, sshKeyPath string) *VLABStepHelper {
	return &VLABStepHelper{
		kube:       kube,
		sshPorts:   sshPorts,
		sshKeyPath: sshKeyPath,
	}
}

func (h *VLABStepHelper) Kube() client.Client {
	return h.kube
}

func (h *VLABStepHelper) ServerExec(ctx context.Context, server, cmd string, timeout time.Duration) (string, error) {
	port, ok := h.sshPorts[server]
	if !ok {
		return "", errors.Errorf("ssh port for server %s not found", server)
	}

	// TODO think about default timeouts
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	auth, err := goph.Key(h.sshKeyPath, "")
	if err != nil {
		return "", errors.Wrapf(err, "error loading SSH key %s", h.sshKeyPath)
	}

	client, err := goph.NewConn(&goph.Config{
		User:     "core",
		Addr:     "127.0.0.1",
		Port:     port,
		Auth:     auth,
		Timeout:  5 * time.Second,             // TODO think about TCP dial timeout
		Callback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	})
	if err != nil {
		return "", errors.Wrapf(err, "error creating SSH client for server %s", server)
	}

	// TODO autoinject client side timeout?
	out, err := client.RunContext(ctx, cmd)
	if err != nil {
		return string(out), errors.Wrapf(err, "error running command on server %s using ssh", server)
	}

	return string(out), nil
}

type StepAPIAbbr struct {
	loader func() (*apiabbr.Enforcer, error)
}

var _ Step = (*StepAPIAbbr)(nil)

func (s *StepAPIAbbr) Run(ctx context.Context, h StepHelper) error {
	slog.Debug("running api abbr step")

	enf, err := s.loader()
	if err != nil {
		return err
	}

	return errors.Wrapf(enf.Enforce(ctx, h.Kube()), "error enforcing")
}

type StepNetconf struct{}

var _ Step = (*StepNetconf)(nil)

func (s *StepNetconf) Run(ctx context.Context, h StepHelper) error {
	slog.Debug("running netconf step")

	// TODO impl

	return nil
}

type StepTestConnectivity struct{}

var _ Step = (*StepTestConnectivity)(nil)

func (s *StepTestConnectivity) Run(ctx context.Context, h StepHelper) error {
	slog.Debug("running test connectivity step")

	// TODO impl

	return nil
}
