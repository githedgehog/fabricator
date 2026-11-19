// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
)

func TestBuildNATConfigIdleTimeout(t *testing.T) {
	portForward := []gwapi.PeeringNATPortForwardEntry{{Protocol: gwapi.PeeringNATProtocolTCP, Port: "5201", As: "15201"}}

	for _, tt := range []struct {
		name        string
		idleTimeout time.Duration
		want        time.Duration
	}{
		{name: "set", idleTimeout: 30 * time.Minute, want: 30 * time.Minute},
		{name: "zero keeps the default", idleTimeout: 0, want: 5 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			masq, err := buildNATConfig(NATModeMasquerade, nil, tt.idleTimeout)
			require.NoError(t, err)
			require.NotNil(t, masq.Masquerade)
			require.Equal(t, tt.want, masq.Masquerade.IdleTimeout.Duration)

			pf, err := buildNATConfig(NATModePortForward, portForward, tt.idleTimeout)
			require.NoError(t, err)
			require.NotNil(t, pf.PortForward)
			require.Equal(t, tt.want, pf.PortForward.IdleTimeout.Duration)
		})
	}
}
