// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
)

type MxGraphModel struct {
	XMLName    xml.Name `xml:"mxGraphModel"`
	Dx         int      `xml:"dx,attr"`
	Dy         int      `xml:"dy,attr"`
	Grid       int      `xml:"grid,attr"`
	Guides     int      `xml:"guides,attr"`
	Tooltips   int      `xml:"tooltips,attr"`
	Connect    int      `xml:"connect,attr"`
	Arrows     int      `xml:"arrows,attr"`
	Fold       int      `xml:"fold,attr"`
	Page       int      `xml:"page,attr"`
	PageScale  float64  `xml:"pageScale,attr"`
	PageWidth  int      `xml:"pageWidth,attr"`
	PageHeight int      `xml:"pageHeight,attr"`
	Background string   `xml:"background,attr,omitempty"`
	Root       Root     `xml:"root"`
}

type Root struct {
	MxCell []MxCell `xml:"mxCell"`
}

type MxCell struct {
	ID          string    `xml:"id,attr"`
	Parent      string    `xml:"parent,attr,omitempty"`
	Value       string    `xml:"value,attr,omitempty"`
	Style       string    `xml:"style,attr,omitempty"`
	Vertex      string    `xml:"vertex,attr,omitempty"`
	Edge        string    `xml:"edge,attr,omitempty"`
	Source      string    `xml:"source,attr,omitempty"`
	Target      string    `xml:"target,attr,omitempty"`
	Connectable string    `xml:"connectable,attr,omitempty"`
	ExitX       float64   `xml:"exitX,attr,omitempty"`
	ExitY       float64   `xml:"exitY,attr,omitempty"`
	EntryX      float64   `xml:"entryX,attr,omitempty"`
	EntryY      float64   `xml:"entryY,attr,omitempty"`
	Geometry    *Geometry `xml:"mxGeometry,omitempty"`
}

type Geometry struct {
	Relative string  `xml:"relative,attr,omitempty"`
	Fixed    string  `xml:"fixed,attr,omitempty"`
	As       string  `xml:"as,attr,omitempty"`
	X        float64 `xml:"x,attr,omitempty"`
	Y        float64 `xml:"y,attr,omitempty"`
	Width    int     `xml:"width,attr,omitempty"`
	Height   int     `xml:"height,attr,omitempty"`
}

type Point struct {
	X  float64 `xml:"x,attr"`
	Y  float64 `xml:"y,attr"`
	As string  `xml:"as,attr,omitempty"`
}

func GenerateDrawio(workDir string, jsonData []byte, styleType StyleType) error {
	outputFile := filepath.Join(workDir, "vlab-diagram.drawio")
	topo, err := ConvertJSONToTopology(jsonData)
	if err != nil {
		return fmt.Errorf("converting JSON to topology: %w", err)
	}

	style := GetStyle(styleType)

	model := createDrawioModel(topo, style)
	outputXML, err := xml.MarshalIndent(model, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling XML: %w", err)
	}
	xmlContent := []byte(xml.Header + string(outputXML))
	if err := os.WriteFile(outputFile, xmlContent, 0600); err != nil {
		return fmt.Errorf("writing draw.io file: %w", err)
	}

	return nil
}

func createDrawioModel(topo Topology, style Style) *MxGraphModel {
	model := &MxGraphModel{
		Dx:         600,
		Dy:         700,
		Grid:       1,
		Guides:     1,
		Tooltips:   1,
		Connect:    1,
		Arrows:     1,
		Fold:       1,
		Page:       1,
		PageScale:  1,
		PageWidth:  600,
		PageHeight: 1000,
		Background: style.BackgroundColor,
		Root: Root{
			MxCell: []MxCell{
				{ID: "0"},
				{ID: "1", Parent: "0"},
			},
		},
	}

	model.Root.MxCell = append(model.Root.MxCell, createLegend(style)...)

	spineY := 100
	leafY := spineY + 250
	serverY := leafY + 250

	layers := sortNodes(topo.Nodes, topo.Links)
	cellMap := make(map[string]*MxCell)

	canvasWidth := 600

	spineNodeWidth := 100
	var spineSpacing float64 = 250
	if len(layers.Spine) > 1 {
		spineSpacing = math.Min(350, float64(canvasWidth-len(layers.Spine)*spineNodeWidth)/float64(len(layers.Spine)-1))
	}

	spineStartX := float64(canvasWidth-(len(layers.Spine)*spineNodeWidth+int(spineSpacing)*(len(layers.Spine)-1))) / 2
	spineCenterX := spineStartX + float64(len(layers.Spine)*spineNodeWidth+int(spineSpacing)*(len(layers.Spine)-1))/2

	for i, node := range layers.Spine {
		width, height := GetNodeDimensions(node)
		x := spineStartX + float64(i)*(float64(width)+spineSpacing)
		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  FormatNodeValue(node, style),
			Style:  GetNodeStyle(node, style),
			Vertex: "1",
			Geometry: &Geometry{
				X:      x,
				Y:      float64(spineY),
				Width:  width,
				Height: height,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	leafNodeWidth := 100
	var leafSpacing float64
	if len(layers.Leaf) <= 3 {
		leafSpacing = 200
	} else if len(layers.Leaf) <= 5 {
		leafSpacing = 160
	} else {
		leafSpacing = 120
	}

	totalLeafWidth := float64(len(layers.Leaf)*leafNodeWidth) + leafSpacing*float64(len(layers.Leaf)-1)

	leafStartX := spineCenterX - (totalLeafWidth / 2)

	for i, node := range layers.Leaf {
		width, height := GetNodeDimensions(node)
		x := leafStartX + float64(i)*(float64(width)+leafSpacing)
		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  FormatNodeValue(node, style),
			Style:  GetNodeStyle(node, style),
			Vertex: "1",
			Geometry: &Geometry{
				X:      x,
				Y:      float64(leafY),
				Width:  width,
				Height: height,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	serverNodeWidth := 100
	var serverSpacing float64 = 60

	totalServerWidth := float64(len(layers.Server)*serverNodeWidth) + serverSpacing*float64(len(layers.Server)-1)

	serverStartX := spineCenterX - (totalServerWidth / 2)

	for i, node := range layers.Server {
		width, height := GetNodeDimensions(node)
		x := serverStartX + float64(i)*(float64(width)+serverSpacing)
		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  FormatNodeValue(node, style),
			Style:  GetNodeStyle(node, style),
			Vertex: "1",
			Geometry: &Geometry{
				X:      x,
				Y:      float64(serverY),
				Width:  width,
				Height: height,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	linkGroups := groupLinks(topo.Links)
	for i, group := range linkGroups {
		createParallelEdges(model, group, cellMap, i, style)
	}

	return model
}

func createLegend(style Style) []MxCell {
	container := MxCell{
		ID:     "legend_container",
		Parent: "1",
		Style:  "group",
		Vertex: "1",
		Geometry: &Geometry{
			X:      -400,
			Y:      10,
			Width:  320,
			Height: 250,
			As:     "geometry",
		},
	}
	background := MxCell{
		ID:     "legend_bg",
		Parent: "legend_container",
		Style:  "rounded=0;whiteSpace=wrap;html=1;fillColor=none;strokeColor=none;",
		Vertex: "1",
		Geometry: &Geometry{
			Width:  320,
			Height: 250,
			As:     "geometry",
		},
	}
	title := MxCell{
		ID:     "legend_title",
		Parent: "legend_container",
		Value:  "Network Connection Types",
		Style:  "text;html=1;strokeColor=none;fillColor=none;align=left;verticalAlign=middle;whiteSpace=wrap;rounded=0;fontSize=14;fontStyle=1",
		Vertex: "1",
		Geometry: &Geometry{
			X:      20,
			Y:      10,
			Width:  320,
			Height: 20,
			As:     "geometry",
		},
	}

	// Using the actual style values from Style struct instead of hardcoded values
	// This ensures the legend matches the actual link styles used in the diagram
	legendEntries := []struct {
		y     int
		style string
		text  string
	}{
		{50, style.FabricLinkStyle, "Fabric Links"},
		{80, style.MCLAGPeerStyle, "MCLAG Peer Links"},
		{110, style.MCLAGSessionStyle, "MCLAG Session Links"},
		{140, style.MCLAGServerStyle, "MCLAG Server Links"},
		{170, style.BundledServerStyle, "Bundled Server Links"},
		{200, style.UnbundledStyle, "Unbundled Server Links"},
		{230, style.ESLAGServerStyle, "ESLAG Server Links"},
	}

	cells := make([]MxCell, 3+4*len(legendEntries))[:0]
	cells = append(cells, container, background, title)
	for i, entry := range legendEntries {
		startPoint := MxCell{
			ID:     fmt.Sprintf("legend_line_%d_start", i),
			Parent: "legend_container",
			Style:  "point;",
			Vertex: "1",
			Geometry: &Geometry{
				X:      20,
				Y:      float64(entry.y),
				Width:  1,
				Height: 1,
				As:     "geometry",
			},
		}
		endPoint := MxCell{
			ID:     fmt.Sprintf("legend_line_%d_end", i),
			Parent: "legend_container",
			Style:  "point;",
			Vertex: "1",
			Geometry: &Geometry{
				X:      60,
				Y:      float64(entry.y),
				Width:  1,
				Height: 1,
				As:     "geometry",
			},
		}

		lineSample := MxCell{
			ID:     fmt.Sprintf("legend_line_%d", i),
			Parent: "legend_container",
			Style:  entry.style,
			Edge:   "1",
			Source: startPoint.ID,
			Target: endPoint.ID,
			Geometry: &Geometry{
				Relative: "1",
				As:       "geometry",
			},
		}
		text := MxCell{
			ID:     fmt.Sprintf("legend_text_%d", i),
			Parent: "legend_container",
			Value:  entry.text,
			Style:  "text;html=1;strokeColor=none;fillColor=none;align=left;verticalAlign=middle;whiteSpace=wrap;rounded=0;fontSize=14;",
			Vertex: "1",
			Geometry: &Geometry{
				X:      70,
				Y:      float64(entry.y - 10),
				Width:  230,
				Height: 20,
				As:     "geometry",
			},
		}
		cells = append(cells, startPoint, endPoint, lineSample, text)
	}

	return cells
}

func groupLinks(links []Link) []LinkGroup {
	linkMap := make(map[string]LinkGroup)
	for _, link := range links {
		key := fmt.Sprintf("%s-%s", link.Source, link.Target)
		reverseKey := fmt.Sprintf("%s-%s", link.Target, link.Source)
		if group, exists := linkMap[key]; exists {
			group.Links = append(group.Links, link)
			linkMap[key] = group
		} else if group, exists := linkMap[reverseKey]; exists {
			group.Links = append(group.Links, link)
			linkMap[reverseKey] = group
		} else {
			linkMap[key] = LinkGroup{
				Source: link.Source,
				Target: link.Target,
				Links:  []Link{link},
			}
		}
	}
	result := make([]LinkGroup, 0, len(linkMap))
	for _, group := range linkMap {
		sort.Slice(group.Links, func(i, j int) bool {
			return group.Links[i].Speed < group.Links[j].Speed
		})
		result = append(result, group)
	}

	return result
}

func fixedConnectionPoint(cell *MxCell, targetX, targetY float64) (float64, float64) {
	if cell.Geometry == nil || cell.Geometry.Width == 0 || cell.Geometry.Height == 0 {
		return 0.5, 0.5
	}
	cx := cell.Geometry.X + float64(cell.Geometry.Width)/2
	cy := cell.Geometry.Y + float64(cell.Geometry.Height)/2
	dx := targetX - cx
	dy := targetY - cy
	if dx == 0 && dy == 0 {
		return 0.5, 0.5
	}
	halfWidth := float64(cell.Geometry.Width) / 2
	halfHeight := float64(cell.Geometry.Height) / 2
	scaleX := halfWidth / math.Abs(dx)
	scaleY := halfHeight / math.Abs(dy)
	scale := math.Min(scaleX, scaleY)
	ix := cx + dx*scale
	iy := cy + dy*scale
	rx := (ix - cell.Geometry.X) / float64(cell.Geometry.Width)
	ry := (iy - cell.Geometry.Y) / float64(cell.Geometry.Height)

	return rx, ry
}

func createParallelEdges(model *MxGraphModel, group LinkGroup, cellMap map[string]*MxCell, edgeGroupID int, style Style) {
	sourceCell, ok := cellMap[group.Source]
	if !ok || sourceCell.Geometry == nil {
		return
	}
	targetCell, ok := cellMap[group.Target]
	if !ok || targetCell.Geometry == nil {
		return
	}
	srcCenterX := sourceCell.Geometry.X + float64(sourceCell.Geometry.Width)/2
	srcCenterY := sourceCell.Geometry.Y + float64(sourceCell.Geometry.Height)/2
	tgtCenterX := targetCell.Geometry.X + float64(targetCell.Geometry.Width)/2
	tgtCenterY := targetCell.Geometry.Y + float64(targetCell.Geometry.Height)/2
	sx, sy := fixedConnectionPoint(sourceCell, tgtCenterX, tgtCenterY)
	tx, ty := fixedConnectionPoint(targetCell, srcCenterX, srcCenterY)
	srcDefaultX := sourceCell.Geometry.X + sx*float64(sourceCell.Geometry.Width)
	srcDefaultY := sourceCell.Geometry.Y + sy*float64(sourceCell.Geometry.Height)
	tgtDefaultX := targetCell.Geometry.X + tx*float64(targetCell.Geometry.Width)
	tgtDefaultY := targetCell.Geometry.Y + ty*float64(targetCell.Geometry.Height)
	vx := tgtDefaultX - srcDefaultX
	vy := tgtDefaultY - srcDefaultY
	baseLength := math.Sqrt(vx*vx + vy*vy)
	if baseLength == 0 {
		baseLength = 1
	}
	ux := vx / baseLength
	uy := vy / baseLength
	px := -uy
	py := ux
	numLinks := len(group.Links)
	for i, link := range group.Links {
		offset := (float64(i) - float64(numLinks-1)/2) * 10.0
		srcX := srcDefaultX + px*offset
		srcY := srcDefaultY + py*offset
		tgtX := tgtDefaultX + px*offset
		tgtY := tgtDefaultY + py*offset
		relSrcX := (srcX - sourceCell.Geometry.X) / float64(sourceCell.Geometry.Width)
		relSrcY := (srcY - sourceCell.Geometry.Y) / float64(sourceCell.Geometry.Height)
		relTgtX := (tgtX - targetCell.Geometry.X) / float64(targetCell.Geometry.Width)
		relTgtY := (tgtY - targetCell.Geometry.Y) / float64(targetCell.Geometry.Height)
		edgeID := fmt.Sprintf("e%d_%d", edgeGroupID, i)

		edgeStyle := GetLinkStyleFromTheme(link, style) +
			fmt.Sprintf("exitX=%.3f;exitY=%.3f;exitDx=0;exitDy=0;entryX=%.3f;entryY=%.3f;entryDx=0;entryDy=0;",
				relSrcX, relSrcY, relTgtX, relTgtY)

		edgeCell := MxCell{
			ID:     edgeID,
			Parent: "1",
			Source: group.Source,
			Target: group.Target,
			Style:  edgeStyle,
			Edge:   "1",
			Geometry: &Geometry{
				Relative: "1",
				Fixed:    "1",
				As:       "geometry",
			},
		}
		model.Root.MxCell = append(model.Root.MxCell, edgeCell)
		generateEdgeLabels(model, edgeID, link, srcX, srcY, tgtX, tgtY, ux, uy)
	}
}

func generateEdgeLabels(model *MxGraphModel, edgeID string, link Link, srcX, srcY, tgtX, tgtY, ux, _ float64) {
	const labelOffset = 10.0
	labelStyle := fmt.Sprintf("edgeLabel;html=1;align=center;verticalAlign=middle;resizable=0;rotation=%.2f;", calculateLabelRotation(srcX, srcY, tgtX, tgtY))
	swapLabels := (tgtX < srcX)

	var label1X, label2X float64
	var label1Value, label2Value string

	if !swapLabels {
		label1X = srcX + labelOffset*ux
		label2X = tgtX - labelOffset*ux
		label1Value = extractPort(link.Properties["sourcePort"])
		label2Value = extractPort(link.Properties["targetPort"])
	} else {
		label1X = srcX - labelOffset*ux
		label2X = tgtX + labelOffset*ux
		label1Value = extractPort(link.Properties["targetPort"])
		label2Value = extractPort(link.Properties["sourcePort"])
	}

	minXBB := math.Min(srcX, tgtX)
	maxXBB := math.Max(srcX, tgtX)
	widthBB := maxXBB - minXBB
	if widthBB == 0 {
		widthBB = 1
	}
	midBBX := (minXBB + maxXBB) / 2.0

	label1RelX := (label1X - midBBX) / widthBB
	label2RelX := (label2X - midBBX) / widthBB

	label1Value = fmt.Sprintf("<span style=\"font-size:10px;\">%s</span>", label1Value)
	label2Value = fmt.Sprintf("<span style=\"font-size:10px;\">%s</span>", label2Value)

	label1 := MxCell{
		ID:          edgeID + "_1",
		Parent:      edgeID,
		Value:       label1Value,
		Style:       labelStyle,
		Vertex:      "1",
		Connectable: "0",
		Geometry: &Geometry{
			Relative: "1",
			Fixed:    "1",
			As:       "geometry",
			X:        label1RelX,
		},
	}

	label2 := MxCell{
		ID:          edgeID + "_2",
		Parent:      edgeID,
		Value:       label2Value,
		Style:       labelStyle,
		Vertex:      "1",
		Connectable: "0",
		Geometry: &Geometry{
			Relative: "1",
			Fixed:    "1",
			As:       "geometry",
			X:        label2RelX,
		},
	}

	model.Root.MxCell = append(model.Root.MxCell, label1, label2)
}

func calculateLabelRotation(srcX, srcY, tgtX, tgtY float64) float64 {
	angle := math.Atan2(tgtY-srcY, tgtX-srcX) * (180 / math.Pi)
	if angle > 90 || angle < -90 {
		angle += 180
	}

	return angle
}
