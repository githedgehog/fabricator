// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/support"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kyaml "sigs.k8s.io/yaml"
)

func TestCurrentVersion(t *testing.T) {
	supported := support.SupportedVersion.Check(support.CurrentVersion)
	require.True(t, supported, "current version should be supported")
}

func TestMarshalCompressed(t *testing.T) {
	dump := &support.Dump{
		Version:   support.CurrentVersion.String(),
		Name:      "test",
		CreatedAt: kmetav1.NewTime(time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)),
		Resources: "kind: Node\n---\nkind: Pod\n",
		PodLogs: map[string]map[string]support.PodLogs{
			"fab": {"fabric-ctrl": {"manager": {Current: "line 1\nline 2\n", Previous: "\x1b[31mcolored\x1b[0m\n"}}},
		},
		GatewayInsights: map[string]map[string]support.ExecOutputs{
			"gw-1": {"frr: show bgp summary": {Stdout: "summary\n"}},
		},
	}

	buf := &bytes.Buffer{}
	require.NoError(t, support.Marshal(buf, dump))
	require.True(t, bytes.HasPrefix(buf.Bytes(), []byte{0x1f, 0x8b}), "dump should be gzip compressed")

	got := &support.Dump{}
	require.NoError(t, support.Unmarshal(buf, got))
	require.Equal(t, dump.Name, got.Name)
	require.True(t, dump.CreatedAt.Equal(&got.CreatedAt))
	require.Equal(t, dump.Resources, got.Resources)
	require.Equal(t, dump.PodLogs, got.PodLogs)
	require.Equal(t, dump.GatewayInsights, got.GatewayInsights)
}

func TestUnmarshalPlain(t *testing.T) {
	// the way the dumps were made before they were compressed
	data, err := kyaml.Marshal(map[string]any{
		"version":   support.CurrentVersion.String(),
		"name":      "test",
		"createdAt": kmetav1.NewTime(time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)),
		"resources": "kind: Node\n",
	})
	require.NoError(t, err)

	got := &support.Dump{}
	require.NoError(t, support.Unmarshal(bytes.NewReader(data), got))
	require.Equal(t, "test", got.Name)
	require.True(t, time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC).Equal(got.CreatedAt.Time))
	require.Equal(t, "kind: Node\n", got.Resources)
}

func TestUnmarshalUnsupportedVersion(t *testing.T) {
	// a dump of an unsupported version is reported as such, not by the fields it's expected to have unknown
	data := []byte("version: 0.2.0\nname: test\nnewField: value\n")

	err := support.Unmarshal(bytes.NewReader(data), &support.Dump{})
	require.ErrorContains(t, err, "is not supported")
}
