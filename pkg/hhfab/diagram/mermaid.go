// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func GenerateMermaid(workDir string, topo Topology) error {
	outputFile := filepath.Join(workDir, MermaidFilename)
	mermaid := generateMermaid(topo)
	if err := os.WriteFile(outputFile, []byte(mermaid), 0o600); err != nil {
		return fmt.Errorf("writing Mermaid file: %w", err)
	}

	return nil
}

func generateMermaid(topo Topology) string {
	var b strings.Builder

	b.WriteString("graph TD\n\n")

	b.WriteString("classDef gateway fill:#FFF2CC,stroke:#999,stroke-width:1px,color:#000\n")
	b.WriteString("classDef spine   fill:#F8CECC,stroke:#B85450,stroke-width:1px,color:#000\n")
	b.WriteString("classDef leaf    fill:#DAE8FC,stroke:#6C8EBF,stroke-width:1px,color:#000\n")
	b.WriteString("classDef server  fill:#D5E8D4,stroke:#82B366,stroke-width:1px,color:#000\n")
	b.WriteString("classDef mclag   fill:#F0F8FF,stroke:#6C8EBF,stroke-width:1px,color:#000\n\n")

	layers := sortNodes(topo.Nodes, topo.Links)

	mclagPairs := findMCLAGLeafPairs(topo)

	processedMCLAG := make(map[string]bool)

	if len(layers.Gateway) > 0 {
		b.WriteString("subgraph Gateways\n")
		b.WriteString("\tdirection TB\n")
		for _, node := range layers.Gateway {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	if len(layers.Spine) > 0 {
		b.WriteString("subgraph Spines\n")
		b.WriteString("\tdirection TB\n")
		for _, node := range layers.Spine {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	if len(layers.Leaf) > 0 {
		b.WriteString("subgraph Leaves [Leaves]\n")
		b.WriteString("\tdirection LR\n")

		mclagSubgraphs := make(map[string][]Node)
		singleLeaves := []Node{}

		for _, node := range layers.Leaf {
			if pair, hasPair := mclagPairs[node.ID]; hasPair {
				// Only process each MCLAG pair once
				if !processedMCLAG[node.ID+pair] && !processedMCLAG[pair+node.ID] {
					groupID := "MCLAG"

					if mclagSubgraphs[groupID] == nil {
						mclagSubgraphs[groupID] = []Node{}
					}

					mclagSubgraphs[groupID] = append(mclagSubgraphs[groupID], node)

					for _, pairNode := range layers.Leaf {
						if pairNode.ID == pair {
							mclagSubgraphs[groupID] = append(mclagSubgraphs[groupID], pairNode)

							break
						}
					}

					processedMCLAG[node.ID+pair] = true
					processedMCLAG[pair+node.ID] = true
				}
			} else {
				singleLeaves = append(singleLeaves, node)
			}
		}

		for groupID, nodes := range mclagSubgraphs {
			b.WriteString(fmt.Sprintf("\tsubgraph %s [MCLAG]\n", groupID))
			b.WriteString("\t\tdirection LR\n")

			for _, node := range nodes {
				nodeID := cleanID(node.ID)
				label := formatLabel(node.Label)
				b.WriteString(fmt.Sprintf("\t\t%s[\"%s\"]\n", nodeID, label))
			}

			b.WriteString("\tend\n\n")
		}

		for _, node := range singleLeaves {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}

		b.WriteString("end\n\n")
	}

	if len(layers.Server) > 0 {
		b.WriteString("subgraph Servers\n")
		b.WriteString("\tdirection TB\n")

		for _, node := range layers.Server {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	connectionMap := make(map[string]map[string][]string)

	for _, link := range topo.Links {
		if link.Type == EdgeTypeMCLAG {
			sourceIsLeaf := false
			targetIsLeaf := false

			for _, node := range topo.Nodes {
				if node.ID == link.Source && node.Type == NodeTypeSwitch {
					if role, ok := node.Properties["role"]; ok && role != SwitchRoleSpine {
						sourceIsLeaf = true
					}
				}
				if node.ID == link.Target && node.Type == NodeTypeSwitch {
					if role, ok := node.Properties["role"]; ok && role != SwitchRoleSpine {
						targetIsLeaf = true
					}
				}
			}

			if sourceIsLeaf && targetIsLeaf {
				continue
			}
		}

		sourceID := cleanID(link.Source)
		targetID := cleanID(link.Target)
		key := sourceID + "->" + targetID

		sourcePort := extractPort(link.Properties["sourcePort"])
		targetPort := extractPort(link.Properties["targetPort"])
		portLabel := ""

		if sourcePort != "" && targetPort != "" { //nolint:gocritic
			portLabel = targetPort + "↔" + sourcePort
		} else if sourcePort != "" {
			portLabel = "↔" + sourcePort
		} else if targetPort != "" {
			portLabel = targetPort + "↔"
		}

		if portLabel != "" {
			if connectionMap[key] == nil {
				connectionMap[key] = make(map[string][]string)
			}

			var connType string
			switch link.Type {
			case EdgeTypeFabric:
				connType = EdgeTypeFabric
			case EdgeTypeMCLAG:
				connType = EdgeTypeMCLAG
			case EdgeTypeBundled:
				connType = EdgeTypeBundled
			case EdgeTypeUnbundled:
				connType = EdgeTypeUnbundled
			case EdgeTypeESLAG:
				connType = EdgeTypeESLAG
			case EdgeTypeGateway:
				connType = EdgeTypeGateway
			default:
				connType = "other"
			}

			connectionMap[key][connType] = append(connectionMap[key][connType], portLabel)
		}
	}

	// Track link indices for each type of connection
	gatewayLinks := []int{}
	spineLeafLinks := []int{}
	mclagLinks := []int{}
	bundledLinks := []int{}
	eslagLinks := []int{}
	unbundledLinks := []int{}

	linkIndex := 0

	b.WriteString("%% Gateways -> Spines\n")
	for key, connTypes := range connectionMap {
		parts := strings.Split(key, "->")
		sourceID := parts[0]
		targetID := parts[1]

		isGatewaySpine := false
		for _, node := range layers.Gateway {
			if cleanID(node.ID) == sourceID {
				for _, spine := range layers.Spine {
					if cleanID(spine.ID) == targetID {
						isGatewaySpine = true

						break
					}
				}

				break
			}
		}

		if isGatewaySpine {
			for _, ports := range connTypes {
				portLabel := strings.Join(ports, "<br>")
				connection := fmt.Sprintf("%s ---|%q| %s", sourceID, portLabel, targetID)
				b.WriteString(connection + "\n")
				gatewayLinks = append(gatewayLinks, linkIndex)
				linkIndex++
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("%% Spine_01 -> Leaves\n")
	spineLeafMap := make(map[string][]string)

	for key, connTypes := range connectionMap {
		parts := strings.Split(key, "->")
		sourceID := parts[0]
		targetID := parts[1]

		isSpineLeaf := false
		var spineID string
		for _, node := range layers.Spine {
			if cleanID(node.ID) == sourceID {
				spineID = cleanID(node.ID)
				for _, leaf := range layers.Leaf {
					if cleanID(leaf.ID) == targetID {
						isSpineLeaf = true

						break
					}
				}

				break
			}
		}

		if isSpineLeaf {
			for _, ports := range connTypes {
				portLabel := strings.Join(ports, "<br>")
				connection := fmt.Sprintf("%s ---|%q| %s", sourceID, portLabel, targetID)

				if spineLeafMap[spineID] == nil {
					spineLeafMap[spineID] = []string{}
				}
				spineLeafMap[spineID] = append(spineLeafMap[spineID], connection)
			}
		}
	}

	for spineID, connections := range spineLeafMap {
		if spineID == "Spine_02" {
			b.WriteString("\n%% Spine_02 -> Leaves\n")
		}

		for _, conn := range connections {
			b.WriteString(conn + "\n")
			spineLeafLinks = append(spineLeafLinks, linkIndex)
			linkIndex++
		}
	}
	b.WriteString("\n")

	b.WriteString("%% Leaves -> Servers\n")
	leafServerMap := make(map[string][]string)

	leafServerTypes := make(map[string]string)

	for key, connTypes := range connectionMap {
		parts := strings.Split(key, "->")
		sourceID := parts[0]
		targetID := parts[1]

		isLeafServer := false
		var leafID string
		var connType string

		for _, node := range layers.Leaf {
			if cleanID(node.ID) == sourceID {
				leafID = cleanID(node.ID)
				for _, server := range layers.Server {
					if cleanID(server.ID) == targetID {
						isLeafServer = true

						for cType := range connTypes {
							connType = cType

							break
						}

						break
					}
				}

				break
			}
		}

		if !isLeafServer {
			for _, node := range layers.Server {
				if cleanID(node.ID) == sourceID {
					for _, leaf := range layers.Leaf {
						if cleanID(leaf.ID) == targetID {
							leafID = targetID
							targetID = sourceID
							sourceID = leafID
							isLeafServer = true

							for cType := range connTypes {
								connType = cType

								break
							}

							break
						}
					}

					if isLeafServer {
						break
					}
				}
			}
		}

		if isLeafServer {
			for _, ports := range connTypes {
				portLabel := strings.Join(ports, "<br>")
				connection := fmt.Sprintf("%s ---|%q| %s", sourceID, portLabel, targetID)

				if leafServerMap[leafID] == nil {
					leafServerMap[leafID] = []string{}
				}

				leafServerMap[leafID] = append(leafServerMap[leafID], connection)

				connectionKey := fmt.Sprintf("%s-%s", sourceID, targetID)
				leafServerTypes[connectionKey] = connType
			}
		}
	}

	// Sort leaf IDs for consistent rendering
	leafIDs := make([]string, 0, len(leafServerMap))
	for leafID := range leafServerMap {
		leafIDs = append(leafIDs, leafID)
	}
	sort.Strings(leafIDs)

	for _, leafID := range leafIDs {
		connections := leafServerMap[leafID]
		for _, conn := range connections {
			b.WriteString(conn + "\n")

			connParts := strings.Split(conn, " ---|")
			sourceID := connParts[0]
			targetID := strings.Split(strings.Split(connParts[1], "| ")[1], "\n")[0]
			connectionKey := fmt.Sprintf("%s-%s", sourceID, targetID)

			connType := leafServerTypes[connectionKey]
			switch connType {
			case EdgeTypeMCLAG:
				mclagLinks = append(mclagLinks, linkIndex)
			case EdgeTypeBundled:
				bundledLinks = append(bundledLinks, linkIndex)
			case EdgeTypeESLAG:
				eslagLinks = append(eslagLinks, linkIndex)
			case EdgeTypeUnbundled:
				unbundledLinks = append(unbundledLinks, linkIndex)
			}

			linkIndex++
		}
		b.WriteString("\n")
	}

	if len(layers.Gateway) > 0 {
		gatewayIDs := []string{}
		for _, node := range layers.Gateway {
			gatewayIDs = append(gatewayIDs, cleanID(node.ID))
		}
		b.WriteString(fmt.Sprintf("class %s gateway\n", strings.Join(gatewayIDs, ",")))
	}

	if len(layers.Spine) > 0 {
		spineIDs := []string{}
		for _, node := range layers.Spine {
			spineIDs = append(spineIDs, cleanID(node.ID))
		}
		b.WriteString(fmt.Sprintf("class %s spine\n", strings.Join(spineIDs, ",")))
	}

	if len(layers.Leaf) > 0 {
		leafIDs := []string{}
		for _, node := range layers.Leaf {
			leafIDs = append(leafIDs, cleanID(node.ID))
		}
		b.WriteString(fmt.Sprintf("class %s leaf\n", strings.Join(leafIDs, ",")))
	}

	if len(layers.Server) > 0 {
		serverIDs := []string{}
		for _, node := range layers.Server {
			serverIDs = append(serverIDs, cleanID(node.ID))
		}
		b.WriteString(fmt.Sprintf("class %s server\n", strings.Join(serverIDs, ",")))
	}

	// Add class for MCLAG subgraph
	b.WriteString("class MCLAG mclag\n")

	b.WriteString("linkStyle default stroke:#666,stroke-width:2px\n")

	formatIndices := func(indices []int) string {
		if len(indices) == 0 {
			return ""
		}

		strIndices := make([]string, len(indices))
		for i, idx := range indices {
			strIndices[i] = strconv.Itoa(idx)
		}

		return strings.Join(strIndices, ",")
	}

	if len(gatewayLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#CC9900,stroke-width:2px\n", formatIndices(gatewayLinks)))
	}

	if len(spineLeafLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#CC3333,stroke-width:4px\n", formatIndices(spineLeafLinks)))
	}

	if len(mclagLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#99CCFF,stroke-width:4px,stroke-dasharray:5 5\n", formatIndices(mclagLinks)))
	}

	if len(bundledLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#66CC66,stroke-width:4px\n", formatIndices(bundledLinks)))
	}

	if len(eslagLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#CC9900,stroke-width:4px,stroke-dasharray:5 5\n", formatIndices(eslagLinks)))
	}

	if len(unbundledLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#999999,stroke-width:2px\n", formatIndices(unbundledLinks)))
	}

	// Add styles for transparent subgraphs
	b.WriteString("style Gateways fill:none,stroke:none\n")
	b.WriteString("style Spines fill:none,stroke:none\n")
	b.WriteString("style Leaves fill:none,stroke:none\n")
	b.WriteString("style Servers fill:none,stroke:none\n")

	return b.String()
}

func findMCLAGLeafPairs(topo Topology) map[string]string {
	leafPairs := make(map[string]string)

	for _, link := range topo.Links {
		if link.Type == EdgeTypeMCLAG {
			sourceIsLeaf := false
			targetIsLeaf := false

			for _, node := range topo.Nodes {
				if node.ID == link.Source && node.Type == NodeTypeSwitch {
					if role, ok := node.Properties["role"]; ok && role != SwitchRoleSpine {
						sourceIsLeaf = true
					}
				}
				if node.ID == link.Target && node.Type == NodeTypeSwitch {
					if role, ok := node.Properties["role"]; ok && role != SwitchRoleSpine {
						targetIsLeaf = true
					}
				}
			}

			if sourceIsLeaf && targetIsLeaf {
				leafPairs[link.Source] = link.Target
				leafPairs[link.Target] = link.Source
			}
		}
	}

	return leafPairs
}

func formatLabel(label string) string {
	return strings.ReplaceAll(label, "\n", "<br>")
}

func cleanID(id string) string {
	result := strings.ReplaceAll(id, "-", "_")

	parts := strings.Split(result, "_")

	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[0:1]) + part[1:]
		}
	}

	return strings.Join(parts, "_")
}
