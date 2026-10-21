// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

const stagedTestWiring = `
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: default
spec:
  leafASNStart: 65101
  leafASNEnd: 65533
  domains:
    default:
      spineASN: 65100
      gatewayASN: 65534
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: fab-b
  annotations:
    hhfab.githedgehog.com/staged: "true"
spec:
  leafASNStart: 64600
  leafASNEnd: 64699
  domains:
    default:
      spineASN: 64700
      gatewayASN: 64701
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-01
spec:
  role: server-leaf
  profile: vs
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-b1
  annotations:
    hhfab.githedgehog.com/staged: "true"
spec:
  role: server-leaf
  profile: vs
  topology:
    fabric: fab-b
`

func TestStagedObjects(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	l := apiutil.NewLoader()
	require.NoError(t, l.LoadAddWiring(ctx, []byte(stagedTestWiring)))
	kube := l.GetClient()

	staged, err := apiutil.ListStaged(ctx, kube)
	require.NoError(t, err)
	names := []string{}
	for _, obj := range staged {
		names = append(names, obj.GetName())
	}
	require.Equal(t, []string{"fab-b", "leaf-b1"}, names, "staged objects come in creation order, Fabrics before Switches")

	all := &bytes.Buffer{}
	require.NoError(t, apiutil.PrintInclude(ctx, kube, all))
	require.Contains(t, all.String(), "name: leaf-b1")
	require.Contains(t, all.String(), "name: fab-b")

	installed := &bytes.Buffer{}
	require.NoError(t, apiutil.PrintIncludeInstalled(ctx, kube, installed))
	require.Contains(t, installed.String(), "name: leaf-01")
	require.Contains(t, installed.String(), "name: default")
	require.NotContains(t, installed.String(), "leaf-b1")
	require.NotContains(t, installed.String(), "fab-b")
	require.NotContains(t, installed.String(), apiutil.StagedAnnotation)
}
