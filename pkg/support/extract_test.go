// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/support"
	kyaml "sigs.k8s.io/yaml"
)

func TestExtract(t *testing.T) {
	pod := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: fabric-ctrl-1\n  namespace: fab\n"
	role := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: system:node\n"
	agent := "apiVersion: agent.githedgehog.com/v1beta1\nkind: Agent\nmetadata:\n  name: leaf-01\n  namespace: default\n"
	deploy := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: fabric-ctrl\n  namespace: fab\n"
	chart := "apiVersion: helm.cattle.io/v1\nkind: HelmChart\nmetadata:\n  name: fabric\n  namespace: fab\n"
	unnamed := "apiVersion: v1\nkind: Pod\nmetadata: {}\n"
	// the non-ASCII characters seen in the gateway outputs and Go durations, extracted as is unless asked for ASCII
	decorated := "dataplane(✔)# ━━ summary ━━\n NAME │ VALUE\n ■ took 12.5µs ┣━┫ ─┼─ ✓\n"

	dump := &support.Dump{
		Version:   support.CurrentVersion.String(),
		Name:      "test",
		Resources: strings.Join([]string{pod, role, agent, deploy, chart, unnamed}, "---\n"),
		PodLogs: map[string]map[string]support.PodLogs{
			"fab": {"fabric-ctrl-1": {"manager": {Current: "\x1b[2mcurrent\x1b[0m\n", Previous: "previous\n"}, "init": {Current: "done\n"}}},
		},
		GatewayInsights: map[string]map[string]support.ExecOutputs{
			"gw-1": {
				"frr: show bgp summary": {Stdout: "\x1b[1;32msummary\x1b[0m\n"},
				"dataplane: show tech":  {Stdout: decorated, Stderr: "warning\n", Error: "exit 1"},
			},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test"+support.FileExt)
	require.NoError(t, support.WriteFile(path, dump, 0o600))
	require.NoError(t, support.Extract(path, support.ExtractOpts{StripColors: true}))

	out := filepath.Join(dir, "test")
	for file, content := range map[string]string{
		"resources/k8s.io/v1.Pod/fab/fabric-ctrl-1.yaml":                                     pod,
		"resources/k8s.io/v1.rbac.authorization.k8s.io.ClusterRole/system-node.yaml":         role,
		"resources/githedgehog.com/v1beta1.agent.githedgehog.com.Agent/default/leaf-01.yaml": agent,
		"resources/k8s.io/v1.apps.Deployment/fab/fabric-ctrl.yaml":                           deploy,
		"resources/cattle.io/v1.helm.cattle.io.HelmChart/fab/fabric.yaml":                    chart,
		"resources/unparsed/1.yaml":                                                          unnamed,
		"podlogs/fab/fabric-ctrl-1/manager.current.log":                                      "current\n",
		"podlogs/fab/fabric-ctrl-1/manager.previous.log":                                     "previous\n",
		"podlogs/fab/fabric-ctrl-1/init.current.log":                                         "done\n",
		"insights/gw-1/frr-show-bgp-summary.stdout.txt":                                      "summary\n",
		"insights/gw-1/dataplane-show-tech.stdout.txt":                                       decorated,
		"insights/gw-1/dataplane-show-tech.stderr.txt":                                       "warning\n",
		"insights/gw-1/dataplane-show-tech.error.txt":                                        "exit 1",
	} {
		data, err := os.ReadFile(filepath.Join(out, file))
		require.NoError(t, err, file)
		require.Equal(t, content, string(data), file)
	}
	require.NoFileExists(t, filepath.Join(out, "podlogs/fab/fabric-ctrl-1/init.previous.log"))

	data, err := os.ReadFile(filepath.Join(out, "support.yaml"))
	require.NoError(t, err)
	meta := map[string]any{}
	require.NoError(t, kyaml.Unmarshal(data, &meta))
	require.Equal(t, "test", meta["name"])
	require.NotContains(t, meta, "resources")
	require.Equal(t, map[string]any{
		"objects":    float64(5),
		"kinds":      map[string]any{"v1beta1.agent.githedgehog.com.Agent": float64(1)},
		"containers": float64(2),
		"logFiles":   float64(3),
		"gateways":   float64(1),
		"commands":   float64(2),
		"errors":     float64(1),
		"unparsed":   float64(1),
	}, meta["summary"])

	require.ErrorContains(t, support.Extract(path, support.ExtractOpts{}), "already exists")

	// the colors are kept as is when not stripped
	raw := filepath.Join(dir, "raw")
	require.NoError(t, support.Extract(path, support.ExtractOpts{OutDir: raw}))
	for file, content := range map[string]string{
		"podlogs/fab/fabric-ctrl-1/manager.current.log": "\x1b[2mcurrent\x1b[0m\n",
		"insights/gw-1/frr-show-bgp-summary.stdout.txt": "\x1b[1;32msummary\x1b[0m\n",
	} {
		data, err := os.ReadFile(filepath.Join(raw, file))
		require.NoError(t, err, file)
		require.Equal(t, content, string(data), file)
	}

	// the decorations become ASCII when asked for, while the characters not known to be there are kept as is
	ascii := filepath.Join(dir, "ascii")
	require.NoError(t, support.Extract(path, support.ExtractOpts{OutDir: ascii, StripColors: true, ASCII: true}))
	for file, content := range map[string]string{
		"insights/gw-1/dataplane-show-tech.stdout.txt":  "dataplane(v)# -- summary --\n NAME | VALUE\n * took 12.5us +-+ -+- ✓\n",
		"insights/gw-1/frr-show-bgp-summary.stdout.txt": "summary\n",
	} {
		data, err := os.ReadFile(filepath.Join(ascii, file))
		require.NoError(t, err, file)
		require.Equal(t, content, string(data), file)
	}
}
