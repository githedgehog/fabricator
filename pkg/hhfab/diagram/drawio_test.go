// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testNode(id, nodeType, fabric, role string) Node {
	return Node{
		ID:    id,
		Type:  nodeType,
		Label: id,
		Properties: map[string]string{
			PropFabric:  fabric,
			PropDomains: "default",
			PropRole:    role,
		},
	}
}

// leaves are the switches that aren't spines
const testLeafRole = "server-leaf"

func testLink(source, target, linkType string) Link {
	return Link{Source: source, Target: target, Type: linkType, Properties: map[string]string{}}
}

func testCells(t *testing.T, topo Topology) map[string]*Geometry {
	t.Helper()

	cells := map[string]*Geometry{}
	for _, cell := range createDrawioModel(topo, GetStyle(StyleTypeDefault)).Root.MxCell {
		if cell.Geometry != nil {
			cells[cell.ID] = cell.Geometry
		}
	}

	return cells
}

func testMesh(leaves ...string) []Link {
	links := []Link{}
	for i := range leaves {
		for j := i + 1; j < len(leaves); j++ {
			links = append(links, testLink(leaves[i], leaves[j], EdgeTypeMesh))
		}
	}

	return links
}

func requireClearOfBoxes(t *testing.T, cells map[string]*Geometry) {
	t.Helper()

	logo := cells["hedgehog_logo"]
	require.NotNil(t, logo)
	for id, box := range cells {
		if !strings.HasPrefix(id, "topology_box_") {
			continue
		}
		overlaps := logo.X < box.X+float64(box.Width) && box.X < logo.X+float64(logo.Width) &&
			logo.Y < box.Y+float64(box.Height) && box.Y < logo.Y+float64(logo.Height)
		require.False(t, overlaps, "logo overlaps %s", id)
	}
}

func TestDrawioMeshTrianglePerFabric(t *testing.T) {
	// fabric-a has a spine and two leaves, fabric-b has a mesh of three leaves and two gateways
	topo := Topology{
		Nodes: []Node{
			testNode("spine-a", NodeTypeSwitch, "fabric-a", SwitchRoleSpine),
			testNode("leaf-a1", NodeTypeSwitch, "fabric-a", testLeafRole),
			testNode("leaf-a2", NodeTypeSwitch, "fabric-a", testLeafRole),
			testNode("leaf-b1", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("leaf-b2", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("leaf-b3", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("gateway-1", NodeTypeGateway, "fabric-b", ""),
			testNode("gateway-2", NodeTypeGateway, "fabric-b", ""),
			testNode("server-a", NodeTypeServer, "fabric-a", ""),
			testNode("server-b", NodeTypeServer, "fabric-b", ""),
		},
		Links: append([]Link{
			testLink("spine-a", "leaf-a1", EdgeTypeFabric),
			testLink("spine-a", "leaf-a2", EdgeTypeFabric),
			testLink("gateway-1", "leaf-b1", EdgeTypeGateway),
			testLink("gateway-2", "leaf-b3", EdgeTypeGateway),
			testLink("leaf-a1", "server-a", EdgeTypeUnbundled),
			testLink("leaf-b2", "server-b", EdgeTypeUnbundled),
		}, testMesh("leaf-b1", "leaf-b2", "leaf-b3")...),
	}
	cells := testCells(t, topo)

	// the second leaf of the mesh is raised, and the other leaves of both fabrics are on one row
	require.Less(t, cells["leaf-b2"].Y, cells["leaf-b1"].Y)
	require.Equal(t, cells["leaf-b1"].Y, cells["leaf-b3"].Y)
	require.Equal(t, cells["leaf-b1"].Y, cells["leaf-a1"].Y)
	require.Equal(t, cells["leaf-a1"].Y, cells["leaf-a2"].Y)

	// the gateways have no spine, so they are above the raised leaf, a tier below the top
	for _, gw := range []string{"gateway-1", "gateway-2"} {
		require.Less(t, cells[gw].Y+float64(cells[gw].Height), cells["leaf-b2"].Y)
		require.Greater(t, cells[gw].Y, 50.0)
	}

	requireClearOfBoxes(t, cells)
}

func TestDrawioMeshTriangleGatewayWithoutSpines(t *testing.T) {
	// no spine anywhere: the tiers move down for the gateway of the triangle
	topo := Topology{
		Nodes: []Node{
			testNode("leaf-a1", NodeTypeSwitch, "fabric-a", testLeafRole),
			testNode("leaf-a2", NodeTypeSwitch, "fabric-a", testLeafRole),
			testNode("leaf-b1", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("leaf-b2", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("leaf-b3", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("gateway-1", NodeTypeGateway, "fabric-b", ""),
		},
		Links: append([]Link{
			testLink("leaf-a1", "leaf-a2", EdgeTypeMesh),
			testLink("gateway-1", "leaf-b1", EdgeTypeGateway),
		}, testMesh("leaf-b1", "leaf-b2", "leaf-b3")...),
	}
	cells := testCells(t, topo)

	require.Less(t, cells["leaf-b2"].Y, cells["leaf-b1"].Y)
	require.Less(t, cells["gateway-1"].Y+float64(cells["gateway-1"].Height), cells["leaf-b2"].Y)
	requireClearOfBoxes(t, cells)
}

func TestDrawioSpinelessFabricGatewaysOneTierDown(t *testing.T) {
	// fabric-b has no spine and no triangle: its gateway is on the row of the spines, close to its leaves
	topo := Topology{
		Nodes: []Node{
			testNode("spine-a", NodeTypeSwitch, "fabric-a", SwitchRoleSpine),
			testNode("leaf-a1", NodeTypeSwitch, "fabric-a", testLeafRole),
			testNode("leaf-b1", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("leaf-b2", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("gateway-a", NodeTypeGateway, "fabric-a", ""),
			testNode("gateway-b", NodeTypeGateway, "fabric-b", ""),
		},
		Links: []Link{
			testLink("spine-a", "leaf-a1", EdgeTypeFabric),
			testLink("gateway-a", "spine-a", EdgeTypeGateway),
			testLink("leaf-b1", "leaf-b2", EdgeTypeMesh),
			testLink("gateway-b", "leaf-b1", EdgeTypeGateway),
		},
	}
	cells := testCells(t, topo)

	require.Equal(t, cells["spine-a"].Y, cells["gateway-b"].Y)
	require.Less(t, cells["gateway-a"].Y, cells["gateway-b"].Y)
	require.Less(t, cells["gateway-b"].Y, cells["leaf-b1"].Y)
}

func TestDrawioMeshTriangleExternalsDontOverlapWithoutSpines(t *testing.T) {
	// no spine and no gateway anywhere: Externals on the lower and on the raised leaf of a triangle keep their spacing
	topo := Topology{
		Nodes: []Node{
			testNode("leaf-a1", NodeTypeSwitch, "fabric-a", testLeafRole),
			testNode("leaf-a2", NodeTypeSwitch, "fabric-a", testLeafRole),
			testNode("leaf-b1", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("leaf-b2", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("leaf-b3", NodeTypeSwitch, "fabric-b", testLeafRole),
			testNode("ext-1", NodeTypeExternal, "fabric-b", ""),
			testNode("ext-2", NodeTypeExternal, "fabric-b", ""),
		},
		Links: append([]Link{
			testLink("leaf-a1", "leaf-a2", EdgeTypeMesh),
			testLink("leaf-b1", "ext-1", EdgeTypeExternal),
			testLink("leaf-b2", "ext-2", EdgeTypeExternal),
		}, testMesh("leaf-b1", "leaf-b2", "leaf-b3")...),
	}
	cells := testCells(t, topo)

	require.Less(t, cells["leaf-b2"].Y, cells["leaf-b1"].Y)
	first, second := cells["ext-1"], cells["ext-2"]
	require.False(t, first.Y < second.Y+float64(second.Height) && second.Y < first.Y+float64(first.Height),
		"ext-1 at y=%v and ext-2 at y=%v overlap", first.Y, second.Y)
}

func TestDrawioSingleFabricMeshTriangleUnchanged(t *testing.T) {
	topo := Topology{
		Nodes: []Node{
			testNode("leaf-1", NodeTypeSwitch, "", testLeafRole),
			testNode("leaf-2", NodeTypeSwitch, "", testLeafRole),
			testNode("leaf-3", NodeTypeSwitch, "", testLeafRole),
		},
		Links: testMesh("leaf-1", "leaf-2", "leaf-3"),
	}
	cells := testCells(t, topo)

	require.Less(t, cells["leaf-2"].Y, cells["leaf-1"].Y)
	require.Equal(t, cells["leaf-1"].Y, cells["leaf-3"].Y)
}

func TestDrawioLogoClearOfDomainAndFabricBoxes(t *testing.T) {
	// the box of a wide fabric with a gateway on the top row is under the logo, in the top right corner
	nodes := []Node{
		testNode("leaf-a1", NodeTypeSwitch, "fabric-a", testLeafRole),
		testNode("leaf-a2", NodeTypeSwitch, "fabric-a", testLeafRole),
		testNode("spine-b", NodeTypeSwitch, "fabric-b", SwitchRoleSpine),
		testNode("gateway-b", NodeTypeGateway, "fabric-b", ""),
	}
	links := []Link{
		testLink("leaf-a1", "leaf-a2", EdgeTypeMesh),
		testLink("gateway-b", "spine-b", EdgeTypeGateway),
	}
	for i := 1; i <= 6; i++ {
		leaf := testNode("leaf-b"+string(rune('0'+i)), NodeTypeSwitch, "fabric-b", testLeafRole)
		nodes = append(nodes, leaf)
		links = append(links, testLink("spine-b", leaf.ID, EdgeTypeFabric))
	}

	requireClearOfBoxes(t, testCells(t, Topology{Nodes: nodes, Links: links}))
}
