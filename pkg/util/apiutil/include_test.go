// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
)

func TestValidateFabrics(t *testing.T) {
	t.Parallel()

	cfg := &meta.FabricConfig{SpineASN: 65100, LeafASNStart: 65101, LeafASNEnd: 65533, GatewayASN: 65534}

	for _, test := range []struct {
		name   string
		wiring string
		err    string
	}{
		{
			name: "disjoint from default",
			wiring: `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: fab-b
spec:
  leafASNStart: 64600
  leafASNEnd: 64699
  domains:
    default:
      spineASN: 64700
      gatewayASN: 64701
`,
		},
		{
			name: "leaf range overlaps default",
			wiring: `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: fab-b
spec:
  leafASNStart: 65500
  leafASNEnd: 65600
  domains:
    default:
      spineASN: 64700
      gatewayASN: 64701
`,
			err: "overlaps with fabric default",
		},
		{
			name: "spine ASN is the default gateway ASN",
			wiring: `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: fab-b
spec:
  leafASNStart: 64600
  leafASNEnd: 64699
  domains:
    default:
      spineASN: 65534
      gatewayASN: 64701
`,
			err: "already used by fabric default",
		},
		{
			name: "default in the wiring",
			wiring: `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: default
spec:
  leafASNStart: 65101
  leafASNEnd: 65533
`,
			err: "must not be in the wiring",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			l := NewLoader()
			require.NoError(t, l.LoadAdd(t.Context(), FabricGVKs, []byte(test.wiring)))

			err := validateFabrics(t.Context(), l.GetClient(), cfg)
			if test.err == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.err)
			}
		})
	}
}
