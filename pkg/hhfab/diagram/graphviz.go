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
	b.WriteString("\t\t\t</TABLE>\n")
	b.WriteString("\t\t>];\n")
	b.WriteString("\t}\n")
	b.WriteString("\t// Connect legend to anchor to position it at the top-left\n")
	b.WriteString("\tlegend_anchor -> legend [style=invis];\n\n")

	b.WriteString("\t{rank=same; ")
	for _, node := range layers.Spine {
		b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
	}
	b.WriteString("}\n")

	if len(layers.Gateway) > 0 {
		b.WriteString("\t{rank=same; ")
		for _, node := range layers.Gateway {
			b.WriteString(fmt.Sprintf("\"%s\"; ", node.ID))
		}
		b.WriteString("}\n")
	}

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
	b.WriteString("\n")

	if len(layers.Gateway) > 1 {
		writeChain(&b, layers.Gateway)
	}
	writeChain(&b, layers.Spine)
	writeChain(&b, layers.Leaf)
	writeChain(&b, layers.Server)
	b.WriteString("\n")

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
		case EdgeTypeGateway:
			color = "#d6b656"
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
