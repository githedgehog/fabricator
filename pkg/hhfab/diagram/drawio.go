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

	// Create dynamic legend that only shows link types present in the topology
	model.Root.MxCell = append(model.Root.MxCell, createLegend(topo.Links, style)...)
	model.Root.MxCell = append(model.Root.MxCell, createHedgehogLogo()...)

	gatewayY := 50
	spineY := gatewayY + 250
	leafY := spineY + 250
	serverY := leafY + 250

	// If no spine layer exists, move leaf and server layers up
	if len(layers.Spine) == 0 {
		leafY = spineY
		serverY = leafY + 250
	}

	cellMap := make(map[string]*MxCell)

	canvasWidth := 600

	leafNodeWidth := 100
	var leafSpacing float64
	if len(layers.Leaf) <= 3 { //nolint:gocritic
		leafSpacing = 200
	} else if len(layers.Leaf) <= 5 {
		leafSpacing = 160
	} else {
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
			maxSpineWidth := totalLeafWidth * 0.8 // Make spine layer about 80% of leaf layer width
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
				Value:  node.Label,
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
				Value:  node.Label,
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
		Style:  "shape=image;aspect=fixed;image=data:image/svg+xml,PD94bWwgdmVyc2lvbj0iMS4wIiBlbmNvZGluZz0idXRmLTgiPz4KPCEtLSBHZW5lcmF0b3I6IEFkb2JlIElsbHVzdHJhdG9yIDI4LjMuMCwgU1ZHIEV4cG9ydCBQbHVnLUluIC4gU1ZHIFZlcnNpb246IDYuMDAgQnVpbGQgMCkgIC0tPgo8c3ZnIHZlcnNpb249IjEuMSIgaWQ9IkxheWVyXzEiIHhtbG5zPSJodHRwOi8vd3d3LnczLm9yZy8yMDAwL3N2ZyIgeG1sbnM6eGxpbms9Imh0dHA6Ly93d3cudzMub3JnLzE5OTkveGxpbmsiIHg9IjBweCIgeT0iMHB4IgoJIHdpZHRoPSI5MTdweCIgaGVpZ2h0PSIxOTVweCIgdmlld0JveD0iMCAwIDkxNyAxOTUiIHN0eWxlPSJlbmFibGUtYmFja2dyb3VuZDpuZXcgMCAwIDkxNyAxOTU7IiB4bWw6c3BhY2U9InByZXNlcnZlIj4KPHN0eWxlIHR5cGU9InRleHQvY3NzIj4KCS5zdDB7ZmlsbDojMzMzMzMzO30KCS5zdDF7ZmlsbDojRTJDNDc5O30KCS5zdDJ7ZmlsbDp1cmwoI1BhdGhfNF8wMDAwMDE1NzMwNzk4MDIwNDEwMDc5NzkyMDAwMDAxNTM5NDc3MTEzODU5MDQxMzIzOF8pO30KCS5zdDN7ZmlsbDojODA4MDgwO30KCS5zdDR7ZmlsbDojNkY0RTJDO30KPC9zdHlsZT4KPGcgaWQ9Ikdyb3VwXzUwIiB0cmFuc2Zvcm09InRyYW5zbGF0ZSgwIDApIj4KCTxwYXRoIGlkPSJQYXRoXzEwMyIgY2xhc3M9InN0MCIgZD0iTTg4Mi4xLDE0NS4yYy0yMS43LDEzLTQyLDEwLjktNTUuNy01LjFjLTE1LjYtMTkuNC0xNS42LTQ3LTAuMS02Ni40CgkJYzE0LTE2LjMsMzQuMS0xOC41LDUyLjQtNy4yYzMuNS0xLjgsNy4xLTMuMiwxMS00YzguOC0wLjUsMTcuNi0wLjIsMjYuNS0wLjJjMC40LDEuMSwwLjYsMi4yLDAuOCwzLjRjMCwyOS43LDAuNCw1OS40LTAuMSw4OS4xCgkJYzAuNiwxNi4zLTkuNiwzMS4yLTI1LjEsMzYuNGMtMTguNyw3LjYtMzkuOSw2LjEtNTcuMy00LjFjLTUuNS0zLjktMTAuNi04LjQtMTUuMi0xMy4ybDI4LjQtMTYuNmM4LjIsOC4zLDE3LjcsMTEuNiwyOC42LDYuMwoJCUM4ODMuOCwxNTkuNyw4ODIuMywxNTIuNiw4ODIuMSwxNDUuMiBNODY1LjksOTAuMmMtOC42LTAuNi0xNiw2LTE2LjUsMTQuNmMwLDAuNywwLDEuMywwLDJjMCw5LjEsNy4zLDE2LjUsMTYuNCwxNi41CgkJYzkuMSwwLDE2LjUtNy4zLDE2LjUtMTYuNGMwLjctOC41LTUuNS0xNS45LTE0LTE2LjZDODY3LjUsOTAuMSw4NjYuNyw5MC4xLDg2NS45LDkwLjIiLz4KCTxwYXRoIGlkPSJQYXRoXzEwNCIgY2xhc3M9InN0MCIgZD0iTTM4Ny42LDY2LjhjMy4yLTIsNi42LTMuNiwxMC4yLTQuNmM4LjctMC41LDE3LjUtMC4yLDI2LjktMC4yYzAuMiwzLjEsMC40LDUuNSwwLjQsNy45CgkJYzAsMjYuOCwwLDUzLjUsMCw4MC4zYzAsMjQuNS0xMC45LDM4LjYtMzQuOSw0My43Yy0xNS45LDQtMzIuOCwxLjYtNDYuOS02LjhjLTUuNi00LjEtMTEtOC42LTE1LjktMTMuNWwyOC41LTE2LjcKCQljOC4zLDguNiwxOC4xLDEyLDI5LjEsNmM3LTMuOCw2LjQtMTAuNyw1LjUtMTguOGMtMTAuNywxMC4xLTIyLjksMTEuMi0zNS41LDguNmMtOS43LTIuMy0xOC4xLTguMy0yMy40LTE2LjcKCQljLTEzLjMtMTkuMi0xMC44LTQ5LDUuNS02NS4yQzM1MC41LDU3LDM3Mi4xLDU1LjMsMzg3LjYsNjYuOCBNMzczLjgsOTAuMWMtOS4yLDAtMTYuNiw3LjUtMTYuNiwxNi42YzAsOS4yLDcuNSwxNi42LDE2LjYsMTYuNgoJCWMwLjIsMCwwLjUsMCwwLjcsMGM5LjItMC4yLDE2LjQtNy44LDE2LjItMTdDMzkwLjYsOTcuMiwzODMsODkuOSwzNzMuOCw5MC4xIi8+Cgk8cGF0aCBpZD0iUGF0aF8xMDUiIGNsYXNzPSJzdDAiIGQ9Ik0yNzYuNyw2OC43VjI1aDMzLjl2MTMwLjFjLTEwLjMsMC0yMSwwLjItMzEuNi0wLjJjLTEuMywwLTIuNC0zLjQtMy40LTQuOQoJCWMtNS4yLDIuOC0xMC43LDUuMS0xNi40LDYuOWMtMTQuMiwzLjQtMjkuMS0xLjgtMzguMy0xMy4yYy0xNi43LTE5LjEtMTYtNTMuMSwxLjQtNzEuN2MxMy0xNC40LDM0LjgtMTYuOCw1MC42LTUuNAoJCUMyNzMuNiw2Ny4xLDI3NC41LDY3LjUsMjc2LjcsNjguNyBNMjQzLjYsMTA4Yy0wLjIsMTAuNiw2LjIsMTcuOCwxNi4xLDE4YzguOSwwLjQsMTYuNC02LjUsMTYuOC0xNS40YzAtMC40LDAtMC44LDAtMS4xCgkJYzEtOS4yLTUuNi0xNy41LTE0LjgtMTguNWMtMC42LTAuMS0xLjEtMC4xLTEuNy0wLjFjLTksMC0xNi40LDcuMy0xNi40LDE2LjRDMjQzLjYsMTA3LjUsMjQzLjYsMTA3LjgsMjQzLjYsMTA4Ii8+Cgk8cGF0aCBpZD0iUGF0aF8xMDYiIGNsYXNzPSJzdDAiIGQ9Ik05NC40LDE1NC44SDYwYzAtMTMuOSwwLTI3LjYsMC00MS4yYzAtMi45LDAuMS01LjksMC04LjhjLTAuMy04LjEtNC4zLTEyLjMtMTEuNC0xMi40CgkJYy02LjctMC4zLTEyLjQsNC45LTEyLjcsMTEuNmMwLDAuMywwLDAuNiwwLDAuOWMtMC40LDEyLjUtMC4xLDI1LjEtMC4yLDM3LjZjMCwzLjksMCw3LjgsMCwxMi4ySDEuOVYyNC45aDMzLjZ2NDIuMgoJCWM4LjQtMi42LDE2LjMtNi43LDI0LjUtNy4zYzE2LjYtMS4yLDI4LDguMSwzMi4xLDI0LjJjMS4zLDQuNywyLjEsOS42LDIuMywxNC41Qzk0LjYsMTE3LjEsOTQuNCwxMzUuNyw5NC40LDE1NC44Ii8+Cgk8cGF0aCBpZD0iUGF0aF8xMDciIGNsYXNzPSJzdDAiIGQ9Ik02MDMuNCwxNTQuN2MwLTE0LDAtMjcuNywwLTQxLjNjMC0zLjQsMC02LjktMC4yLTEwLjNjMC4xLTUuOC00LjQtMTAuNi0xMC4yLTEwLjcKCQljLTAuMiwwLTAuNCwwLTAuNiwwYy02LjItMC44LTExLjksMy42LTEyLjgsOS44YzAsMC4yLDAsMC40LTAuMSwwLjZjLTAuOCw4LjUtMC42LDE3LjItMC43LDI1LjdjLTAuMSw4LjYsMCwxNy4xLDAsMjYuMmgtMzMuOAoJCVYyNC44aDMzLjZ2NDIuNWM4LjItMi44LDE1LjUtNi44LDIzLTcuNGMxOC4xLTEuNywzMS45LDguMiwzMy45LDI2LjJjMi41LDIyLjUsMi4xLDQ1LjQsMyw2OC43TDYwMy40LDE1NC43eiIvPgoJPHBhdGggaWQ9IlBhdGhfMTA4IiBjbGFzcz0ic3QwIiBkPSJNMTM5LjcsMTIwLjRjNywxMC45LDE4LjUsMTEuOSwzNC43LDRsMjEuNiwxOC4xYy0xLjMsMS41LTIuNiwzLTQuMSw0LjMKCQljLTIzLjQsMTktNjQuNiwxMy4xLTgwLjQtMTEuNWMtMTQuNi0yMy43LTcuMy01NC43LDE2LjQtNjkuM2MwLjMtMC4yLDAuNi0wLjMsMC44LTAuNWMyNS4yLTE0LDU3LjQtNC4zLDY5LDIxLjIKCQljMi40LDUuNCwzLjksMTEuMSw0LjYsMTYuOWMxLjcsMTQuOS0wLjIsMTYuOC0xNSwxNi44SDEzOS43IE0xNjguOCw5Ny44Yy0yLjgtOC40LTcuMi0xMS40LTE1LjctMTAuN2MtNi42LTAuMS0xMi40LDQuMy0xNC4xLDEwLjcKCQlIMTY4Ljh6Ii8+Cgk8cGF0aCBpZD0iUGF0aF8xMDkiIGNsYXNzPSJzdDAiIGQ9Ik01MDYsMTI0LjNsMjEuOCwxOC41Yy0xMi4zLDEyLjctMjcuMywxNS45LTQzLjQsMTQuOWMtMjEtMS4zLTM3LjYtMTAuMi00NS4zLTMwLjcKCQljLTcuOS0xOC43LTMuMy00MC40LDExLjYtNTQuMmMxNS42LTE0LjQsMzguNS0xNy43LDU3LjUtOC4zYzE4LjEsOS43LDI4LjIsMjkuNiwyNS41LDQ5LjljLTAuNiw0LjctMi43LDYuMS03LjIsNi4xCgkJYy0xNS41LTAuMi0zMC45LTAuMS00Ni40LDBjLTIuNiwwLTUuMiwwLjItOC4yLDAuNEM0NzcuOCwxMzEuMyw0OTAuMiwxMzIuNCw1MDYsMTI0LjMgTTUwMC43LDk3LjZjLTEuMS02LjYtNy4xLTExLjItMTMuOC0xMC42CgkJYy03LjItMS4xLTE0LDMuNi0xNS42LDEwLjZINTAwLjd6Ii8+Cgk8ZyBpZD0iR3JvdXBfNDUiIHRyYW5zZm9ybT0idHJhbnNsYXRlKDE1Ni44MjMgMCkiPgoJCTxwYXRoIGlkPSJTdWJ0cmFjdGlvbl8xNSIgY2xhc3M9InN0MSIgZD0iTTU5Ni42LDE1MS40Yy0wLjcsMC0xLjQtMC4xLTIuMS0wLjJjLTQuOS0yLTkuNC00LjktMTMuMy04LjdjLTMuNC0zLjEtNi4zLTYuNy04LjUtMTAuOAoJCQljLTIuNi00LjctNC4yLTEwLTQuNi0xNS40Yy0wLjYtNi44LDAuMi0xMy43LDIuMi0yMC4zYzEuMy00LjMsMy41LTguMiw2LjUtMTEuNmMxLjItMS42LDMtMi43LDQuOS0zLjJoMC4xYzIuNiwwLjEsMy43LDIsNS4yLDQuNgoJCQljMi4zLDQuNSw1LjksOC4yLDEwLjIsMTAuN2M2LjMsMy44LDIwLjcsNC43LDIwLjgsNC43YzAuNCw0LjEsMS42LDgsMy42LDExLjZjMi41LDMuMiw1LjQsNS45LDguNyw4LjJ2MAoJCQljLTAuNywxLjktMS42LDMuNy0yLjcsNS40Yy0zLjYsNS40LTcuOCwxMC4yLTEyLjcsMTQuNEM2MDcuMSwxNDgsNjAxLjIsMTUxLjQsNTk2LjYsMTUxLjR6IE01ODMsOTkuMWMtMi40LDAuMS00LjIsMi4xLTQuMSw0LjQKCQkJYzAsMCwwLDAuMSwwLDAuMWMtMC4xLDIuMiwxLjYsNC4yLDMuOCw0LjNjMC4yLDAsMC40LDAsMC42LDBjMS4yLDAuMSwyLjMtMC40LDMuMi0xLjJjMC44LTAuOSwxLjItMi4xLDEuMS0zLjMKCQkJYzAuMS0yLjMtMS43LTQuMy00LTQuNEM1ODMuNCw5OS4xLDU4My4yLDk5LjEsNTgzLDk5LjFMNTgzLDk5LjF6Ii8+CgkJCgkJCTxsaW5lYXJHcmFkaWVudCBpZD0iUGF0aF80XzAwMDAwMTUyMjIyOTQwODkyOTcwMDM3ODUwMDAwMDA2MjE4MzU3ODI3NTc5MTM0MDgxXyIgZ3JhZGllbnRVbml0cz0idXNlclNwYWNlT25Vc2UiIHgxPSI0OTEuODcyNSIgeTE9IjEyMy43MTYiIHgyPSI2MjkuMjY0IiB5Mj0iNDkuMDM4OCI+CgkJCTxzdG9wICBvZmZzZXQ9IjAuMTA1NCIgc3R5bGU9InN0b3AtY29sb3I6IzhENkU0RiIvPgoJCQk8c3RvcCAgb2Zmc2V0PSIwLjk5OTYiIHN0eWxlPSJzdG9wLWNvbG9yOiM3MDQ5MjQiLz4KCQk8L2xpbmVhckdyYWRpZW50PgoJCTxwYXRoIGlkPSJQYXRoXzQiIHN0eWxlPSJmaWxsOnVybCgjUGF0aF80XzAwMDAwMTUyMjIyOTQwODkyOTcwMDM3ODUwMDAwMDA2MjE4MzU3ODI3NTc5MTM0MDgxXyk7IiBkPSJNNjQzLDI5LjcKCQkJYy0xMS0yLjItMjAuMi0yLjYtMzAsMi42YzIwLDcuNCwzNC4zLDI1LjEsMzcuMyw0Ni4yYy0xNC4xLTE0LjQtMjkuOC0yMC4yLTQ4LTE2LjFjLTEyLjcsMi44LTIzLjcsMTAuNC0zMC45LDIxLjIKCQkJYy0xNSwyMi43LTguNyw1My4zLDE0LDY4LjNjMSwwLjcsMiwxLjMsMy4xLDEuOWMtNDMuNywxNS45LTk3LjMtMTkuOS05NC40LTczYzQuNCwyLjYsNy4xLDcuNCwxMi45LDguNgoJCQljLTguNS0yNS0zLjYtNDYuMiwxNi42LTYzLjNjLTEuNiw5LjEtMC4zLDE4LjQsMy43LDI2LjdjMy42LTI1LjUsMTUuNy00Mi43LDQwLTUyYy0zLDkuMi01LjgsMTcuNS0yLjcsMjcuMgoJCQljMTQtMTMuMywzMy45LTE4LjQsNTIuNi0xMy43QzYyNy4xLDE2LjcsNjM2LjIsMjIuMSw2NDMsMjkuNyIvPgoJCTxwYXRoIGlkPSJQYXRoXzUiIGNsYXNzPSJzdDMiIGQ9Ik02MzQuMiw5NC43YzQsMC4xLDUuOCwxLjYsNS40LDUuMWMtMC44LDUuMi0yLjMsMTAuMi00LjYsMTQuOWMtMC45LDItMi4zLDEuOC0zLjcsMC42CgkJCWMtMS43LTEuMy0zLjItMi44LTQuNi00LjRjLTIuNC0zLjUtNS42LTcuOC0zLjMtMTEuN0M2MjUuNSw5NS42LDYzMC44LDk2LDYzNC4yLDk0LjciLz4KCQk8cGF0aCBpZD0iUGF0aF8yODEiIGNsYXNzPSJzdDQiIGQ9Ik01ODMsOTkuMmMyLjMtMC4yLDQuMywxLjUsNC41LDMuOGMwLDAuMiwwLDAuNCwwLDAuNmMwLjMsMi4yLTEuMyw0LjEtMy40LDQuNAoJCQljLTAuMywwLTAuNSwwLTAuOCwwYy0yLjIsMC4yLTQuMi0xLjQtNC41LTMuN2MwLTAuMiwwLTAuNCwwLTAuNkM1NzguNywxMDEuMyw1ODAuNSw5OS4zLDU4Myw5OS4yQzU4Mi45LDk5LjIsNTgyLjksOTkuMiw1ODMsOTkuMgoJCQkiLz4KCTwvZz4KPC9nPgo8L3N2Zz4K;",
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
		} else if link.Type == EdgeTypeMCLAG {
			// If it's an MCLAG link without a specific type, it's a server link
			linkTypesMap[LegendKeyMCLAGServer] = true
		} else if link.Type == EdgeTypeBundled {
			linkTypesMap[LegendKeyBundled] = true
		} else if _, ok := link.Properties[PropBundled]; ok {
			linkTypesMap[LegendKeyBundled] = true
		} else if link.Type == EdgeTypeESLAG {
			linkTypesMap[LegendKeyESLAGServer] = true
		} else if _, ok := link.Properties[PropESLAGServer]; ok {
			linkTypesMap[LegendKeyESLAGServer] = true
		} else if link.Type == EdgeTypeGateway {
			linkTypesMap[LegendKeyGateway] = true
		} else if _, ok := link.Properties[PropGateway]; ok {
			linkTypesMap[LegendKeyGateway] = true
		} else {
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
						linkTypesMap[LegendKeyFabric] = true
					}
				} else if (sourceType == NodeTypeSwitch && targetType == NodeTypeServer) ||
					(sourceType == NodeTypeServer && targetType == NodeTypeSwitch) {
					linkTypesMap[LegendKeyUnbundled] = true
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
		{LegendKeyMCLAGPeer, style.MCLAGPeerStyle, "MCLAG Peer Links"},
		{LegendKeyMCLAGSession, style.MCLAGSessionStyle, "MCLAG Session Links"},
		{LegendKeyMCLAGServer, style.MCLAGServerStyle, "MCLAG Server Links"},
		{LegendKeyBundled, style.BundledServerStyle, "Bundled Server Links"},
		{LegendKeyUnbundled, style.UnbundledStyle, "Unbundled Server Links"},
		{LegendKeyESLAGServer, style.ESLAGServerStyle, "ESLAG Server Links"},
		{LegendKeyGateway, style.GatewayLinkStyle, "Gateway Links"},
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

	// Add the label cells to the model
	model.Root.MxCell = append(model.Root.MxCell, srcLabelCell, tgtLabelCell)
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
