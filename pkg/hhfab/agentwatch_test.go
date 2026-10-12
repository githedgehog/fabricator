// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDiffAgentSpecs(t *testing.T) {
	before := AgentSpecs{
		"leaf-01": {
			"benchTouch": 1,
			"catalog":    map[string]any{"irbVLANs": map[string]any{"vpc-03": 3004, "ext@x": 3005, "vpc-01": 3000}},
		},
		"leaf-02": {"benchTouch": 1, "role": "server-leaf"},
	}
	after := AgentSpecs{
		"leaf-01": {
			"benchTouch": 2,
			"catalog":    map[string]any{"irbVLANs": map[string]any{"vpc-03": 3005, "ext@x": 3004, "vpc-01": 3000}},
		},
		"leaf-02": {"benchTouch": 2, "role": "server-leaf"},
	}

	require.Equal(t, []string{
		"leaf-01: spec.catalog.irbVLANs.ext@x: 3005 -> 3004",
		"leaf-01: spec.catalog.irbVLANs.vpc-03: 3004 -> 3005",
	}, DiffAgentSpecs(before, after, IgnoreBenchTouch))

	require.Empty(t, DiffAgentSpecs(before, before, nil))

	// a key present on one side only, and an ignore scoped to one agent
	after["leaf-02"]["reinstall"] = "id"
	got := DiffAgentSpecs(before, after, func(agent, path string) bool {
		return IgnoreBenchTouch(agent, path) || (agent == "leaf-02" && path == "reinstall")
	})
	require.Len(t, got, 2)
	require.Equal(t, []string{"leaf-02: agent disappeared"}, DiffAgentSpecs(before, AgentSpecs{"leaf-01": before["leaf-01"]}, nil))
}

func TestAgentsNotApplied(t *testing.T) {
	agent := func(name string, gen, applied int64, status kmetav1.ConditionStatus, reason string) agentapi.Agent {
		ag := agentapi.Agent{}
		ag.Name = name
		ag.Generation = gen
		ag.Status.LastAppliedGen = applied
		if status != "" {
			ag.Status.Conditions = []kmetav1.Condition{{Type: "Applied", Status: status, Reason: reason}}
		}

		return ag
	}

	require.Empty(t, AgentsNotApplied([]agentapi.Agent{agent("leaf-01", 5, 5, kmetav1.ConditionTrue, "ApplySucceeded")}))

	got := AgentsNotApplied([]agentapi.Agent{
		agent("leaf-01", 5, 5, kmetav1.ConditionFalse, "ApplyPending"),
		agent("leaf-02", 6, 5, kmetav1.ConditionTrue, "ApplySucceeded"),
		agent("leaf-03", 3, 3, "", ""),
	})
	require.Len(t, got, 3)
	require.Contains(t, got[0], "Applied=False reason=ApplyPending")
	require.Contains(t, got[1], "lastAppliedGen=5 generation=6")
	require.Contains(t, got[2], "no Applied condition")
}
