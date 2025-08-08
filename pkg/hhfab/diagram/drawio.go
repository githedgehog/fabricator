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

var nodeConnectionsMap map[string][]float64

var nodes []Node

func GenerateDrawio(workDir string, topo Topology, styleType StyleType, outputPath string) error {
	var finalOutputPath string
	if outputPath != "" {
		finalOutputPath = outputPath
	} else {
		finalOutputPath = filepath.Join(workDir, DrawioFilename)
	}

	style := GetStyle(styleType)

	nodeConnectionsMap = make(map[string][]float64)

	model := createDrawioModel(topo, style)
	outputXML, err := xml.MarshalIndent(model, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling XML: %w", err)
	}
	xmlContent := []byte(xml.Header + string(outputXML))

	if err := os.MkdirAll(filepath.Dir(finalOutputPath), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	if err := os.WriteFile(finalOutputPath, xmlContent, 0o600); err != nil {
		return fmt.Errorf("writing draw.io file: %w", err)
	}

	return nil
}

func createDrawioModel(topo Topology, style Style) *MxGraphModel {
	nodes = topo.Nodes

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

	layers := sortNodes(topo.Nodes, topo.Links)
	linkGroups := groupLinks(topo.Links)

	model.Root.MxCell = append(model.Root.MxCell, createLegend(topo.Links, style)...)
	model.Root.MxCell = append(model.Root.MxCell, createHedgehogLogo()...)

	gatewayY := 50
	spineY := gatewayY + 250
	leafY := spineY + 250
	externalY := spineY + 100
	serverY := leafY + 250

	if len(layers.Spine) == 0 {
		leafY = spineY
		serverY = leafY + 250
	}

	// Detect mesh triangle for special positioning
	isMeshTriangle := detectMeshTriangle(layers.Leaf, topo.Links)
	hasGateway := len(layers.Gateway) > 0
	var meshTriangleUpperY int

	// If we have both gateway and mesh triangle, move mesh triangle down a tier
	if isMeshTriangle && hasGateway {
		leafY = gatewayY + 400              // Move leaves down to make room for gateway
		meshTriangleUpperY = gatewayY + 250 // Upper leaf positioned between gateway and lower leaves
		serverY = leafY + 250               // Adjust server position accordingly
	} else if isMeshTriangle {
		meshTriangleUpperY = leafY - 150
	}

	cellMap := make(map[string]*MxCell)

	canvasWidth := 600

	leafNodeWidth := 100
	var leafSpacing float64
	switch {
	case len(layers.Leaf) <= 3:
		leafSpacing = 200
	case len(layers.Leaf) <= 5:
		leafSpacing = 160
	default:
		leafSpacing = 120
	}

	totalLeafWidth := float64(len(layers.Leaf)*leafNodeWidth) + leafSpacing*float64(len(layers.Leaf)-1)
	leafCenterX := float64(canvasWidth) / 2

	spineNodeWidth := 100
	var spineSpacing float64

	if len(layers.Spine) > 1 {
		if len(layers.Spine) >= len(layers.Leaf) {
			spineSpacing = math.Max(120, (totalLeafWidth-float64(len(layers.Spine)*spineNodeWidth))/float64(len(layers.Spine)-1))
		} else {
			maxSpineWidth := totalLeafWidth * 0.8
			spineSpacing = math.Min(350, math.Max(150, (maxSpineWidth-float64(len(layers.Spine)*spineNodeWidth))/float64(len(layers.Spine)-1)))
		}
	} else {
		spineSpacing = 250
	}

	totalSpineWidth := float64(len(layers.Spine)*spineNodeWidth) + spineSpacing*float64(len(layers.Spine)-1)
	spineStartX := leafCenterX - (totalSpineWidth / 2)

	spinePositions := make([]float64, len(layers.Spine))
	for i, node := range layers.Spine {
		width, _ := GetNodeDimensions(node)
		spinePositions[i] = spineStartX + float64(i)*(float64(width)+spineSpacing)
	}

	gatewayNodeWidth := 100

	gatewaySpacing := 180.0
	if len(layers.Gateway) > 2 {
		gatewaySpacing = 150.0
	}

	totalGatewayWidth := float64(len(layers.Gateway)*gatewayNodeWidth) + gatewaySpacing*float64(len(layers.Gateway)-1)

	gatewayStartX := float64(canvasWidth)/2 - (totalGatewayWidth / 2)

	for i, node := range layers.Gateway {
		width, height := GetNodeDimensions(node)

		x := gatewayStartX + float64(i)*(float64(width)+gatewaySpacing)

		usingIconStyle := IsIconBasedStyle(style)

		if usingIconStyle {
			iconCell := MxCell{
				ID:     node.ID,
				Parent: "1",
				Style:  GetNodeStyle(node, style),
				Vertex: "1",
				Geometry: &Geometry{
					X:      x,
					Y:      float64(gatewayY),
					Width:  width,
					Height: height,
					As:     "geometry",
				},
			}

			labelCell := MxCell{
				ID:     fmt.Sprintf("%s_label", node.ID),
				Parent: "1",
				Value:  FormatNodeValue(node, style),
				Style:  GetGatewayLabelStyle(),
				Vertex: "1",
				Geometry: &Geometry{
					X:      x + 26,
					Y:      float64(gatewayY) + 70,
					Width:  48,
					Height: 13,
					As:     "geometry",
				},
			}

			model.Root.MxCell = append(model.Root.MxCell, iconCell, labelCell)
			cellMap[node.ID] = &iconCell
		} else {
			cell := MxCell{
				ID:     node.ID,
				Parent: "1",
				Value:  FormatNodeValue(node, style),
				Style:  GetNodeStyle(node, style),
				Vertex: "1",
				Geometry: &Geometry{
					X:      x,
					Y:      float64(gatewayY),
					Width:  width,
					Height: height,
					As:     "geometry",
				},
			}
			cellMap[node.ID] = &cell
			model.Root.MxCell = append(model.Root.MxCell, cell)
		}
	}

	for i, node := range layers.Spine {
		width, height := GetNodeDimensions(node)
		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  FormatNodeValue(node, style),
			Style:  GetNodeStyle(node, style),
			Vertex: "1",
			Geometry: &Geometry{
				X:      spinePositions[i],
				Y:      float64(spineY),
				Width:  width,
				Height: height,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	leafStartX := leafCenterX - (totalLeafWidth / 2)

	for i, node := range layers.Leaf {
		width, height := GetNodeDimensions(node)
		x := leafStartX + float64(i)*(float64(width)+leafSpacing)

		// For mesh triangle, put the second leaf (index 1) in upper tier
		nodeY := float64(leafY)
		if isMeshTriangle && i == 1 {
			nodeY = float64(meshTriangleUpperY)
		}

		cell := MxCell{
			ID:     node.ID,
			Parent: "1",
			Value:  FormatNodeValue(node, style),
			Style:  GetNodeStyle(node, style),
			Vertex: "1",
			Geometry: &Geometry{
				X:      x,
				Y:      nodeY,
				Width:  width,
				Height: height,
				As:     "geometry",
			},
		}
		cellMap[node.ID] = &cell
		model.Root.MxCell = append(model.Root.MxCell, cell)
	}

	if len(layers.External) > 0 {
		leftExternals, rightExternals := splitExternalNodes(layers.External, topo.Links, layers.Leaf)

		externalCenterY := float64(externalY)
		externalDistance := 120.0
		leftmostLeafX := leafStartX
		rightmostLeafX := leafStartX + totalLeafWidth - 100
		leftExternalX := leftmostLeafX - externalDistance
		rightExternalX := rightmostLeafX + externalDistance

		for i, node := range leftExternals {
			width, height := GetNodeDimensions(node)

			var y float64
			if len(leftExternals) == 1 {
				y = externalCenterY
			} else {
				nodeSpacing := float64(height) + 30
				totalHeight := float64(len(leftExternals)-1) * nodeSpacing
				startY := externalCenterY - totalHeight/2
				y = startY + float64(i)*nodeSpacing
			}

			cell := MxCell{
				ID:     node.ID,
				Parent: "1",
				Value:  FormatNodeValue(node, style),
				Style:  GetNodeStyle(node, style),
				Vertex: "1",
				Geometry: &Geometry{
					X:      leftExternalX,
					Y:      y,
					Width:  width,
					Height: height,
					As:     "geometry",
				},
			}
			cellMap[node.ID] = &cell
			model.Root.MxCell = append(model.Root.MxCell, cell)
		}

		for i, node := range rightExternals {
			width, height := GetNodeDimensions(node)

			var y float64
			if len(rightExternals) == 1 {
				y = externalCenterY
			} else {
				nodeSpacing := float64(height) + 30
				totalHeight := float64(len(rightExternals)-1) * nodeSpacing
				startY := externalCenterY - totalHeight/2
				y = startY + float64(i)*nodeSpacing
			}

			cell := MxCell{
				ID:     node.ID,
				Parent: "1",
				Value:  FormatNodeValue(node, style),
				Style:  GetNodeStyle(node, style),
				Vertex: "1",
				Geometry: &Geometry{
					X:      rightExternalX,
					Y:      y,
					Width:  width,
					Height: height,
					As:     "geometry",
				},
			}
			cellMap[node.ID] = &cell
			model.Root.MxCell = append(model.Root.MxCell, cell)
		}
	}

	serverNodeWidth := 100
	var serverSpacing float64 = 60

	totalServerWidth := float64(len(layers.Server)*serverNodeWidth) + serverSpacing*float64(len(layers.Server)-1)
	serverStartX := leafCenterX - (totalServerWidth / 2)

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

	nodeConnectionsMap = make(map[string][]float64)

	for i, group := range linkGroups {
		createParallelEdges(model, group, cellMap, i, style)
	}

	return model
}

func createHedgehogLogo() []MxCell {
	logoContainer := MxCell{
		ID:     "hedgehog_logo",
		Parent: "1",
		Style:  "shape=image;aspect=fixed;image=" + HedgehogLogoSVG,
		Vertex: "1",
		Geometry: &Geometry{
			X:      820,
			Y:      10,
			Width:  150,
			Height: 30,
			As:     "geometry",
		},
	}

	return []MxCell{logoContainer}
}

func createLegend(links []Link, style Style) []MxCell {
	container := MxCell{
		ID:     "legend_container",
		Parent: "1",
		Style:  "group",
		Vertex: "1",
		Geometry: &Geometry{
			X:      -400,
			Y:      10,
			Width:  320,
			Height: 280,
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
			Height: 280,
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

	// Collect unique link types present in the topology
	linkTypesMap := make(map[string]bool)

	for _, link := range links {
		// First check for MCLAG links which use "mclagType" property
		if mclagType, ok := link.Properties[PropMCLAGType]; ok {
			if mclagType == MCLAGTypePeer {
				linkTypesMap[LegendKeyMCLAGPeer] = true
			} else if mclagType == MCLAGTypeSession {
				linkTypesMap[LegendKeyMCLAGSession] = true
			}
		} else if _, ok := link.Properties[PropBundled]; ok {
			linkTypesMap[LegendKeyBundled] = true
		} else if _, ok := link.Properties[PropESLAGServer]; ok {
			linkTypesMap[LegendKeyESLAGServer] = true
		} else if _, ok := link.Properties[PropGateway]; ok {
			linkTypesMap[LegendKeyGateway] = true
		} else {
			switch link.Type {
			case EdgeTypeMCLAG:
				// If it's an MCLAG link without a specific type, it's a server link
				linkTypesMap[LegendKeyMCLAGServer] = true
			case EdgeTypeBundled:
				linkTypesMap[LegendKeyBundled] = true
			case EdgeTypeESLAG:
				linkTypesMap[LegendKeyESLAGServer] = true
			case EdgeTypeGateway:
				linkTypesMap[LegendKeyGateway] = true
			case EdgeTypeExternal:
				linkTypesMap[LegendKeyExternal] = true
			case EdgeTypeStaticExternal:
				linkTypesMap[LegendKeyStaticExternal] = true
			case EdgeTypeMesh:
				linkTypesMap[LegendKeyMesh] = true
			case EdgeTypeFabric:
				linkTypesMap[LegendKeyFabric] = true
			default:
				// For other links, determine type based on node roles
				sourceNodeFound := false
				targetNodeFound := false
				var sourceNode, targetNode Node

				// Find source node
				for _, n := range nodes {
					if n.ID == link.Source {
						sourceNode = n
						sourceNodeFound = true

						break
					}
				}

				// Find target node
				for _, n := range nodes {
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
							linkTypesMap[LegendKeyFabric] = true
						} else if sourceRole == SwitchRoleLeaf && targetRole == SwitchRoleLeaf {
							// For leaf-to-leaf connections, check if it's mesh or fabric
							if link.Type == EdgeTypeMesh {
								linkTypesMap[LegendKeyMesh] = true
							} else {
								// Default leaf-to-leaf connections to fabric unless explicitly mesh
								linkTypesMap[LegendKeyFabric] = true
							}
						}
					} else if (sourceType == NodeTypeSwitch && targetType == NodeTypeServer) ||
						(sourceType == NodeTypeServer && targetType == NodeTypeSwitch) {
						linkTypesMap[LegendKeyUnbundled] = true
					}
				}
			}
		}
	}

	// Create legend entries based on detected link types
	legendEntries := []struct {
		linkType string
		style    string
		text     string
	}{
		{LegendKeyFabric, style.FabricLinkStyle, "Fabric Links"},
		{LegendKeyMesh, style.MeshLinkStyle, "Mesh Links"},
		{LegendKeyMCLAGPeer, style.MCLAGPeerStyle, "MCLAG Peer Links"},
		{LegendKeyMCLAGSession, style.MCLAGSessionStyle, "MCLAG Session Links"},
		{LegendKeyMCLAGServer, style.MCLAGServerStyle, "MCLAG Server Links"},
		{LegendKeyBundled, style.BundledServerStyle, "Bundled Server Links"},
		{LegendKeyUnbundled, style.UnbundledStyle, "Unbundled Server Links"},
		{LegendKeyESLAGServer, style.ESLAGServerStyle, "ESLAG Server Links"},
		{LegendKeyGateway, style.GatewayLinkStyle, "Gateway Links"},
		{LegendKeyExternal, style.ExternalLinkStyle, "External Links"},
		{LegendKeyStaticExternal, style.StaticExternalLinkStyle, "Static External Links"},
	}

	cells := make([]MxCell, 0, 3+4*len(legendEntries))
	cells = append(cells, container, background, title)

	// Only include legend entries for link types present in the topology
	y := 50
	for i, entry := range legendEntries {
		if linkTypesMap[entry.linkType] {
			startPoint := MxCell{
				ID:     fmt.Sprintf("legend_line_%d_start", i),
				Parent: "legend_container",
				Style:  "point;",
				Vertex: "1",
				Geometry: &Geometry{
					X:      20,
					Y:      float64(y),
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
					Y:      float64(y),
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
					Y:      float64(y - 10),
					Width:  230,
					Height: 20,
					As:     "geometry",
				},
			}
			cells = append(cells, startPoint, endPoint, lineSample, text)

			// Only increment y for legend entries that are actually added
			y += 30
		}
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

func createParallelEdges(model *MxGraphModel, group LinkGroup, cellMap map[string]*MxCell, edgeGroupID int, style Style) {
	sourceCell, ok := cellMap[group.Source]
	if !ok || sourceCell.Geometry == nil {
		return
	}

	targetCell, ok := cellMap[group.Target]
	if !ok || targetCell.Geometry == nil {
		return
	}

	connectionType := getConnectionType(group.Source, group.Target)

	if nodeConnectionsMap == nil {
		nodeConnectionsMap = make(map[string][]float64)
	}

	// Calculate center points
	srcCenterX := sourceCell.Geometry.X + float64(sourceCell.Geometry.Width)/2
	srcCenterY := sourceCell.Geometry.Y + float64(sourceCell.Geometry.Height)/2
	tgtCenterX := targetCell.Geometry.X + float64(targetCell.Geometry.Width)/2
	tgtCenterY := targetCell.Geometry.Y + float64(targetCell.Geometry.Height)/2

	// Calculate connection points
	sx, sy := calculateOptimalConnectionPoint(sourceCell, tgtCenterX, tgtCenterY, nodeConnectionsMap)
	tx, ty := calculateOptimalConnectionPoint(targetCell, srcCenterX, srcCenterY, nodeConnectionsMap)

	// Calculate absolute coordinates of connection points
	srcDefaultX := sourceCell.Geometry.X + sx*float64(sourceCell.Geometry.Width)
	srcDefaultY := sourceCell.Geometry.Y + sy*float64(sourceCell.Geometry.Height)
	tgtDefaultX := targetCell.Geometry.X + tx*float64(targetCell.Geometry.Width)
	tgtDefaultY := targetCell.Geometry.Y + ty*float64(targetCell.Geometry.Height)

	// Calculate edge vector and normalize
	vx := tgtDefaultX - srcDefaultX
	vy := tgtDefaultY - srcDefaultY
	baseLength := math.Sqrt(vx*vx + vy*vy)
	if baseLength == 0 {
		baseLength = 1
	}
	ux := vx / baseLength
	uy := vy / baseLength

	// Calculate perpendicular vector for offset
	px := -uy
	py := ux

	// Check for leaf-to-leaf connection using the connection type
	isLeafToLeaf := connectionType == ConnTypeLeafToLeaf

	// Set vertical offset for leaf-to-leaf connections
	var verticalOffset float64
	if isLeafToLeaf {
		verticalOffset = 12.0
	}

	// Check for spine-to-distant-leaf connection that needs special handling
	isSpineToDistantLeaf, spineLeafOffset := calculateSpineToLeafOffset(group.Source, group.Target, cellMap)

	// Process each link in the group
	numLinks := len(group.Links)
	baseSpacing := 10.0

	for i, link := range group.Links {
		// Calculate offset
		offset := (float64(i) - float64(numLinks-1)/2) * baseSpacing

		// Apply the perpendicular offset
		srcX := srcDefaultX + px*offset
		srcY := srcDefaultY + py*offset
		tgtX := tgtDefaultX + px*offset
		tgtY := tgtDefaultY + py*offset

		// Apply vertical offset for leaf-to-leaf connections
		if isLeafToLeaf {
			srcY += verticalOffset
			tgtY += verticalOffset
		}

		// Apply additional vertical adjustment for spine-to-distant-leaf connections
		if isSpineToDistantLeaf {
			srcY += spineLeafOffset
		}

		// Calculate relative positions for connection points
		relSrcX := (srcX - sourceCell.Geometry.X) / float64(sourceCell.Geometry.Width)
		relSrcY := (srcY - sourceCell.Geometry.Y) / float64(sourceCell.Geometry.Height)
		relTgtX := (tgtX - targetCell.Geometry.X) / float64(targetCell.Geometry.Width)
		relTgtY := (tgtY - targetCell.Geometry.Y) / float64(targetCell.Geometry.Height)

		// Create edge ID
		edgeID := fmt.Sprintf("e%d_%d", edgeGroupID, i)

		// Create edge style
		edgeStyle := GetLinkStyleFromTheme(link, style) +
			fmt.Sprintf("exitX=%.3f;exitY=%.3f;exitDx=0;exitDy=0;entryX=%.3f;entryY=%.3f;entryDx=0;entryDy=0;",
				relSrcX, relSrcY, relTgtX, relTgtY)

		// Create the edge cell
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

		// Add the edge to the model
		model.Root.MxCell = append(model.Root.MxCell, edgeCell)

		// Generate labels with fixed call
		ux, uy := calculateUnitVector(srcX, srcY, tgtX, tgtY)
		generateEdgeLabels(model, edgeID, link, srcX, srcY, tgtX, tgtY, ux, uy)
	}
}

func calculateSpineToLeafOffset(source, target string, cellMap map[string]*MxCell) (bool, float64) {
	sourceNode := findNode(nodes, source)
	targetNode := findNode(nodes, target)

	sourceType, sourceRole := getNodeTypeInfo(sourceNode)
	targetType, targetRole := getNodeTypeInfo(targetNode)

	isSpineToLeaf := (sourceType == NodeTypeSwitch && sourceRole == SwitchRoleSpine &&
		targetType == NodeTypeSwitch && targetRole == SwitchRoleLeaf) ||
		(targetType == NodeTypeSwitch && targetRole == SwitchRoleSpine &&
			sourceType == NodeTypeSwitch && sourceRole == SwitchRoleLeaf)

	if !isSpineToLeaf {
		return false, 0
	}

	var spineNode, leafNode Node
	if sourceType == NodeTypeSwitch && sourceRole == SwitchRoleSpine {
		spineNode = sourceNode
		leafNode = targetNode
	} else {
		spineNode = targetNode
		leafNode = sourceNode
	}

	spinePositions := make(map[string]float64)
	leafPositions := make(map[string]float64)
	spineIDs := []string{}
	leafIDs := []string{}

	for _, node := range nodes {
		nodeType, nodeRole := getNodeTypeInfo(node)
		if nodeType != NodeTypeSwitch {
			continue
		}

		cell, ok := cellMap[node.ID]
		if !ok || cell.Geometry == nil {
			continue
		}

		centerX := cell.Geometry.X + float64(cell.Geometry.Width)/2

		if nodeRole == SwitchRoleSpine {
			spinePositions[node.ID] = centerX
			spineIDs = append(spineIDs, node.ID)
		} else if nodeRole == SwitchRoleLeaf {
			leafPositions[node.ID] = centerX
			leafIDs = append(leafIDs, node.ID)
		}
	}

	// Rule 1: If 2 spines and 5 or fewer leaves, no offset
	if len(spineIDs) <= 2 && len(leafIDs) <= 5 {
		return false, 0
	}

	sort.Slice(spineIDs, func(i, j int) bool {
		return spinePositions[spineIDs[i]] < spinePositions[spineIDs[j]]
	})

	sort.Slice(leafIDs, func(i, j int) bool {
		return leafPositions[leafIDs[i]] < leafPositions[leafIDs[j]]
	})

	// Find the leaf's position in the sorted array
	leafIndex := -1
	for i, id := range leafIDs {
		if id == leafNode.ID {
			leafIndex = i

			break
		}
	}

	// Get positions for calculations
	leftmostSpineID := spineIDs[0]
	rightmostSpineID := spineIDs[len(spineIDs)-1]

	// Handle special case: 3 spines and 5 leaves
	if len(spineIDs) == 3 && len(leafIDs) == 5 {
		// Only offset the outermost leaves (first and last)
		if leafIndex == 0 || leafIndex == len(leafIDs)-1 {
			spineX := spinePositions[spineNode.ID]
			leafX := leafPositions[leafNode.ID]
			spineCenterX := (spinePositions[leftmostSpineID] + spinePositions[rightmostSpineID]) / 2
			leafCenterX := (leafPositions[leafIDs[0]] + leafPositions[leafIDs[len(leafIDs)-1]]) / 2

			// Only apply if spine and leaf are on opposite sides of center
			if (spineX < spineCenterX && leafX > leafCenterX) ||
				(spineX > spineCenterX && leafX < leafCenterX) {
				return true, -15.0
			}
		}
	} else if (len(spineIDs) == 2 && len(leafIDs) >= 6) || (len(spineIDs) >= 3 && len(leafIDs) >= 6) {
		// For 2 spines and 6+ leaves OR 3+ spines and 6+ leaves
		// Apply offset for the 2 leftmost and 2 rightmost leaves
		if leafIndex < 2 || leafIndex >= len(leafIDs)-2 {
			spineX := spinePositions[spineNode.ID]
			leafX := leafPositions[leafNode.ID]
			spineCenterX := (spinePositions[leftmostSpineID] + spinePositions[rightmostSpineID]) / 2
			leafCenterX := (leafPositions[leafIDs[0]] + leafPositions[leafIDs[len(leafIDs)-1]]) / 2

			// Only apply if spine and leaf are on opposite sides of center
			if (spineX < spineCenterX && leafX > leafCenterX) ||
				(spineX > spineCenterX && leafX < leafCenterX) {
				return true, -15.0
			}
		}
	}

	return false, 0
}

func generateEdgeLabels(model *MxGraphModel, edgeID string, link Link, srcX, srcY, tgtX, tgtY, ux, uy float64) {
	// Calculate vector properties
	dx := tgtX - srcX
	dy := tgtY - srcY
	edgeLength := math.Sqrt(dx*dx + dy*dy)

	// Skip if edge is too short
	if edgeLength < 10 {
		return
	}

	// Retrieve port labels from link properties
	srcPort := extractPort(link.Properties["sourcePort"])
	tgtPort := extractPort(link.Properties["targetPort"])

	// Format the label values with proper font size
	srcText := fmt.Sprintf("<span style=\"font-size:10px;\">%s</span>", srcPort)
	tgtText := fmt.Sprintf("<span style=\"font-size:10px;\">%s</span>", tgtPort)

	// Calculate rotation angle for label readability using the utility function
	angle := calculateLabelRotation(srcX, srcY, tgtX, tgtY)

	// Calculate dynamic vertical offset based on the edge angle
	verticalOffset := calculateVerticalOffset(angle)

	// Fixed distance from node (in pixels)
	const fixedDistance = 30.0

	// Use the provided ux and uy as unit vectors if they're valid
	// Otherwise calculate them from the edge
	var unitX, unitY float64
	if ux != 0 || uy != 0 {
		unitX = ux
		unitY = uy
	} else {
		unitX = dx / edgeLength
		unitY = dy / edgeLength
	}

	// Calculate perpendicular vector (for vertical adjustment)
	perpX := -unitY
	perpY := unitX

	// Calculate label positions with both distance from node and vertical offset
	srcLabelX := srcX + (unitX * fixedDistance) + (perpX * verticalOffset)
	srcLabelY := srcY + (unitY * fixedDistance) + (perpY * verticalOffset)
	tgtLabelX := tgtX - (unitX * fixedDistance) + (perpX * verticalOffset)
	tgtLabelY := tgtY - (unitY * fixedDistance) + (perpY * verticalOffset)

	// Calculate widths with better padding
	srcWidth := len(srcPort)*4 + 8 // More padding around text
	tgtWidth := len(tgtPort)*4 + 8 // More padding around text

	// Slightly taller height to better center the text
	const labelHeight = 10

	// Style with improved alignment settings
	textStyle := fmt.Sprintf("text;html=1;strokeColor=#888888;strokeWidth=0.5;"+
		"fillColor=#FFFFFF;fillOpacity=80;align=center;verticalAlign=middle;"+
		"whiteSpace=wrap;rounded=1;fontSize=10;rotation=%.1f;",
		angle)

	// Create unique IDs for the text elements
	srcLabelID := fmt.Sprintf("%s_src_label", edgeID)
	tgtLabelID := fmt.Sprintf("%s_tgt_label", edgeID)

	// Create source port label as a separate text element
	srcLabelCell := MxCell{
		ID:     srcLabelID,
		Parent: "1", // Attach directly to the root, not to the edge
		Value:  srcText,
		Style:  textStyle,
		Vertex: "1",
		Geometry: &Geometry{
			X:      srcLabelX - float64(srcWidth)/2,    // Center the text horizontally
			Y:      srcLabelY - float64(labelHeight)/2, // Center the text vertically
			Width:  srcWidth,
			Height: labelHeight,
			As:     "geometry",
		},
	}

	// Create target port label as a separate text element
	tgtLabelCell := MxCell{
		ID:     tgtLabelID,
		Parent: "1", // Attach directly to the root, not to the edge
		Value:  tgtText,
		Style:  textStyle,
		Vertex: "1",
		Geometry: &Geometry{
			X:      tgtLabelX - float64(tgtWidth)/2,    // Center the text horizontally
			Y:      tgtLabelY - float64(labelHeight)/2, // Center the text vertically
			Width:  tgtWidth,
			Height: labelHeight,
			As:     "geometry",
		},
	}

	// Add the label cells to the model only if they have text
	if srcPort != "" {
		model.Root.MxCell = append(model.Root.MxCell, srcLabelCell)
	}

	if tgtPort != "" {
		model.Root.MxCell = append(model.Root.MxCell, tgtLabelCell)
	}
}

// calculateLabelRotation returns the appropriate angle for text labels along an edge
// with improved alignment for better readability.
func calculateLabelRotation(srcX, srcY, tgtX, tgtY float64) float64 {
	// Calculate basic angle using arctangent
	angle := math.Atan2(tgtY-srcY, tgtX-srcX) * (180 / math.Pi)

	// If the angle would result in upside-down text, flip it
	if angle > 90 || angle < -90 {
		angle += 180
	}

	return angle
}

// calculateVerticalOffset returns the appropriate vertical offset for a given angle
// to ensure consistent alignment across different edge angles
func calculateVerticalOffset(angleDegrees float64) float64 {
	// Normalize angle to 0-180 range for calculation
	normalizedAngle := math.Abs(math.Mod(angleDegrees, 180))

	// Use switch statement for clearer code structure
	switch {
	case normalizedAngle < 30:
		// Near horizontal (0-30 degrees)
		return 0.8
	case normalizedAngle < 60:
		// Diagonal (30-60 degrees)
		return 0.5
	case normalizedAngle < 120:
		// Near vertical (60-120 degrees)
		return 0.3
	case normalizedAngle < 150:
		// Diagonal (120-150 degrees)
		return 0.5
	default:
		// Near horizontal (150-180 degrees)
		return 0.8
	}
}

func calculateOptimalConnectionPoint(cell *MxCell, targetX, targetY float64, nodeConnectionsMap map[string][]float64) (float64, float64) {
	if cell.Geometry == nil || cell.Geometry.Width == 0 || cell.Geometry.Height == 0 {
		return 0.5, 0.5 // Default to center if no geometry
	}

	// Calculate center of the cell
	cx := cell.Geometry.X + float64(cell.Geometry.Width)/2
	cy := cell.Geometry.Y + float64(cell.Geometry.Height)/2

	// Calculate vector from center to target
	dx := targetX - cx
	dy := targetY - cy

	// Handle the case where target is at the same position as cell center
	if dx == 0 && dy == 0 {
		return 0.5, 0.5
	}

	// Calculate angle of approach (in radians)
	angle := math.Atan2(dy, dx)

	// Convert to degrees for easier comparison
	angleDeg := angle * 180 / math.Pi

	// Round angle to nearest sector (to group similar approaches)
	// Using 15-degree sectors as in the original version
	sectorSize := 15.0
	sectorAngle := math.Round(angleDeg/sectorSize) * sectorSize

	// Store this angle in the node connections map to track distribution
	// Using the node ID as key ensures we track per-node
	connections := nodeConnectionsMap[cell.ID]

	// If this is the first connection at this angle, initialize
	if connections == nil {
		connections = make([]float64, 0)
	}

	// Check if we already have connections at this exact sector angle
	// We only care about exact matches to maintain symmetry
	connectionCount := 0
	for _, existingAngle := range connections {
		if math.Abs(existingAngle-sectorAngle) < 0.001 { // Almost exact match
			connectionCount++
		}
	}

	// Check for opposing angle - connections from opposite sides need special handling
	// This is important for symmetry between opposing sides
	opposingAngle := sectorAngle + 180
	if opposingAngle > 180 {
		opposingAngle -= 360
	}
	opposingCount := 0
	for _, existingAngle := range connections {
		if math.Abs(existingAngle-opposingAngle) < 0.001 {
			opposingCount++
		}
	}

	// Add this angle to the connections
	nodeConnectionsMap[cell.ID] = append(connections, sectorAngle)

	// Apply minimal adjustment only when we have exact overlaps
	var adjustmentFactor float64

	// Only adjust if we have multiple connections at the exact same angle
	if connectionCount > 0 {
		// Apply a small fixed offset per connection, symmetrically
		adjustmentFactor = float64(connectionCount) * 0.2

		// Use node metadata for spine-specific adjustment
		node := findNode(nodes, cell.ID)
		nodeType, nodeRole := getNodeTypeInfo(node)

		// For spine nodes, which have many connections, apply slightly larger offset
		if nodeType == NodeTypeSwitch && nodeRole == SwitchRoleSpine && connectionCount > 1 {
			adjustmentFactor *= 1.1
		}
	}

	// Convert back to radians with adjustment
	adjustedAngle := (sectorAngle + adjustmentFactor) * math.Pi / 180

	// Re-calculate dx, dy with adjusted angle
	dx = math.Cos(adjustedAngle)
	dy = math.Sin(adjustedAngle)

	// Find intersection with rectangle sides
	halfWidth := float64(cell.Geometry.Width) / 2
	halfHeight := float64(cell.Geometry.Height) / 2

	scaleX := halfWidth / math.Abs(dx)
	scaleY := halfHeight / math.Abs(dy)
	scale := math.Min(scaleX, scaleY)

	ix := cx + dx*scale
	iy := cy + dy*scale

	// Convert to relative coordinates (0-1 range)
	rx := (ix - cell.Geometry.X) / float64(cell.Geometry.Width)
	ry := (iy - cell.Geometry.Y) / float64(cell.Geometry.Height)

	return rx, ry
}

func calculateUnitVector(x1, y1, x2, y2 float64) (float64, float64) {
	dx := x2 - x1
	dy := y2 - y1
	length := math.Sqrt(dx*dx + dy*dy)

	if length < 1e-6 {
		return 0, 0
	}

	return dx / length, dy / length
}

func getConnectionType(source, target string) string {
	sourceNode := findNode(nodes, source)
	targetNode := findNode(nodes, target)

	sourceType, sourceRole := getNodeTypeInfo(sourceNode)
	targetType, targetRole := getNodeTypeInfo(targetNode)

	switch {
	case sourceType == NodeTypeSwitch && targetType == NodeTypeSwitch:
		switch {
		case sourceRole == SwitchRoleLeaf && targetRole == SwitchRoleLeaf:
			return ConnTypeLeafToLeaf
		case sourceRole == SwitchRoleSpine && targetRole == SwitchRoleLeaf:
			return ConnTypeSpineToLeaf
		case sourceRole == SwitchRoleLeaf && targetRole == SwitchRoleSpine:
			return ConnTypeLeafToSpine
		default:
			return ConnTypeSwitchToSwitch
		}
	case (sourceType == NodeTypeSwitch && targetType == NodeTypeServer) ||
		(sourceType == NodeTypeServer && targetType == NodeTypeSwitch):
		return ConnTypeServerConnection
	case sourceType == NodeTypeGateway || targetType == NodeTypeGateway:
		return ConnTypeGatewayConnection
	}

	return ConnTypeUnknown
}
