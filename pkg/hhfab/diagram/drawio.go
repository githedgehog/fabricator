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
	model.Root.MxCell = append(model.Root.MxCell, createHedgehogLogo()...)

	gatewayY := 50
	spineY := gatewayY + 250
	leafY := spineY + 250
	serverY := leafY + 250

	layers := sortNodes(topo.Nodes, topo.Links)
	cellMap := make(map[string]*MxCell)

	canvasWidth := 600

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

	linkGroups := groupLinks(topo.Links)
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
		{260, style.GatewayLinkStyle, "Gateway Links"},
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
