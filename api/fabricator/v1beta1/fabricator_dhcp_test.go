// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
)

func TestManagementDHCPSubnetSpec(t *testing.T) {
	newFab := func() *Fabricator {
		f := &Fabricator{}
		f.Spec.Config.Control.ManagementSubnet = "172.30.0.0/21"
		f.Spec.Config.Control.VIP = "172.30.0.1/32"
		f.Spec.Config.Fabric.ManagementDHCPStart = "172.30.4.0"
		f.Spec.Config.Fabric.ManagementDHCPEnd = "172.30.7.254"

		return f
	}

	t.Run("spec", func(t *testing.T) {
		f := newFab()
		f.Spec.Config.Control.ManagementSubnetStatic = map[string]string{"0c:20:12:fe:00:01": "172.30.1.10"}

		spec, err := f.ManagementDHCPSubnetSpec()
		require.NoError(t, err)
		require.NoError(t, spec.Validate())
		assert.Equal(t, "172.30.0.0/21", spec.CIDRBlock)
		assert.Equal(t, "172.30.0.1", spec.Gateway)
		assert.Equal(t, "http://172.30.0.1:32000", spec.ZTPBaseURL)
		assert.Equal(t, map[string]dhcpapi.DHCPSubnetStatic{"0c:20:12:fe:00:01": {IP: "172.30.1.10"}}, spec.Static)
	})

	for _, tt := range []struct {
		name   string
		modify func(f *Fabricator)
	}{
		{
			name:   "start after end",
			modify: func(f *Fabricator) { f.Spec.Config.Fabric.ManagementDHCPStart = "172.30.7.255" },
		},
		{
			name: "invalid static mac",
			modify: func(f *Fabricator) {
				f.Spec.Config.Control.ManagementSubnetStatic = map[string]string{"garbage": "172.30.1.10"}
			},
		},
		{
			name: "static outside the subnet",
			modify: func(f *Fabricator) {
				f.Spec.Config.Control.ManagementSubnetStatic = map[string]string{"0c:20:12:fe:00:01": "10.0.0.10"}
			},
		},
		{
			name: "static is the gateway",
			modify: func(f *Fabricator) {
				f.Spec.Config.Control.ManagementSubnetStatic = map[string]string{"0c:20:12:fe:00:01": "172.30.0.1"}
			},
		},
		{
			name: "duplicate static ip",
			modify: func(f *Fabricator) {
				f.Spec.Config.Control.ManagementSubnetStatic = map[string]string{
					"0c:20:12:fe:00:01": "172.30.1.10",
					"0c:20:12:fe:00:02": "172.30.1.10",
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFab()
			tt.modify(f)

			spec, err := f.ManagementDHCPSubnetSpec()
			require.NoError(t, err)
			require.Error(t, spec.Validate())
		})
	}
}
