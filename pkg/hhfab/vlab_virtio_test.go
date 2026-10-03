// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
)

// Servers carry the VLAB virtio naming rule too, so they can move to
// virtio-net without their interfaces changing names.
func TestServerIgnitionHasVLABVirtioNames(t *testing.T) {
	t.Parallel()

	f := fabapi.Fabricator{Spec: fabapi.FabricatorSpec{Config: fab.DefaultConfig}}

	but, ign, err := serverIgnition(f, VM{Name: "server-01", Type: VMTypeServer})
	require.NoError(t, err)
	require.NotEmpty(t, ign, "the butane has to translate")

	require.Contains(t, but, "- path: "+flatcar.VLABVirtioNamesLinkPath)
	for line := range strings.SplitSeq(strings.TrimSpace(flatcar.VLABVirtioNamesLink), "\n") {
		require.Contains(t, but, "          "+line, "every line of the rule must sit inside the inline block")
	}
}
