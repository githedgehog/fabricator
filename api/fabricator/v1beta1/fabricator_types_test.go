// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

func TestAgentIntervals(t *testing.T) {
	ctx := t.Context()

	data, err := fab.InitConfig(ctx, fab.InitConfigInput{Dev: true})
	require.NoError(t, err)

	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAdd(ctx, apiutil.FabricatorGVKs, data))

	base, _, _, err := fab.GetFabAndNodes(ctx, l.GetClient(), fab.GetFabAndNodesOpts{AllowNotHydrated: true})
	require.NoError(t, err)

	for _, tt := range []struct {
		name              string
		heartbeat         uint32
		enforce           uint32
		err               string
		expectedHeartbeat uint32
		expectedEnforce   uint32
	}{
		{name: "unset", expectedHeartbeat: 15, expectedEnforce: 120},
		{name: "min", heartbeat: 15, enforce: 120, expectedHeartbeat: 15, expectedEnforce: 120},
		{name: "max", heartbeat: 3600, enforce: 86400, expectedHeartbeat: 3600, expectedEnforce: 86400},
		{name: "heartbeat-too-low", heartbeat: 14, err: "agent heartbeat interval 14 must be between 15 and 3600 seconds"},
		{name: "heartbeat-too-high", heartbeat: 3601, err: "agent heartbeat interval 3601 must be between 15 and 3600 seconds"},
		{name: "enforce-too-low", enforce: 119, err: "agent enforce interval 119 must be between 120 and 86400 seconds"},
		{name: "enforce-too-high", enforce: 86401, err: "agent enforce interval 86401 must be between 120 and 86400 seconds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := base.DeepCopy()
			f.Spec.Config.Fabric.AgentHeartbeatInterval = tt.heartbeat
			f.Spec.Config.Fabric.AgentEnforceInterval = tt.enforce

			err := f.Validate(ctx)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectedHeartbeat, f.Spec.Config.Fabric.AgentHeartbeatInterval)
			require.Equal(t, tt.expectedEnforce, f.Spec.Config.Fabric.AgentEnforceInterval)
		})
	}
}

func TestAgentHeartbeatStale(t *testing.T) {
	for interval, expected := range map[uint32]time.Duration{
		0:    time.Minute,
		10:   time.Minute,
		15:   time.Minute,
		60:   4 * time.Minute,
		3600: 4 * time.Hour,
	} {
		require.Equal(t, expected, fabapi.AgentHeartbeatStale(interval), "interval %d", interval)
	}
}
