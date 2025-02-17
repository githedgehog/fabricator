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

func GenerateDrawio(workDir string, jsonData []byte) error {
	outputFile := filepath.Join(workDir, "vlab-diagram.drawio")
	topo, err := ConvertJSONToTopology(jsonData)
	if err != nil {
		return fmt.Errorf("converting JSON to topology: %w", err)
	}

	model := generateDrawio(topo)
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

func generateDrawio(topo Topology) *MxGraphModel {
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
		Root: Root{
			MxCell: []MxCell{
				{ID: "0"},
				{ID: "1", Parent: "0"},
			},
		},
	}

	model.Root.MxCell = append(model.Root.MxCell, createLegend()...)

	const (
		nodeWidth     = 100
		nodeHeight    = 50
		horizontalGap = 120
		verticalGap   = 250
		canvasWidth   = 600
		spineY        = 100
	)

	layers := sortNodes(topo.Nodes, topo.Links)
	cellMap := make(map[string]*MxCell)

	totalSpineWidth := len(layers.Spine)*nodeWidth + (len(layers.Spine)-1)*horizontalGap
	spineStartX := (canvasWidth - totalSpineWidth) / 2
	for i, node := range layers.Spine {
		x := spineStartX + i*(nodeWidth+horizontalGap)
		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  node.Label,
			Style:  getNodeStyle(node),
			Vertex: "1",
			Geometry: &Geometry{
				X:      float64(x),
				Y:      float64(spineY),
				Width:  nodeWidth,
				Height: nodeHeight,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	totalLeafWidth := len(layers.Leaf)*nodeWidth + (len(layers.Leaf)-1)*horizontalGap
	leafStartX := (canvasWidth - totalLeafWidth) / 2
	leafY := spineY + verticalGap
	for i, node := range layers.Leaf {
		x := leafStartX + i*(nodeWidth+horizontalGap)
		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  node.Label,
			Style:  getNodeStyle(node),
			Vertex: "1",
			Geometry: &Geometry{
				X:      float64(x),
				Y:      float64(leafY),
				Width:  nodeWidth,
				Height: nodeHeight,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	totalServerWidth := len(layers.Server)*nodeWidth + (len(layers.Server)-1)*(horizontalGap/2)
	serverStartX := (canvasWidth - totalServerWidth) / 2
	serverY := leafY + verticalGap
	for i, node := range layers.Server {
		x := serverStartX + i*(nodeWidth+horizontalGap/2)
		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  node.Label,
			Style:  getNodeStyle(node),
			Vertex: "1",
			Geometry: &Geometry{
				X:      float64(x),
				Y:      float64(serverY),
				Width:  nodeWidth,
				Height: nodeHeight,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	linkGroups := groupLinks(topo.Links)
	for i, group := range linkGroups {
		createParallelEdges(model, group, cellMap, i)
	}

	return model
}

func getNodeStyle(node Node) string {
	baseStyle := "shape=rectangle;whiteSpace=wrap;html=1;fontSize=11;"
	rounded := "rounded=1;"
	colors := ""
	switch node.Type {
	case NodeTypeSwitch:
		if role, ok := node.Properties["role"]; ok && role == SwitchRoleSpine {
			colors = "fillColor=#f8cecc;strokeColor=#b85450;"
		} else {
			colors = "fillColor=#dae8fc;strokeColor=#6c8ebf;"
		}
	case NodeTypeServer:
		rounded = "rounded=0;"
		colors = "fillColor=#d5e8d4;strokeColor=#82b366;"
	}

	return baseStyle + rounded + colors
}

func createLegend() []MxCell {
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
		Style:  "rounded=0;whiteSpace=wrap;html=1;fillColor=#ffffff;strokeColor=#666666;",
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
		Style:  "text;html=1;strokeColor=none;fillColor=none;align=center;verticalAlign=middle;whiteSpace=wrap;rounded=0;fontSize=14;fontStyle=1",
		Vertex: "1",
		Geometry: &Geometry{
			Y:      10,
			Width:  320,
			Height: 20,
			As:     "geometry",
		},
	}
	legendEntries := []struct {
		y      int
		stroke string
		dash   bool
		width  int
		text   string
	}{
		{50, "#b85450", false, 3, "Fabric Links"},
		{80, "#2f5597", true, 2, "MCLAG Peer Links"},
		{110, "#4472c4", true, 2, "MCLAG Session Links"},
		{140, "#9cc1f7", true, 2, "MCLAG Server Links"},
		{170, "#82b366", false, 2, "Bundled Server Links"},
		{200, "#666666", false, 2, "Unbundled Server Links"},
		{230, "#d79b00", true, 2, "ESLAG Server Links"},
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
		lineStyle := fmt.Sprintf("endArrow=none;html=1;strokeWidth=%d;strokeColor=%s;%s",
			entry.width,
			entry.stroke,
			map[bool]string{true: "dashed=1;", false: ""}[entry.dash])
		lineSample := MxCell{
			ID:     fmt.Sprintf("legend_line_%d", i),
			Parent: "legend_container",
			Style:  lineStyle,
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

func createParallelEdges(model *MxGraphModel, group LinkGroup, cellMap map[string]*MxCell, edgeGroupID int) {
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
		edgeStyle := getLinkStyle(link, true) +
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

func getLinkStyle(link Link, isMiddle bool) string {
	style := "endArrow=none;html=1;strokeWidth=2;"
	switch link.Type {
	case EdgeTypeFabric:
		style += "strokeColor=#b85450;"
	case EdgeTypeMCLAG:
		if mclagType, ok := link.Properties["mclagType"]; ok {
			switch mclagType {
			case "peer":
				style += "strokeColor=#2f5597;dashed=1;"
			case "session":
				style += "strokeColor=#4472c4;dashed=1;"
			default:
				style += "strokeColor=#9cc1f7;dashed=1;"
			}
		} else {
			style += "strokeColor=#9cc1f7;dashed=1;"
		}
	case EdgeTypeBundled:
		style += "strokeColor=#82b366;"
	case EdgeTypeUnbundled:
		style += "strokeColor=#666666;"
	case EdgeTypeESLAG:
		style += "strokeColor=#d79b00;dashed=1;"
	default:
		style += "strokeColor=#000000;"
	}
	if isMiddle {
		style += "fontSize=10;spacing=5;"
	}

	return style
}
