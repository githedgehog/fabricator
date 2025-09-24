// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"
	"net/netip"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func (c *Config) SSHVM(ctx context.Context, vlab *VLAB, vm VM) (*sshutil.Config, error) {
	ssh := &sshutil.Config{
		SSHKey: vlab.SSHKey,
	}

	switch vm.Type {
	case VMTypeServer, VMTypeControl, VMTypeExternal:
		ssh.Remote = sshutil.Remote{
			User: "core",
			Host: "127.0.0.1",
			Port: getSSHPort(vm.ID),
		}
	case VMTypeSwitch:
		sw := &wiringapi.Switch{}
		if err := c.Client.Get(ctx, kclient.ObjectKey{Name: vm.Name, Namespace: kmetav1.NamespaceDefault}, sw); err != nil {
			return nil, fmt.Errorf("getting switch object: %w", err) //nolint:goerr113
		}

		if sw.Spec.IP == "" {
			return nil, fmt.Errorf("switch IP not found: %s", vm.Name) //nolint:goerr113
		}

		swIP, err := netip.ParsePrefix(sw.Spec.IP)
		if err != nil {
			return nil, fmt.Errorf("parsing switch IP: %w", err)
		}

		ssh.Remote = sshutil.Remote{
			User: "admin",
			Host: swIP.Addr().String(),
			Port: 22,
		}
	case VMTypeGateway:
		nodeIP := ""
		for _, node := range c.Nodes {
			if node.Name == vm.Name {
				prefix, err := node.Spec.Management.IP.Parse()
				if err != nil {
					return nil, fmt.Errorf("parsing node %s management IP: %w", vm.Name, err)
				}
				nodeIP = prefix.Addr().String()

				break
			}
		}

		if nodeIP == "" {
			return nil, fmt.Errorf("node %s not found", vm.Name) //nolint:err113
		}

		ssh.Remote = sshutil.Remote{
			User: "core",
			Host: nodeIP,
			Port: 22,
		}
	}

	if ssh.Remote.Host != "127.0.0.1" {
		controlSSHPort := uint(0)
		for _, vm := range vlab.VMs {
			if vm.Type == VMTypeControl {
				controlSSHPort = getSSHPort(vm.ID)

				break
			}
		}

		if controlSSHPort == 0 {
			return nil, fmt.Errorf("control VM not found") //nolint:err113
		}

		ssh.Proxy = &sshutil.Remote{
			User: "core",
			Host: "127.0.0.1",
			Port: controlSSHPort,
		}
	}

	return ssh, nil
}

func (c *Config) SSH(ctx context.Context, vlab *VLAB, target string) (*sshutil.Config, error) {
	for _, vm := range vlab.VMs {
		if vm.Name != target {
			continue
		}

		return c.SSHVM(ctx, vlab, vm)
	}

	return nil, fmt.Errorf("unknown target: %s", target) //nolint:err113
}
