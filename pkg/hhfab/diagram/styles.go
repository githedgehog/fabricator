// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"fmt"
	"strings"
)

type StyleType string

const (
	StyleDefault StyleType = "default"
	StyleCisco   StyleType = "cisco"
)

var StyleTypes = []StyleType{
	StyleDefault,
	StyleCisco,
}

type Style struct {
	SpineNodeStyle     string
	LeafNodeStyle      string
	ServerNodeStyle    string
	FabricLinkStyle    string
	MCLAGPeerStyle     string
	MCLAGSessionStyle  string
	MCLAGServerStyle   string
	BundledServerStyle string
	UnbundledStyle     string
	ESLAGServerStyle   string
	BackgroundColor    string
}

func GetStyle(styleType StyleType) Style {
	switch styleType {
	case StyleCisco:
		return getCiscoStyle()
	case StyleDefault:
		fallthrough
	default:
		return getDefaultStyle()
	}
}

func getDefaultStyle() Style {
	return Style{
		SpineNodeStyle:     "shape=rectangle;rounded=1;whiteSpace=wrap;html=1;fontSize=11;fillColor=#f8cecc;strokeColor=#b85450;",
		LeafNodeStyle:      "shape=rectangle;rounded=1;whiteSpace=wrap;html=1;fontSize=11;fillColor=#dae8fc;strokeColor=#6c8ebf;",
		ServerNodeStyle:    "shape=rectangle;rounded=0;whiteSpace=wrap;html=1;fontSize=11;fillColor=#d5e8d4;strokeColor=#82b366;",
		FabricLinkStyle:    "endArrow=none;html=1;strokeWidth=3;strokeColor=#b85450;",
		MCLAGPeerStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#2f5597;dashed=1;",
		MCLAGSessionStyle:  "endArrow=none;html=1;strokeWidth=2;strokeColor=#4472c4;dashed=1;",
		MCLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#9cc1f7;dashed=1;",
		BundledServerStyle: "endArrow=none;html=1;strokeWidth=2;strokeColor=#82b366;",
		UnbundledStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#666666;",
		ESLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#d79b00;dashed=1;",
		BackgroundColor:    "",
	}
}

func getCiscoStyle() Style {
	return Style{
		// Fixed style: square icon shape with text properly fitted inside
		SpineNodeStyle:  "shape=mxgraph.cisco19.rect;prIcon=nexus_9300;html=1;fillColor=#00589C;strokeColor=#ffffff;strokeWidth=2;fontColor=#ffffff;fontSize=11;verticalLabelPosition=middle;verticalAlign=middle;align=center;spacingLeft=5;spacingTop=5;spacingRight=5;spacingBottom=5;",
		LeafNodeStyle:   "shape=mxgraph.cisco19.rect;prIcon=nexus_9300;html=1;fillColor=#00589C;strokeColor=#ffffff;strokeWidth=2;fontColor=#ffffff;fontSize=11;verticalLabelPosition=middle;verticalAlign=middle;align=center;spacingLeft=5;spacingTop=5;spacingRight=5;spacingBottom=5;",
		ServerNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=ucs_c_series_server;html=1;fillColor=#999999;strokeColor=#ffffff;strokeWidth=2;fontColor=#000000;fontSize=11;verticalLabelPosition=bottom;verticalAlign=top;align=center;spacingLeft=5;spacingTop=0;spacingRight=5;spacingBottom=0;",
		// Changed fabric links to blue for Cisco style
		FabricLinkStyle:    "endArrow=none;html=1;strokeWidth=3;strokeColor=#00589C;",
		MCLAGPeerStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#2f5597;dashed=1;",
		MCLAGSessionStyle:  "endArrow=none;html=1;strokeWidth=2;strokeColor=#4472c4;dashed=1;",
		MCLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#9cc1f7;dashed=1;",
		BundledServerStyle: "endArrow=none;html=1;strokeWidth=2;strokeColor=#82b366;",
		UnbundledStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#666666;",
		ESLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#d79b00;dashed=1;",
		BackgroundColor:    "#ffffff",
	}
}

func ExtractStyleParameters(style string) string {
	return style + "fontSize=10;spacing=5;"
}

func GetNodeStyleFromTheme(node Node, style Style) string {
	switch node.Type {
	case NodeTypeSwitch:
		if role, ok := node.Properties["role"]; ok && role == SwitchRoleSpine {
			return style.SpineNodeStyle
		}

		return style.LeafNodeStyle
	case NodeTypeServer:
		return style.ServerNodeStyle
	default:
		return style.LeafNodeStyle
	}
}

func GetLinkStyleFromTheme(link Link, style Style) string {
	baseStyle := "endArrow=none;html=1;"
	switch link.Type {
	case EdgeTypeFabric:
		return ExtractStyleParameters(style.FabricLinkStyle)
	case EdgeTypeMCLAG:
		if mclagType, ok := link.Properties["mclagType"]; ok {
			switch mclagType {
			case "peer":
				return ExtractStyleParameters(style.MCLAGPeerStyle)
			case "session":
				return ExtractStyleParameters(style.MCLAGSessionStyle)
			default:
				return ExtractStyleParameters(style.MCLAGServerStyle)
			}
		} else {
			return ExtractStyleParameters(style.MCLAGServerStyle)
		}
	case EdgeTypeBundled:
		return ExtractStyleParameters(style.BundledServerStyle)
	case EdgeTypeUnbundled:
		return ExtractStyleParameters(style.UnbundledStyle)
	case EdgeTypeESLAG:
		return ExtractStyleParameters(style.ESLAGServerStyle)
	default:
		return baseStyle + "strokeColor=#000000;strokeWidth=2;"
	}
}

// Consolidated getNodeStyle function - modify the existing one in drawio.go
func GetNodeStyle(node Node, style Style) string {
	baseStyle := GetNodeStyleFromTheme(node, style)

	// Apply different node sizes based on style and node type
	if strings.Contains(baseStyle, "fillColor=#00589C") && node.Type == NodeTypeSwitch {
		// Cisco style switches - make square with proper spacing
		baseStyle += "width=120;height=120;"
	} else if strings.Contains(baseStyle, "fillColor=#999999") && node.Type == NodeTypeServer {
		// Cisco style servers
		baseStyle += "width=100;height=60;"
	}

	return baseStyle
}

// Get node label with role information for Cisco style
func GetNodeLabel(node Node, style Style) string {
	var nodeValue string
	if role, ok := node.Properties["role"]; ok && strings.Contains(GetNodeStyle(node, style), "fillColor=#00589C") {
		// Format label with role for Cisco style switches
		nodeValue = fmt.Sprintf("%s\n%s", node.Label, role)
	} else {
		nodeValue = node.Label
	}

	return nodeValue
}
