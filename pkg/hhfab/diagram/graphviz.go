// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	StyleSolid  = "solid"
	StyleDashed = "dashed"

	ColorFabric    = "red"
	ColorMesh      = "blue"
	ColorMCLAG     = "blue"
	ColorBundled   = "green"
	ColorUnbundled = "gray"
	ColorESLAG     = "orange"
	ColorGateway   = "khaki"
	ColorExternal  = "goldenrod"
	ColorDefault   = "black"
)

func GenerateDOT(workDir string, topo Topology, outputPath string) error {
	var finalOutputPath string
	if outputPath != "" {
		finalOutputPath = outputPath
	} else {
		finalOutputPath = filepath.Join(workDir, DotFilename)
	}

	dot := generateDOT(topo)

	if err := os.MkdirAll(filepath.Dir(finalOutputPath), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	if err := os.WriteFile(finalOutputPath, []byte(dot), 0o600); err != nil {
		return fmt.Errorf("writing DOT file: %w", err)
	}

	return nil
}

func generateDOT(topo Topology) string {
	var b strings.Builder

	b.WriteString("digraph network_topology {\n")
	b.WriteString("\tgraph [rankdir=TB, nodesep=1.5, ranksep=2.5, splines=line, compound=true];\n")
	b.WriteString("\tnode [shape=box, style=rounded, fontname=\"Arial\", fontsize=12, height=0.4, width=1.2];\n")
	b.WriteString("\tedge [fontname=\"Arial\", fontsize=8, dir=none];\n\n")

	b.WriteString("\tlegend_anchor [shape=none, width=0, height=0, label=\"\", style=\"\"];\n")

	layers := sortNodes(topo.Nodes, topo.Links)

	// Detect mesh triangle for special ranking
	isMeshTriangle := detectMeshTriangle(layers.Leaf, topo.Links)
	hasGateway := len(layers.Gateway) > 0

	// Collect unique link types present in the topology for dynamic legend
	linkTypesPresent := make(map[string]bool)
	maxParallelConnections := make(map[string]int) // Track max parallel connections for aggregated links

	// Pre-analyze links to detect parallel connections before creating legend
	spineLeafGroups := make(map[string]map[string]int)
	meshGroups := make(map[string]map[string]int)

	for _, link := range topo.Links {
		// Pre-analyze for parallel fabric connections
		if link.Type == EdgeTypeFabric {
			var spineNode, leafNode string
			for _, node := range layers.Spine {
				if link.Source == node.ID {
					spineNode = link.Source
					leafNode = link.Target

					break
				} else if link.Target == node.ID {
					spineNode = link.Target
					leafNode = link.Source

					break
				}
			}

			if spineNode != "" && leafNode != "" {
				isLeaf := false
				for _, node := range layers.Leaf {
					if leafNode == node.ID {
						isLeaf = true

						break
					}
				}

				if isLeaf {
					if spineLeafGroups[spineNode] == nil {
						spineLeafGroups[spineNode] = make(map[string]int)
					}
					spineLeafGroups[spineNode][leafNode]++
				}
			}
		}

		// Pre-analyze for parallel mesh connections
		if link.Type == EdgeTypeMesh {
			sourceIsLeaf := false
			targetIsLeaf := false

			for _, node := range layers.Leaf {
				if link.Source == node.ID {
					sourceIsLeaf = true
				}
				if link.Target == node.ID {
					targetIsLeaf = true
				}
			}

			if sourceIsLeaf && targetIsLeaf {
				var key1, key2 string
				if link.Source < link.Target {
					key1, key2 = link.Source, link.Target
				} else {
					key1, key2 = link.Target, link.Source
				}

				if meshGroups[key1] == nil {
					meshGroups[key1] = make(map[string]int)
				}
				meshGroups[key1][key2]++
			}
		}

		// Track link types for legend
		// Check for MCLAG links which use "mclagType" property
		if mclagType, ok := link.Properties[PropMCLAGType]; ok {
			if mclagType == MCLAGTypePeer {
				linkTypesPresent["mclag_peer"] = true
			} else if mclagType == MCLAGTypeSession {
				linkTypesPresent["mclag_session"] = true
			}
		} else if _, ok := link.Properties[PropBundled]; ok {
			linkTypesPresent["bundled"] = true
		} else if _, ok := link.Properties[PropESLAGServer]; ok {
			linkTypesPresent["eslag_server"] = true
		} else if _, ok := link.Properties[PropGateway]; ok {
			linkTypesPresent["gateway"] = true
		} else {
			switch link.Type {
			case EdgeTypeMCLAG:
				linkTypesPresent["mclag_server"] = true
			case EdgeTypeBundled:
				linkTypesPresent["bundled"] = true
			case EdgeTypeESLAG:
				linkTypesPresent["eslag_server"] = true
			case EdgeTypeGateway:
				linkTypesPresent["gateway"] = true
			case EdgeTypeExternal:
				linkTypesPresent["external"] = true
			case EdgeTypeStaticExternal:
				linkTypesPresent["static_external"] = true
			case EdgeTypeMesh:
				linkTypesPresent["mesh"] = true
			case EdgeTypeFabric:
				linkTypesPresent["fabric"] = true
			default:
				// For other links, determine type based on node roles
				sourceNodeFound := false
				targetNodeFound := false
				var sourceNode, targetNode Node

				// Find source node
				for _, n := range topo.Nodes {
					if n.ID == link.Source {
						sourceNode = n
						sourceNodeFound = true

						break
					}
				}

				// Find target node
				for _, n := range topo.Nodes {
					if n.ID == link.Target {
						targetNode = n
						targetNodeFound = true

						break
					}
				}

				// If both nodes found, determine link type
				if sourceNodeFound && targetNodeFound {
					sourceType, sourceRole := getNodeTypeInfo(sourceNode)
					targetType, targetRole := getNodeTypeInfo(targetNode)

					if sourceType == NodeTypeSwitch && targetType == NodeTypeSwitch {
						if (sourceRole == SwitchRoleSpine && targetRole == SwitchRoleLeaf) ||
							(sourceRole == SwitchRoleLeaf && targetRole == SwitchRoleSpine) {
							linkTypesPresent["fabric"] = true
						} else if sourceRole == SwitchRoleLeaf && targetRole == SwitchRoleLeaf {
							if link.Type == EdgeTypeMesh {
								linkTypesPresent["mesh"] = true
							} else {
								linkTypesPresent["fabric"] = true
							}
						}
					} else if (sourceType == NodeTypeSwitch && targetType == NodeTypeServer) ||
						(sourceType == NodeTypeServer && targetType == NodeTypeSwitch) {
						linkTypesPresent["unbundled"] = true
					}
				}
			}
		}
	}

	// Calculate max parallel connections from pre-analysis
	for _, leafConnections := range spineLeafGroups {
		for _, count := range leafConnections {
			if count > maxParallelConnections["fabric"] {
				maxParallelConnections["fabric"] = count
			}
		}
	}

	for _, leafConnections := range meshGroups {
		for _, count := range leafConnections {
			if count > maxParallelConnections["mesh"] {
				maxParallelConnections["mesh"] = count
			}
		}
	}

	b.WriteString("\t{rank=source; legend_anchor}\n")
	b.WriteString("\tsubgraph cluster_legend {\n")
	b.WriteString("\t\tlabel=\"Network Connection Types\";\n")
	b.WriteString("\t\tlabelloc=\"top\";\n")
	b.WriteString("\t\tfontsize=14;\n")
	b.WriteString("\t\tfontname=\"Arial\";\n")
	b.WriteString("\t\tstyle=\"rounded\";\n")
	b.WriteString("\t\tcolor=\"transparent\";\n")
	b.WriteString("\t\tbgcolor=\"transparent\";\n")
	b.WriteString("\t\tlegend [shape=none, margin=0, label=<\n")
	b.WriteString("\t\t\t<TABLE BORDER=\"0\" CELLBORDER=\"0\" CELLSPACING=\"4\" CELLPADDING=\"0\">\n")

	// Only include legend entries for link types present in the topology
	if linkTypesPresent["fabric"] {
		fabricLabel := "Fabric Links"
		if maxParallelConnections["fabric"] > 1 {
			fabricLabel = fmt.Sprintf("Fabric Links (x%d)", maxParallelConnections["fabric"])
		}
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"red\">────</FONT></TD>\n")
		b.WriteString(fmt.Sprintf("\t\t\t<TD ALIGN=\"LEFT\">%s</TD>\n", fabricLabel))
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["mesh"] {
		meshLabel := "Mesh Links"
		if maxParallelConnections["mesh"] > 1 {
			meshLabel = fmt.Sprintf("Mesh Links (x%d)", maxParallelConnections["mesh"])
		}
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"blue\">────</FONT></TD>\n")
		b.WriteString(fmt.Sprintf("\t\t\t<TD ALIGN=\"LEFT\">%s</TD>\n", meshLabel))
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["mclag_peer"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"purple\">- - - -</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">MCLAG Peer Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["mclag_session"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"purple\">────</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">MCLAG Session Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["mclag_server"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"purple\">────</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">MCLAG Server Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["bundled"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"green\">────</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Bundled Server Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["unbundled"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"gray\">────</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Unbundled Server Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["eslag_server"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"orange\">- - - -</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">ESLAG Server Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["gateway"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"khaki\">- - - -</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Gateway Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["external"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"goldenrod\">────</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">External Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}
	if linkTypesPresent["static_external"] {
		b.WriteString("\t\t\t<TR>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"goldenrod\">────</FONT></TD>\n")
		b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Static External Links</TD>\n")
		b.WriteString("\t\t\t</TR>\n")
	}

	b.WriteString("\t\t\t</TABLE>\n")
	b.WriteString("\t\t>];\n")
	b.WriteString("\t}\n")
	b.WriteString("\t// Connect legend to anchor to position it at the top-left\n")
	b.WriteString("\tlegend_anchor -> legend [style=invis];\n\n")

	// Define source rank for gateways and/or mesh triangle upper leaf
	if len(layers.Gateway) > 0 || (isMeshTriangle && !hasGateway && len(layers.Leaf) >= 2) {
		b.WriteString("\t{rank=source; ")
		for _, node := range layers.Gateway {
			b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
		}
		// If mesh triangle without gateway, add upper leaf to source rank
		if isMeshTriangle && !hasGateway && len(layers.Leaf) >= 2 {
			b.WriteString(fmt.Sprintf("\"%s\"; ", layers.Leaf[1].ID))
		}
		b.WriteString("}\n")
	}

	// Mesh triangle positioning is handled by invisible edges below

	// Put spines and externals at the same rank
	if len(layers.Spine) > 0 || len(layers.External) > 0 {
		b.WriteString("\t{rank=same; ")
		for _, node := range layers.Spine {
			b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
		}

		// Add externals to the spine rank
		if len(layers.External) > 0 {
			leftExternals, rightExternals := splitExternalNodes(layers.External, topo.Links, layers.Leaf)
			for _, node := range leftExternals {
				b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
			}
			for _, node := range rightExternals {
				b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
			}
		}
		b.WriteString("}\n")
	}

	// Leaves below spines and externals
	b.WriteString("\t{rank=same; ")
	for i, node := range layers.Leaf {
		// For mesh triangle, skip the elevated leaf if it should be positioned differently
		if isMeshTriangle && i == 1 && (hasGateway || !hasGateway) {
			continue
		}
		b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
	}
	b.WriteString("}\n")

	// Servers at bottom
	b.WriteString("\t{rank=max; ")
	for _, node := range layers.Server {
		b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
	}
	b.WriteString("}\n\n")

	// Node styling
	for _, node := range layers.Gateway {
		b.WriteString(fmt.Sprintf("\t\"%s\" [label=\"%s\", fillcolor=\"#fff2cc\", style=\"rounded,filled\", color=\"#d6b656\"];\n",
			node.ID, node.Label))
	}
	for _, node := range layers.Spine {
		b.WriteString(fmt.Sprintf("\t\"%s\" [label=\"%s\", fillcolor=\"#f8cecc\", style=\"rounded,filled\", color=\"#b85450\"];\n",
			node.ID, node.Label))
	}
	for _, node := range layers.Leaf {
		b.WriteString(fmt.Sprintf("\t\"%s\" [label=\"%s\", fillcolor=\"#dae8fc\", style=\"rounded,filled\", color=\"#6c8ebf\"];\n",
			node.ID, node.Label))
	}
	for _, node := range layers.Server {
		b.WriteString(fmt.Sprintf("\t\"%s\" [label=\"%s\", fillcolor=\"#d5e8d4\", style=\"filled\", color=\"#82b366\"];\n",
			node.ID, node.Label))
	}

	// Add styling for external nodes
	if len(layers.External) > 0 {
		leftExternals, rightExternals := splitExternalNodes(layers.External, topo.Links, layers.Leaf)
		for _, node := range leftExternals {
			b.WriteString(fmt.Sprintf("\t\"%s\" [label=\"%s\", fillcolor=\"#ffcc99\", style=\"rounded,filled\", color=\"#d79b00\"];\n",
				node.ID, node.Label))
		}
		for _, node := range rightExternals {
			b.WriteString(fmt.Sprintf("\t\"%s\" [label=\"%s\", fillcolor=\"#ffcc99\", style=\"rounded,filled\", color=\"#d79b00\"];\n",
				node.ID, node.Label))
		}
	}

	b.WriteString("\n")

	// Add invisible edges to center the gateway over spines
	if len(layers.Gateway) > 0 && len(layers.Spine) > 0 {
		// For even number of spines, connect to both middle spines
		if len(layers.Spine)%2 == 0 {
			// Find the middle spines
			middleIndex1 := len(layers.Spine)/2 - 1
			middleIndex2 := len(layers.Spine) / 2

			// Add equal weight invisible edges to both middle spines
			for _, gateway := range layers.Gateway {
				b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, weight=200];\n",
					gateway.ID, layers.Spine[middleIndex1].ID))
				b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, weight=200];\n",
					gateway.ID, layers.Spine[middleIndex2].ID))
			}
		} else {
			// For odd number of spines, connect to middle spine
			middleIndex := len(layers.Spine) / 2
			for _, gateway := range layers.Gateway {
				b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, weight=200];\n",
					gateway.ID, layers.Spine[middleIndex].ID))
			}
		}
	}

	// For mesh triangle with gateway, add positioning constraints
	if isMeshTriangle && hasGateway && len(layers.Leaf) >= 3 {
		// Connect gateway to upper leaf to center it
		for _, gateway := range layers.Gateway {
			b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, weight=200];\n",
				gateway.ID, layers.Leaf[1].ID))
		}
		// Add invisible edges from upper leaf to lower leaves for triangle positioning
		b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, weight=150];\n",
			layers.Leaf[1].ID, layers.Leaf[0].ID))
		b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, weight=150];\n",
			layers.Leaf[1].ID, layers.Leaf[2].ID))
		// Add constraint to center upper leaf between lower leaves
		b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, constraint=false, weight=50];\n",
			layers.Leaf[0].ID, layers.Leaf[1].ID))
		b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, constraint=false, weight=50];\n",
			layers.Leaf[2].ID, layers.Leaf[1].ID))
	}

	// Chain nodes within same rank
	if len(layers.Gateway) > 1 {
		writeChain(&b, layers.Gateway)
	}
	writeChain(&b, layers.Spine)

	// For mesh triangle, chain leaves excluding the second one if it's elevated
	if isMeshTriangle {
		var leafsToChain []Node
		for i, leaf := range layers.Leaf {
			if hasGateway && i == 1 { // Skip elevated leaf when gateway present
				continue
			} else if !hasGateway && i == 1 { // Skip elevated leaf when no gateway
				continue
			}
			leafsToChain = append(leafsToChain, leaf)
		}
		writeChain(&b, leafsToChain)
	} else {
		writeChain(&b, layers.Leaf)
	}

	writeChain(&b, layers.Server)

	// Chain externals if needed
	if len(layers.External) > 1 {
		writeChain(&b, layers.External)
	}

	b.WriteString("\n")

	// Draw the edges with grouping
	b.WriteString("\tedge [style=solid, weight=1];\n")

	// Track fabric links between spine and leaf to collapse parallel connections
	spineLeafConnections := make(map[string]map[string][]map[string]string)
	// Track mesh links between leaves to collapse parallel connections
	meshConnections := make(map[string]map[string][]map[string]string)

	for _, link := range topo.Links {
		// For Fabric links between spine and leaf nodes
		if link.Type == EdgeTypeFabric {
			var spineNode, leafNode string
			var spinePort, leafPort string

			// Determine which is spine and which is leaf
			for _, node := range layers.Spine {
				if link.Source == node.ID {
					spineNode = link.Source
					spinePort = link.Properties["sourcePort"]
					leafNode = link.Target
					leafPort = link.Properties["targetPort"]

					break
				} else if link.Target == node.ID {
					spineNode = link.Target
					spinePort = link.Properties["targetPort"]
					leafNode = link.Source
					leafPort = link.Properties["sourcePort"]

					break
				}
			}

			// If both are spine-leaf connection
			if spineNode != "" && leafNode != "" {
				isLeaf := false
				for _, node := range layers.Leaf {
					if leafNode == node.ID {
						isLeaf = true

						break
					}
				}

				if isLeaf {
					// Initialize map for spine if not exists
					if spineLeafConnections[spineNode] == nil {
						spineLeafConnections[spineNode] = make(map[string][]map[string]string)
					}

					// Add port pair to connection list
					portInfo := map[string]string{
						"spinePort": spinePort,
						"leafPort":  leafPort,
					}
					spineLeafConnections[spineNode][leafNode] = append(
						spineLeafConnections[spineNode][leafNode], portInfo)

					// Skip this link as we'll render it later
					continue
				}
			}
		}

		// For Mesh links between leaf nodes
		if link.Type == EdgeTypeMesh {
			var leaf1, leaf2 string
			var leaf1Port, leaf2Port string

			// Check if both source and target are leaves
			sourceIsLeaf := false
			targetIsLeaf := false

			for _, node := range layers.Leaf {
				if link.Source == node.ID {
					sourceIsLeaf = true
					leaf1 = link.Source
					leaf1Port = link.Properties["sourcePort"]
					leaf2 = link.Target
					leaf2Port = link.Properties["targetPort"]

					break
				}
			}

			if sourceIsLeaf {
				for _, node := range layers.Leaf {
					if link.Target == node.ID {
						targetIsLeaf = true

						break
					}
				}
			}

			if sourceIsLeaf && targetIsLeaf {
				// Create consistent key ordering (smaller ID first)
				var key1, key2, port1, port2 string
				if leaf1 < leaf2 {
					key1, key2 = leaf1, leaf2
					port1, port2 = leaf1Port, leaf2Port
				} else {
					key1, key2 = leaf2, leaf1
					port1, port2 = leaf2Port, leaf1Port
				}

				// Initialize map if not exists
				if meshConnections[key1] == nil {
					meshConnections[key1] = make(map[string][]map[string]string)
				}

				// Add port pair to connection list
				portInfo := map[string]string{
					"port1": port1,
					"port2": port2,
				}
				meshConnections[key1][key2] = append(meshConnections[key1][key2], portInfo)

				// Skip this link as we'll render it later
				continue
			}
		}

		// Process all other links normally
		var color, style string
		switch link.Type {
		case EdgeTypeFabric:
			color = ColorFabric
			style = StyleSolid
		case EdgeTypeMesh:
			color = ColorMesh
			style = StyleSolid
		case EdgeTypeMCLAG:
			color = ColorMCLAG
			style = StyleDashed
		case EdgeTypeBundled:
			color = ColorBundled
			style = StyleSolid
		case EdgeTypeUnbundled:
			color = ColorUnbundled
			style = StyleSolid
		case EdgeTypeESLAG:
			color = ColorESLAG
			style = StyleDashed
		case EdgeTypeGateway:
			color = ColorGateway
			style = StyleDashed
		case EdgeTypeExternal:
			color = ColorExternal
			style = StyleSolid // Changed from dashed to solid
		case EdgeTypeStaticExternal:
			color = ColorExternal // Same color as external
			style = StyleSolid    // Solid style
		default:
			color = ColorDefault
			style = StyleSolid
		}
		b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [color=\"%s\", style=\"%s\", headlabel=\"%s\", taillabel=\"%s\", labeldistance=2, labelangle=0];\n",
			link.Source, link.Target, color, style,
			extractPort(link.Properties["targetPort"]),
			extractPort(link.Properties["sourcePort"])))
	}

	// Render collapsed spine-leaf connections
	for spineNode, leafConnections := range spineLeafConnections {
		for leafNode, portPairs := range leafConnections {
			// Only draw one connection per spine-leaf pair
			if len(portPairs) > 0 {
				// Combine port labels
				var spinePorts, leafPorts []string
				for _, pair := range portPairs {
					spinePorts = append(spinePorts, extractPort(pair["spinePort"]))
					leafPorts = append(leafPorts, extractPort(pair["leafPort"]))
				}

				spinePortsLabel := strings.Join(spinePorts, ",")
				leafPortsLabel := strings.Join(leafPorts, ",")

				// Set penwidth proportional to the number of links
				penwidth := 1 + len(portPairs)

				// For thick lines, make them stand out more
				b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [color=\"%s\", style=\"%s\", headlabel=\"%s\", taillabel=\"%s\", labeldistance=2, labelangle=0, penwidth=%d];\n",
					spineNode, leafNode, ColorFabric, StyleSolid,
					leafPortsLabel, spinePortsLabel, penwidth))
			}
		}
	}

	// Render collapsed mesh connections
	for leaf1, leafConnections := range meshConnections {
		for leaf2, portPairs := range leafConnections {
			// Only draw one connection per leaf-leaf pair
			if len(portPairs) > 0 {
				// Combine port labels
				var leaf1Ports, leaf2Ports []string
				for _, pair := range portPairs {
					leaf1Ports = append(leaf1Ports, extractPort(pair["port1"]))
					leaf2Ports = append(leaf2Ports, extractPort(pair["port2"]))
				}

				leaf1PortsLabel := strings.Join(leaf1Ports, ",")
				leaf2PortsLabel := strings.Join(leaf2Ports, ",")

				// Set penwidth proportional to the number of links
				penwidth := 1 + len(portPairs)

				// For thick lines, make them stand out more
				b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [color=\"%s\", style=\"%s\", headlabel=\"%s\", taillabel=\"%s\", labeldistance=2, labelangle=0, penwidth=%d];\n",
					leaf1, leaf2, ColorMesh, StyleSolid,
					leaf2PortsLabel, leaf1PortsLabel, penwidth))
			}
		}
	}

	b.WriteString("}\n")

	return b.String()
}

func writeChain(b *strings.Builder, nodes []Node) {
	if len(nodes) > 1 {
		for i := 0; i < len(nodes)-1; i++ {
			b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [style=invis, weight=100];\n", nodes[i].ID, nodes[i+1].ID))
		}
	}
}
