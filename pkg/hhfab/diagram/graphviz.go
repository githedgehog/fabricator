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
	ColorDefault   = "black"
)

func GenerateDOT(workDir string, jsonData []byte) error {
	outputFile := filepath.Join(workDir, "vlab-diagram.dot")
	topo, err := ConvertJSONToTopology(jsonData)
	if err != nil {
		return fmt.Errorf("converting JSON to topology: %w", err)
	}

	dot := generateDOT(topo)
	if err := os.WriteFile(outputFile, []byte(dot), 0600); err != nil {
		return fmt.Errorf("writing DOT file: %w", err)
	}

	return nil
}

func generateDOT(topo Topology) string {
	var b strings.Builder

	// Begin DOT graph with top-to-bottom ranking and standard styling.
	b.WriteString("digraph network_topology {\n")
	b.WriteString("\tgraph [rankdir=TB, nodesep=1.5, ranksep=2.5, splines=line];\n")
	b.WriteString("\tnode [shape=box, style=rounded, fontname=\"Arial\", fontsize=12, height=0.4, width=1.2];\n")
	b.WriteString("\tedge [fontname=\"Arial\", fontsize=8, dir=none];\n\n")

	layers := sortNodes(topo.Nodes, topo.Links)

	// Create legend subgraph (using an HTML table for labels) as in the original.
	b.WriteString("\tsubgraph cluster_legend {\n")
	b.WriteString("\t\tlabel=\"Connection Types\";\n")
	b.WriteString("\t\tlabelloc=\"top\";\n")
	b.WriteString("\t\tfontsize=12;\n")
	b.WriteString("\t\tstyle=\"rounded\";\n")
	b.WriteString("\t\tcolor=\"#999999\";\n")
	b.WriteString("\t\tlegend [shape=none, margin=0, label=<\n")
	b.WriteString("\t\t\t<TABLE BORDER=\"0\" CELLBORDER=\"0\" CELLSPACING=\"4\" CELLPADDING=\"0\">\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"red\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Fabric</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"blue\">- - - -</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">MCLAG</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"green\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Bundled</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"gray\">────</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">Unbundled</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t<TR>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\" VALIGN=\"MIDDLE\"><FONT COLOR=\"orange\">- - - -</FONT></TD>\n")
	b.WriteString("\t\t\t<TD ALIGN=\"LEFT\">ESLAG</TD>\n")
	b.WriteString("\t\t\t</TR>\n")
	b.WriteString("\t\t\t</TABLE>\n")
	b.WriteString("\t\t>];\n")
	b.WriteString("\t}\n\n")

	// Enforce ordering via rank: spines at the top, leaves in the middle, servers at the bottom.
	b.WriteString("\t{rank=min; ")
	for _, node := range layers.Spine {
		b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
	}
	b.WriteString("}\n")
	b.WriteString("\t{rank=same; ")
	for _, node := range layers.Leaf {
		b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
	}
	b.WriteString("}\n")
	b.WriteString("\t{rank=max; ")
	for _, node := range layers.Server {
		b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
	}
	b.WriteString("}\n\n")

	// Node definitions.
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
	b.WriteString("\n")

	// Invisible chain edges to enforce left-to-right ordering within each layer.
	writeChain(&b, layers.Spine)
	writeChain(&b, layers.Leaf)
	writeChain(&b, layers.Server)
	b.WriteString("\n")

	// Visible network connections.
	b.WriteString("\tedge [style=solid, weight=1];\n")
	for _, link := range topo.Links {
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
		default:
			color = ColorDefault
			style = StyleSolid
		}
		b.WriteString(fmt.Sprintf("\t\"%s\" -> \"%s\" [color=\"%s\", style=\"%s\", headlabel=\"%s\", taillabel=\"%s\", labeldistance=2, labelangle=0];\n",
			link.Source, link.Target, color, style,
			extractPort(link.Properties["targetPort"]),
			extractPort(link.Properties["sourcePort"])))
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
