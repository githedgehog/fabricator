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
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"red\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Fabric Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"blue\">- - - -</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">MCLAG Peer Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"blue\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">MCLAG Session Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"blue\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">MCLAG Server Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"green\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Bundled Server Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"gray\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Unbundled Server Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"orange\">- - - -</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">ESLAG Server Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"purple\">- - - -</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Gateway Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"goldenrod\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">External Links</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t</TABLE>\n")
	b.WriteString("\t\t>];\n")
	b.WriteString("\t}\n")
	b.WriteString("\t// Connect legend to anchor to position it at the top-left\n")
	b.WriteString("\tlegend_anchor -> legend [style=invis];\n\n")

	// Place gateway at source rank (top in TB layout)
	if len(layers.Gateway) > 0 {
		b.WriteString("\t{rank=source; ")
		for _, node := range layers.Gateway {
			b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
		}
		b.WriteString("}\n")
	}

	// Put spines and externals at the same rank
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

	// Leaves below spines and externals
	b.WriteString("\t{rank=same; ")
	for _, node := range layers.Leaf {
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

	// Chain nodes within same rank
	if len(layers.Gateway) > 1 {
		writeChain(&b, layers.Gateway)
	}
	writeChain(&b, layers.Spine)
	writeChain(&b, layers.Leaf)
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

		// Process all other links normally
		var color, style string
		switch link.Type {
		case EdgeTypeFabric:
			color = ColorFabric
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
			style = StyleSolid
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
