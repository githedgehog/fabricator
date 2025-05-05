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

func GenerateMermaid(workDir string, topo Topology, outputPath string) error {
	var finalOutputPath string
	if outputPath != "" {
		finalOutputPath = outputPath
	} else {
		finalOutputPath = filepath.Join(workDir, MermaidFilename)
	}

	mermaid := generateMermaid(topo)

	if err := os.MkdirAll(filepath.Dir(finalOutputPath), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	if err := os.WriteFile(finalOutputPath, []byte(mermaid), 0o600); err != nil {
		return fmt.Errorf("writing Mermaid file: %w", err)
	}

	return nil
}

func generateMermaid(topo Topology) string {
	var b strings.Builder

	b.WriteString("graph TD\n\n")

	b.WriteString("%% Style definitions\n")
	b.WriteString("classDef gateway fill:#FFF2CC,stroke:#999,stroke-width:1px,color:#000\n")
	b.WriteString("classDef spine   fill:#F8CECC,stroke:#B85450,stroke-width:1px,color:#000\n")
	b.WriteString("classDef leaf    fill:#DAE8FC,stroke:#6C8EBF,stroke-width:1px,color:#000\n")
	b.WriteString("classDef server  fill:#D5E8D4,stroke:#82B366,stroke-width:1px,color:#000\n")
	b.WriteString("classDef mclag   fill:#F0F8FF,stroke:#6C8EBF,stroke-width:1px,color:#000\n")
	b.WriteString("classDef eslag   fill:#FFF8E8,stroke:#CC9900,stroke-width:1px,color:#000\n")
	b.WriteString("classDef hidden fill:none,stroke:none\n")
	b.WriteString("classDef legendBox fill:white,stroke:#999,stroke-width:1px,color:#000\n\n")

	b.WriteString("%% Network diagram\n")

	layers := sortNodes(topo.Nodes, topo.Links)

	// Sort gateways explicitly by ID
	sort.Slice(layers.Gateway, func(i, j int) bool {
		return layers.Gateway[i].ID < layers.Gateway[j].ID
	})

	// Sort spines explicitly by ID
	sort.Slice(layers.Spine, func(i, j int) bool {
		return layers.Spine[i].ID < layers.Spine[j].ID
	})

	mclagPairs, eslagPairs := findLeafPairs(topo)

	processedMCLAG := make(map[string]bool)
	processedESLAG := make(map[string]bool)

	// Define mclagGroupCount at the function level so it's accessible throughout
	mclagGroupCount := 0

	// Only add gateway subgraph if gateways are present
	if len(layers.Gateway) > 0 {
		b.WriteString("subgraph Gateways[ ]\n")
		b.WriteString("\tdirection LR\n")
		for _, node := range layers.Gateway {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	if len(layers.Spine) > 0 {
		b.WriteString("subgraph Spines[ ]\n")
		b.WriteString("\tdirection LR\n")

		for _, node := range layers.Spine {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\tsubgraph %s_Group [ ]\n", nodeID))
			b.WriteString("\t\tdirection TB\n")
			b.WriteString(fmt.Sprintf("\t\t%s[\"%s\"]\n", nodeID, label))
			b.WriteString("\tend\n")
		}
		b.WriteString("end\n\n")
	}

	if len(layers.Leaf) > 0 {
		b.WriteString("subgraph Leaves[ ]\n")
		b.WriteString("\tdirection LR\n")

		// Group MCLAG leaf pairs into separate subgraphs
		mclagGroups := make(map[string][]Node)
		eslagSubgraphs := make(map[string][]Node)
		singleLeaves := []Node{}

		for _, node := range layers.Leaf {
			if pair, hasPair := mclagPairs[node.ID]; hasPair {
				// Only process each MCLAG pair once
				if !processedMCLAG[node.ID+pair] && !processedMCLAG[pair+node.ID] {
					// Create a unique group ID for each MCLAG pair
					groupID := fmt.Sprintf("MCLAG_%d", mclagGroupCount)
					mclagGroupCount++

					if mclagGroups[groupID] == nil {
						mclagGroups[groupID] = []Node{}
					}

					mclagGroups[groupID] = append(mclagGroups[groupID], node)

					for _, pairNode := range layers.Leaf {
						if pairNode.ID == pair {
							mclagGroups[groupID] = append(mclagGroups[groupID], pairNode)

							break
						}
					}

					processedMCLAG[node.ID+pair] = true
					processedMCLAG[pair+node.ID] = true
				}
			} else if pair, hasPair := eslagPairs[node.ID]; hasPair {
				// Only process each ESLAG pair once
				if !processedESLAG[node.ID+pair] && !processedESLAG[pair+node.ID] {
					groupID := "ESLAG"

					if eslagSubgraphs[groupID] == nil {
						eslagSubgraphs[groupID] = []Node{}
					}

					eslagSubgraphs[groupID] = append(eslagSubgraphs[groupID], node)

					for _, pairNode := range layers.Leaf {
						if pairNode.ID == pair {
							eslagSubgraphs[groupID] = append(eslagSubgraphs[groupID], pairNode)

							break
						}
					}

					processedESLAG[node.ID+pair] = true
					processedESLAG[pair+node.ID] = true
				}
			} else {
				singleLeaves = append(singleLeaves, node)
			}
		}

		// Render separate MCLAG subgraphs for each pair
		for groupID, nodes := range mclagGroups {
			b.WriteString(fmt.Sprintf("\tsubgraph %s [MCLAG]\n", groupID))
			b.WriteString("\t\tdirection LR\n")

			for _, node := range nodes {
				nodeID := cleanID(node.ID)
				label := formatLabel(node.Label)
				b.WriteString(fmt.Sprintf("\t\t%s[\"%s\"]\n", nodeID, label))
			}

			b.WriteString("\tend\n\n")
		}

		for groupID, nodes := range eslagSubgraphs {
			b.WriteString(fmt.Sprintf("\tsubgraph %s [ESLAG]\n", groupID))
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
		b.WriteString("subgraph Servers[ ]\n")
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

		// Fix ifElseChain: rewrite to switch statement
		switch {
		case sourcePort != "" && targetPort != "":
			portLabel = targetPort + "↔" + sourcePort
		case sourcePort != "":
			portLabel = "↔" + sourcePort
		case targetPort != "":
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

	b.WriteString("%% Connections\n\n")

	// Only add gateway-spine section if both exist
	if len(layers.Gateway) > 0 && len(layers.Spine) > 0 {
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
				} else if cleanID(node.ID) == targetID {
					for _, spine := range layers.Spine {
						if cleanID(spine.ID) == sourceID {
							// Swap source and target for consistent display
							sourceID, targetID = targetID, sourceID
							isGatewaySpine = true

							break
						}
					}

					break
				}
			}

			if isGatewaySpine {
				for connType, ports := range connTypes {
					if connType == EdgeTypeGateway {
						// Fix for inverted labels - swap the ports for gateway connections
						invertedPorts := make([]string, len(ports))
						for i, portLabel := range ports {
							parts := strings.Split(portLabel, "↔")
							if len(parts) == 2 {
								invertedPorts[i] = parts[1] + "↔" + parts[0]
							} else {
								invertedPorts[i] = portLabel
							}
						}
						portLabel := strings.Join(invertedPorts, "<br>")
						connection := fmt.Sprintf("%s ---|%q| %s", sourceID, portLabel, targetID)
						b.WriteString(connection + "\n")
						gatewayLinks = append(gatewayLinks, linkIndex)
						linkIndex++
					}
				}
			}
		}
		b.WriteString("\n")
	}

	// Group spine-leaf connections by spine
	spineLeafMapBySpine := make(map[string][]string)

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

				if spineLeafMapBySpine[spineID] == nil {
					spineLeafMapBySpine[spineID] = []string{}
				}
				spineLeafMapBySpine[spineID] = append(spineLeafMapBySpine[spineID], connection)
			}
		}
	}

	// Sort spine IDs to ensure consistent ordering
	spineIDs := make([]string, 0, len(spineLeafMapBySpine))
	for spineID := range spineLeafMapBySpine {
		spineIDs = append(spineIDs, spineID)
	}
	sort.Strings(spineIDs)

	// Output connections grouped by spine with appropriate headers
	for _, spineID := range spineIDs {
		b.WriteString(fmt.Sprintf("%%%% %s -> Leaves\n", spineID))
		for _, conn := range spineLeafMapBySpine[spineID] {
			b.WriteString(conn + "\n")
			spineLeafLinks = append(spineLeafLinks, linkIndex)
			linkIndex++
		}
		b.WriteString("\n")
	}

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

	// Create the legend subgraph
	b.WriteString("subgraph Legend[\"Network Connection Types\"]\n")
	b.WriteString("\tdirection LR\n")
	b.WriteString("\t%% Create invisible nodes for the start and end of each line\n")

	// Only include connection types that are actually present in the diagram
	if len(spineLeafLinks) > 0 {
		b.WriteString("\tL1(( )) --- |\"Fabric Links\"| L2(( ))\n")
	}

	if len(mclagLinks) > 0 {
		b.WriteString("\tL3(( )) --- |\"MCLAG Server Links\"| L4(( ))\n")
	}

	if len(bundledLinks) > 0 {
		b.WriteString("\tL5(( )) --- |\"Bundled Server Links\"| L6(( ))\n")
	}

	if len(unbundledLinks) > 0 {
		b.WriteString("\tL7(( )) --- |\"Unbundled Server Links\"| L8(( ))\n")
	}

	if len(eslagLinks) > 0 {
		b.WriteString("\tL9(( )) --- |\"ESLAG Server Links\"| L10(( ))\n")
	}

	if len(gatewayLinks) > 0 {
		b.WriteString("\tL11(( )) --- |\"Gateway Links\"| L12(( ))\n")
	}

	b.WriteString("\tP1(( )) --- |\"Label Notation: Downstream ↔ Upstream\"| P2(( ))\n")

	b.WriteString("end\n\n")

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

	// Add class for each MCLAG subgraph
	for i := 0; i < mclagGroupCount; i++ {
		groupName := fmt.Sprintf("MCLAG_%d", i)
		b.WriteString(fmt.Sprintf("class %s mclag\n", groupName))
	}

	b.WriteString("class ESLAG eslag\n")

	// Update hidden class to include P1,P2
	hiddenNodes := []string{"L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8", "L9", "L10", "L11", "L12", "P1", "P2"}
	b.WriteString(fmt.Sprintf("class %s hidden\n", strings.Join(hiddenNodes, ",")))

	b.WriteString("class Legend legendBox\n")

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

	// Calculate legend link indices
	legendLinkStart := linkIndex
	legendLinkIndex := 0

	// Style the legend connection types that are present
	if len(spineLeafLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#B85450,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	if len(mclagLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#6C8EBF,stroke-width:2px,stroke-dasharray:5 5\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	if len(bundledLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#82B366,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	if len(unbundledLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#000000,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	if len(eslagLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#CC9900,stroke-width:2px,stroke-dasharray:5 5\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	if len(gatewayLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#CC9900,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	// Style the label notation line - just use a single white stroke
	b.WriteString(fmt.Sprintf("linkStyle %d stroke:#FFFFFF\n", legendLinkStart+legendLinkIndex))

	b.WriteString("\n%% Make subgraph containers invisible\n")
	if len(layers.Gateway) > 0 {
		b.WriteString("style Gateways fill:none,stroke:none\n")
	}
	if len(layers.Spine) > 0 {
		b.WriteString("style Spines fill:none,stroke:none\n")
	}
	b.WriteString("style Leaves fill:none,stroke:none\n")
	b.WriteString("style Servers fill:none,stroke:none\n")

	if len(layers.Spine) > 0 {
		for _, node := range layers.Spine {
			spineID := cleanID(node.ID)
			b.WriteString(fmt.Sprintf("style %s_Group fill:none,stroke:none\n", spineID))
		}
	}

	return b.String()
}

func findLeafPairs(topo Topology) (map[string]string, map[string]string) {
	mclagPairs := make(map[string]string)
	eslagPairs := make(map[string]string)

	// First, find direct MCLAG and ESLAG links between leaves
	for _, link := range topo.Links {
		if link.Type == EdgeTypeMCLAG || link.Type == EdgeTypeESLAG {
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
				if link.Type == EdgeTypeMCLAG {
					mclagPairs[link.Source] = link.Target
					mclagPairs[link.Target] = link.Source
				} else if link.Type == EdgeTypeESLAG {
					eslagPairs[link.Source] = link.Target
					eslagPairs[link.Target] = link.Source
				}
			}
		}
	}

	// Find ESLAG leaf pairs from server connections
	// Group leaf switches that connect to the same servers using ESLAG
	leafsByServer := make(map[string][]string)
	for _, link := range topo.Links {
		if link.Type == EdgeTypeESLAG {
			var server, leaf string
			if strings.HasPrefix(link.Source, "server-") {
				server = link.Source
				leaf = link.Target
			} else if strings.HasPrefix(link.Target, "server-") {
				server = link.Target
				leaf = link.Source
			}

			if server != "" && leaf != "" {
				leafsByServer[server] = append(leafsByServer[server], leaf)
			}
		}
	}

	// Create pairs from leaves connected to the same server with ESLAG connections
	for _, leaves := range leafsByServer {
		if len(leaves) >= 2 {
			// Just pair the first two leaves found for each server
			eslagPairs[leaves[0]] = leaves[1]
			eslagPairs[leaves[1]] = leaves[0]
		}
	}

	return mclagPairs, eslagPairs
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
