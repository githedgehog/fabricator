// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"fmt"
	"strings"
)

type StyleType string

const (
	StyleTypeDefault  StyleType = "default"
	StyleTypeCisco    StyleType = "cisco"
	StyleTypeHedgehog StyleType = "hedgehog"
)

var StyleTypes = []StyleType{
	StyleTypeDefault,
	StyleTypeCisco,
	StyleTypeHedgehog,
}

type Style struct {
	SpineNodeStyle     string
	LeafNodeStyle      string
	ServerNodeStyle    string
	GatewayNodeStyle   string
	FabricLinkStyle    string
	MCLAGPeerStyle     string
	MCLAGSessionStyle  string
	MCLAGServerStyle   string
	BundledServerStyle string
	UnbundledStyle     string
	ESLAGServerStyle   string
	GatewayLinkStyle   string
	BackgroundColor    string
}

func GetStyle(styleType StyleType) Style {
	switch styleType {
	case StyleTypeCisco:
		return getCiscoStyle()
	case StyleTypeHedgehog:
		return getHedgehogStyle()
	case StyleTypeDefault:
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
		GatewayNodeStyle:   "shape=rectangle;rounded=1;whiteSpace=wrap;html=1;fontSize=11;fillColor=#fff2cc;strokeColor=#d6b656;",
		FabricLinkStyle:    "endArrow=none;html=1;strokeWidth=3;strokeColor=#b85450;",
		MCLAGPeerStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#2f5597;dashed=1;",
		MCLAGSessionStyle:  "endArrow=none;html=1;strokeWidth=2;strokeColor=#4472c4;dashed=1;",
		MCLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#9cc1f7;dashed=1;",
		BundledServerStyle: "endArrow=none;html=1;strokeWidth=2;strokeColor=#82b366;",
		UnbundledStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#666666;",
		ESLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#d79b00;dashed=1;",
		GatewayLinkStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#d6b656;",
		BackgroundColor:    "",
	}
}

func getCiscoStyle() Style {
	return Style{
		SpineNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=nexus_9300;html=1;" +
			"fillColor=#ffffff;strokeColor=#00589C;strokeWidth=2;" +
			"fontColor=#000000;fontSize=11;" +
			"align=center;verticalLabelPosition=middle;verticalAlign=middle;",
		LeafNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=nexus_9300;html=1;" +
			"fillColor=#ffffff;strokeColor=#00589C;strokeWidth=2;" +
			"fontColor=#000000;fontSize=11;" +
			"align=center;verticalLabelPosition=middle;verticalAlign=middle;",
		ServerNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=ucs_c_series_server;html=1;" +
			"fillColor=#ffffff;strokeColor=#999999;strokeWidth=2;" +
			"fontColor=#000000;fontSize=11;" +
			"align=right;verticalAlign=bottom;spacingRight=8;spacingBottom=8;",
		GatewayNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=asr_1000;html=1;" +
			"fillColor=#ffffff;strokeColor=#005073;strokeWidth=2;" +
			"fontColor=#000000;fontSize=11;" +
			"align=center;verticalLabelPosition=middle;verticalAlign=middle;" +
			"perimeter=hexagonPerimeter;",
		FabricLinkStyle:    "endArrow=none;html=1;strokeWidth=3;strokeColor=#4F95D0;",
		MCLAGPeerStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#2f5597;dashed=1;",
		MCLAGSessionStyle:  "endArrow=none;html=1;strokeWidth=2;strokeColor=#4472c4;dashed=1;",
		MCLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#9cc1f7;dashed=1;",
		BundledServerStyle: "endArrow=none;html=1;strokeWidth=2;strokeColor=#82b366;",
		UnbundledStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#666666;",
		ESLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#d79b00;dashed=1;",
		GatewayLinkStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#005073;",
		BackgroundColor:    "#ffffff",
	}
}

func getHedgehogStyle() Style {
	return Style{
		SpineNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=nexus_9300;html=1;" +
			"fillColor=#FFFFFF;strokeColor=#D7B98E;strokeWidth=2;" +
			"fontColor=#000000;fontSize=11;" +
			"align=center;verticalLabelPosition=middle;verticalAlign=middle;",
		LeafNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=nexus_9300;html=1;" +
			"fillColor=#FFFFFF;strokeColor=#D7B98E;strokeWidth=2;" +
			"fontColor=#000000;fontSize=11;" +
			"align=center;verticalLabelPosition=middle;verticalAlign=middle;",
		ServerNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=ucs_c_series_server;html=1;" +
			"fillColor=#FFFFFF;strokeColor=#999999;strokeWidth=2;" +
			"fontColor=#000000;fontSize=11;" +
			"align=right;verticalAlign=bottom;spacingRight=8;spacingBottom=8;",
		GatewayNodeStyle: "shape=mxgraph.cisco19.rect;prIcon=asr_1000;html=1;" +
			"fillColor=#FAFAFA;strokeColor=#D7B98E;" +
			"fontColor=#000000;fontSize=11;" +
			"align=center;verticalLabelPosition=middle;verticalAlign=middle;" +
			"perimeter=hexagonPerimeter;",
		FabricLinkStyle:    "endArrow=none;html=1;strokeWidth=3;strokeColor=#8D6E4F;",
		MCLAGPeerStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#8D6E63;dashed=1;",
		MCLAGSessionStyle:  "endArrow=none;html=1;strokeWidth=2;strokeColor=#A1887F;dashed=1;",
		MCLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#BCAAA4;dashed=1;",
		BundledServerStyle: "endArrow=none;html=1;strokeWidth=2;strokeColor=#82b366;",
		UnbundledStyle:     "endArrow=none;html=1;strokeWidth=2;strokeColor=#666666;",
		ESLAGServerStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#d79b00;dashed=1;",
		GatewayLinkStyle:   "endArrow=none;html=1;strokeWidth=2;strokeColor=#D7B98E;dashed=1;",
		BackgroundColor:    "#FFFFFF",
	}
}

func ExtractStyleParameters(style string) string {
	return style + "fontSize=10;spacing=5;"
}

func GetNodeStyle(node Node, style Style) string {
	return GetNodeStyleFromTheme(node, style)
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
	case NodeTypeGateway:
		return style.GatewayNodeStyle
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
	case EdgeTypeGateway:
		return ExtractStyleParameters(style.GatewayLinkStyle)
	default:
		return baseStyle + "strokeColor=#000000;strokeWidth=2;"
	}
}

func GetNodeDimensions(node Node) (int, int) {
	switch node.Type {
	case NodeTypeSwitch:
		return 100, 90
	case NodeTypeServer:
		return 100, 60
	case NodeTypeGateway:
		return 100, 90
	default:
		return 100, 100
	}
}

func FormatNodeValue(node Node, style Style) string {
	if node.Type == NodeTypeGateway {
		return fmt.Sprintf("<b>%s</b>", node.Label)
	}

	if strings.Contains(style.SpineNodeStyle, "mxgraph.cisco19") && node.Type == NodeTypeSwitch {
		if strings.Contains(node.Label, "\n") {
			parts := strings.SplitN(node.Label, "\n", 2)
			nodeName := parts[0]
			nodeRole := parts[1]

			return fmt.Sprintf(
				"<font style=\"color: rgb(0, 0, 0);\"><b>%s</b></font>"+
					"<br><br><br><br><br>"+
					"<font style=\"color: rgb(0, 0, 0);\">%s</font>",
				nodeName, nodeRole,
			)
		} else if role, ok := node.Properties[PropRole]; ok && role != "" {
			exactMatch := node.Label == role
			endsWithRoleWord := strings.HasSuffix(node.Label, " "+role)

			if !exactMatch && !endsWithRoleWord {
				return fmt.Sprintf(
					"<font style=\"color: rgb(0, 0, 0);\"><b>%s</b></font>"+
						"<br><br><br><br><br>"+
						"<font style=\"color: rgb(0, 0, 0);\">%s</font>",
					node.Label, role,
				)
			}
		}

		return fmt.Sprintf("<font style=\"color: rgb(0, 0, 0);\"><b>%s</b></font>", node.Label)
	}

	if strings.Contains(node.Label, "\n") {
		parts := strings.SplitN(node.Label, "\n", 2)

		return fmt.Sprintf("<b>%s</b>\n%s", parts[0], parts[1])
	}

	return fmt.Sprintf("<b>%s</b>", node.Label)
}

func IsIconBasedStyle(style Style) bool {
	return strings.Contains(style.GatewayNodeStyle, "mxgraph.cisco19") ||
		strings.Contains(style.GatewayNodeStyle, "shape=mxgraph")
}

func GetGatewayLabelStyle() string {
	return "rounded=0;whiteSpace=wrap;html=1;strokeColor=none;fontSize=9;fontStyle=1;"
}
