// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOutputStable renders a topology with parallel spine-leaf and mesh links several times, as
// the writers group links in maps and must still give the same diagram on every run
func TestOutputStable(t *testing.T) {
	link := func(source, target, linkType, sourcePort, targetPort string) Link {
		l := testLink(source, target, linkType)
		l.Properties[PropSourcePort] = source + "/" + sourcePort
		l.Properties[PropTargetPort] = target + "/" + targetPort

		return l
	}
	topo := Topology{
		Nodes: []Node{
			testNode("spine-1", NodeTypeSwitch, "default", SwitchRoleSpine),
			testNode("spine-2", NodeTypeSwitch, "default", SwitchRoleSpine),
			testNode("leaf-1", NodeTypeSwitch, "default", testLeafRole),
			testNode("leaf-2", NodeTypeSwitch, "default", testLeafRole),
			testNode("leaf-3", NodeTypeSwitch, "default", testLeafRole),
		},
		Links: []Link{
			link("spine-1", "leaf-1", EdgeTypeFabric, "E1/1", "E1/1"),
			link("spine-1", "leaf-1", EdgeTypeFabric, "E1/2", "E1/2"),
			link("spine-1", "leaf-2", EdgeTypeFabric, "E1/3", "E1/1"),
			link("spine-1", "leaf-2", EdgeTypeFabric, "E1/4", "E1/2"),
			link("spine-2", "leaf-1", EdgeTypeFabric, "E1/1", "E1/3"),
			link("spine-2", "leaf-1", EdgeTypeFabric, "E1/2", "E1/4"),
			link("spine-2", "leaf-2", EdgeTypeFabric, "E1/3", "E1/3"),
			link("spine-2", "leaf-2", EdgeTypeFabric, "E1/4", "E1/4"),
			link("leaf-1", "leaf-3", EdgeTypeMesh, "E1/5", "E1/1"),
			link("leaf-1", "leaf-3", EdgeTypeMesh, "E1/6", "E1/2"),
			link("leaf-2", "leaf-3", EdgeTypeMesh, "E1/5", "E1/3"),
			link("leaf-2", "leaf-3", EdgeTypeMesh, "E1/6", "E1/4"),
		},
	}

	dot := generateDOT(topo)
	drawio := createDrawioModel(topo, GetStyle(StyleTypeDefault))
	for range 20 {
		require.Equal(t, dot, generateDOT(topo))
		require.Equal(t, drawio, createDrawioModel(topo, GetStyle(StyleTypeDefault)))
	}
}
