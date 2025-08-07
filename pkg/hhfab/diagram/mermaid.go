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
	b.WriteString("classDef external fill:#FFCC99,stroke:#D79B00,stroke-width:1px,color:#000\n")
	b.WriteString("classDef hidden fill:none,stroke:none\n")
	b.WriteString("classDef legendBox fill:white,stroke:#999,stroke-width:1px,color:#000\n\n")

	b.WriteString("%% Network diagram\n")

	layers := sortNodes(topo.Nodes, topo.Links)
	leftExternals, rightExternals := splitMermaidExternalNodes(layers.External, topo.Links, layers.Leaf)

	// Sort gateways explicitly by ID
	sort.Slice(layers.Gateway, func(i, j int) bool {
		return layers.Gateway[i].ID < layers.Gateway[j].ID
	})

	// Sort spines explicitly by ID
	sort.Slice(layers.Spine, func(i, j int) bool {
		return layers.Spine[i].ID < layers.Spine[j].ID
	})

	// Declare variables for redundancy handling at function level
	redundancySubgraphs := make(map[string][]Node)
	redundancyTypes := make(map[string]string)

	// Only add gateway subgraph if gateways are present
	if len(layers.Gateway) > 0 {
		b.WriteString("subgraph Gateways[\" \"]\n")
		b.WriteString("\tdirection LR\n")
		for _, node := range layers.Gateway {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	if len(leftExternals) > 0 {
		b.WriteString("subgraph ExternalsLeft[\" \"]\n")
		b.WriteString("\tdirection TB\n")
		for _, node := range leftExternals {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	if len(layers.Spine) > 0 {
		b.WriteString("subgraph Spines[\" \"]\n")
		b.WriteString("\tdirection LR\n")

		for _, node := range layers.Spine {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\tsubgraph %s_Group [\" \"]\n", nodeID))
			b.WriteString("\t\tdirection TB\n")
			b.WriteString(fmt.Sprintf("\t\t%s[\"%s\"]\n", nodeID, label))
			b.WriteString("\tend\n")
		}
		b.WriteString("end\n\n")
	}

	if len(rightExternals) > 0 {
		b.WriteString("subgraph ExternalsRight[\" \"]\n")
		b.WriteString("\tdirection TB\n")
		for _, node := range rightExternals {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	if len(layers.Leaf) > 0 {
		b.WriteString("subgraph Leaves[\" \"]\n")
		b.WriteString("\tdirection LR\n")

		singleLeaves := []Node{}

		for _, node := range layers.Leaf {
			if groupName, hasGroup := node.Properties["redundancyGroup"]; hasGroup && groupName != "" {
				if _, alreadyProcessed := redundancySubgraphs[groupName]; !alreadyProcessed {
					var groupSwitches []Node
					var redundancyType string

					for _, otherNode := range layers.Leaf {
						if otherGroupName, ok := otherNode.Properties["redundancyGroup"]; ok && otherGroupName == groupName {
							groupSwitches = append(groupSwitches, otherNode)
							if redType, hasType := otherNode.Properties["redundancyType"]; hasType && redundancyType == "" {
								redundancyType = redType
							}
						}
					}

					if len(groupSwitches) > 1 {
						redundancySubgraphs[groupName] = groupSwitches
						redundancyTypes[groupName] = redundancyType
					}
				}
			} else {
				isPartOfGroup := false
				for _, groupNodes := range redundancySubgraphs {
					for _, groupNode := range groupNodes {
						if groupNode.ID == node.ID {
							isPartOfGroup = true

							break
						}
					}
					if isPartOfGroup {
						break
					}
				}

				if !isPartOfGroup {
					singleLeaves = append(singleLeaves, node)
				}
			}
		}

		var sortedGroupNames []string
		for groupName := range redundancySubgraphs {
			sortedGroupNames = append(sortedGroupNames, groupName)
		}
		sort.Strings(sortedGroupNames)

		for _, groupName := range sortedGroupNames {
			nodes := redundancySubgraphs[groupName]
			cleanGroupName := cleanID(groupName)
			b.WriteString(fmt.Sprintf("\tsubgraph %s [\"%s\"]\n", cleanGroupName, groupName))
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
		b.WriteString("subgraph Servers[\" \"]\n")
		b.WriteString("\tdirection TB\n")

		for _, node := range layers.Server {
			nodeID := cleanID(node.ID)
			label := formatLabel(node.Label)
			b.WriteString(fmt.Sprintf("\t%s[\"%s\"]\n", nodeID, label))
		}
		b.WriteString("end\n\n")
	}

	connectionMap := make(map[string]map[string][]string)

	// Track parallel connections for aggregation in legend
	maxParallelConnections := make(map[string]int)
	bundledConnections := make(map[string]map[string]int) // Track bundled connections

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

		// Track bundled connections for aggregation
		if link.Type == EdgeTypeBundled {
			var leaf, server string
			if strings.HasPrefix(link.Source, "server-") {
				server = link.Source
				leaf = link.Target
			} else if strings.HasPrefix(link.Target, "server-") {
				server = link.Target
				leaf = link.Source
			}

			if server != "" && leaf != "" {
				var key1, key2 string
				if leaf < server {
					key1, key2 = leaf, server
				} else {
					key1, key2 = server, leaf
				}

				if bundledConnections[key1] == nil {
					bundledConnections[key1] = make(map[string]int)
				}
				bundledConnections[key1][key2]++
			}
		}

		sourceID := cleanID(link.Source)
		targetID := cleanID(link.Target)
		key := sourceID + "->" + targetID

		sourcePort := extractPort(link.Properties["sourcePort"])
		targetPort := extractPort(link.Properties["targetPort"])
		portLabel := ""

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
			case EdgeTypeMesh:
				connType = EdgeTypeMesh
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
			case EdgeTypeExternal:
				connType = EdgeTypeExternal
			case EdgeTypeStaticExternal:
				connType = EdgeTypeStaticExternal
			default:
				connType = "other"
			}

			connectionMap[key][connType] = append(connectionMap[key][connType], portLabel)
		}
	}

	// Calculate max parallel connections for bundled links
	for _, serverConnections := range bundledConnections {
		for _, count := range serverConnections {
			if count > maxParallelConnections["bundled"] {
				maxParallelConnections["bundled"] = count
			}
		}
	}

	// Track link indices for each type of connection
	gatewayLinks := []int{}
	spineLeafLinks := []int{}
	meshLinks := []int{}
	mclagLinks := []int{}
	bundledLinks := []int{}
	eslagLinks := []int{}
	unbundledLinks := []int{}
	externalLinks := []int{}
	staticExternalLinks := []int{}

	linkIndex := 0

	b.WriteString("%% Connections\n\n")

	// Handle gateway connections (to spines or leaves)
	if len(layers.Gateway) > 0 {
		hasGatewayConnections := false

		for key, connTypes := range connectionMap {
			parts := strings.Split(key, "->")
			sourceID := parts[0]
			targetID := parts[1]

			isGatewayConnection := false
			for _, node := range layers.Gateway {
				if cleanID(node.ID) == sourceID || cleanID(node.ID) == targetID {
					isGatewayConnection = true

					break
				}
			}

			if isGatewayConnection {
				for connType, ports := range connTypes {
					if connType == EdgeTypeGateway {
						if !hasGatewayConnections {
							b.WriteString("%% Gateway connections\n")
							hasGatewayConnections = true
						}

						// Ensure gateway is always the source for better rendering
						finalSourceID := sourceID
						finalTargetID := targetID
						finalPorts := make([]string, len(ports))
						copy(finalPorts, ports)

						// Check if target is gateway, if so swap source and target
						isTargetGateway := false
						for _, node := range layers.Gateway {
							if cleanID(node.ID) == targetID {
								isTargetGateway = true

								break
							}
						}

						if isTargetGateway {
							// Swap source and target so gateway is source
							finalSourceID = targetID
							finalTargetID = sourceID
							// Don't invert ports when gateway becomes source
						} else {
							// Gateway is already source, invert the port labels
							for i, portLabel := range ports {
								parts := strings.Split(portLabel, "↔")
								if len(parts) == 2 {
									finalPorts[i] = parts[1] + "↔" + parts[0]
								}
							}
						}

						portLabel := strings.Join(finalPorts, "<br>")
						connection := fmt.Sprintf("%s ---|%q| %s", finalSourceID, portLabel, finalTargetID)
						b.WriteString(connection + "\n")
						gatewayLinks = append(gatewayLinks, linkIndex)
						linkIndex++
					}
				}
			}
		}

		if hasGatewayConnections {
			b.WriteString("\n")
		}
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

	// Handle mesh connections between leaves
	b.WriteString("%% Mesh connections\n")

	// Collect all mesh connections first
	meshConnections := []string{}
	for key, connTypes := range connectionMap {
		parts := strings.Split(key, "->")
		sourceID := parts[0]
		targetID := parts[1]

		isMeshConnection := false
		for _, node := range layers.Leaf {
			if cleanID(node.ID) == sourceID {
				for _, leaf := range layers.Leaf {
					if cleanID(leaf.ID) == targetID {
						isMeshConnection = true

						break
					}
				}

				break
			}
		}

		if isMeshConnection {
			for connType, ports := range connTypes {
				if connType == EdgeTypeMesh {
					portLabel := strings.Join(ports, "<br>")
					connection := fmt.Sprintf("%s ---|%q| %s", sourceID, portLabel, targetID)
					meshConnections = append(meshConnections, connection)
				}
			}
		}
	}

	// Detect mesh triangle topology and sort accordingly
	if len(meshConnections) >= 3 {
		// Get all leaf node IDs that participate in mesh connections
		meshNodeSet := make(map[string]bool)
		for _, conn := range meshConnections {
			getSourceTarget := func(conn string) (string, string) {
				parts := strings.Split(conn, " ---|")
				if len(parts) < 2 {
					return "", ""
				}
				source := parts[0]
				targetPart := strings.Split(parts[1], "| ")
				if len(targetPart) < 2 {
					return source, ""
				}
				target := targetPart[1]

				return source, target
			}

			source, target := getSourceTarget(conn)
			meshNodeSet[source] = true
			meshNodeSet[target] = true
		}

		// Convert to sorted slice for consistent ordering
		meshNodes := make([]string, 0, len(meshNodeSet))
		for node := range meshNodeSet {
			meshNodes = append(meshNodes, node)
		}
		sort.Strings(meshNodes)

		// If we have exactly 3 nodes (triangle), use optimal ordering
		if len(meshNodes) == 3 {
			// Custom sort for mesh triangle to minimize crossings
			// Optimal order: [0->1, 1->2, 0->2] where 0,1,2 are sorted node indices
			sort.Slice(meshConnections, func(i, j int) bool {
				getSourceTarget := func(conn string) (string, string) {
					parts := strings.Split(conn, " ---|")
					if len(parts) < 2 {
						return "", ""
					}
					source := parts[0]
					targetPart := strings.Split(parts[1], "| ")
					if len(targetPart) < 2 {
						return source, ""
					}
					target := targetPart[1]

					return source, target
				}

				sourceI, targetI := getSourceTarget(meshConnections[i])
				sourceJ, targetJ := getSourceTarget(meshConnections[j])

				// Get node indices in sorted order
				getNodeIndex := func(nodeID string) int {
					for idx, node := range meshNodes {
						if node == nodeID {
							return idx
						}
					}

					return -1
				}

				getPriority := func(source, target string) int {
					srcIdx := getNodeIndex(source)
					tgtIdx := getNodeIndex(target)

					// Ensure consistent ordering (smaller index always first)
					if srcIdx > tgtIdx {
						srcIdx, tgtIdx = tgtIdx, srcIdx
					}

					// Priority for mesh triangle: 0->1, 1->2, 0->2
					switch {
					case srcIdx == 0 && tgtIdx == 1:
						return 1 // First connection
					case srcIdx == 1 && tgtIdx == 2:
						return 2 // Second connection
					case srcIdx == 0 && tgtIdx == 2:
						return 3 // Third connection (diagonal)
					default:
						return 10 + srcIdx + tgtIdx // Fallback
					}
				}

				priorityI := getPriority(sourceI, targetI)
				priorityJ := getPriority(sourceJ, targetJ)

				return priorityI < priorityJ
			})
		} else {
			// For non-triangle mesh (more than 3 nodes), use simple alphabetical sort
			sort.Strings(meshConnections)
		}
	} else {
		// For simple cases, use alphabetical sort
		sort.Strings(meshConnections)
	}

	// Output sorted mesh connections
	for _, connection := range meshConnections {
		b.WriteString(connection + "\n")
		meshLinks = append(meshLinks, linkIndex)
		linkIndex++
	}
	b.WriteString("\n")

	// External connections
	b.WriteString("%% External connections\n")
	for key, connTypes := range connectionMap {
		parts := strings.Split(key, "->")
		sourceID := parts[0]
		targetID := parts[1]

		isExternalConnection := false
		isStaticExternalConnection := false

		for _, node := range layers.External {
			if cleanID(node.ID) == sourceID || cleanID(node.ID) == targetID {
				isExternalConnection = true

				// Check if it's a static external connection by looking at the connection type
				for connType := range connTypes {
					if connType == EdgeTypeStaticExternal {
						isStaticExternalConnection = true

						break
					}
				}

				break
			}
		}

		if isExternalConnection {
			for connType, ports := range connTypes {
				if connType == EdgeTypeExternal || connType == EdgeTypeStaticExternal {
					portLabel := strings.Join(ports, "<br>")
					connection := fmt.Sprintf("%s ---|%q| %s", sourceID, portLabel, targetID)
					b.WriteString(connection + "\n")

					if isStaticExternalConnection {
						staticExternalLinks = append(staticExternalLinks, linkIndex)
					} else {
						externalLinks = append(externalLinks, linkIndex)
					}
					linkIndex++
				}
			}
		}
	}
	b.WriteString("\n")

	// Create the legend subgraph
	b.WriteString("subgraph Legend[\"Network Connection Types\"]\n")
	b.WriteString("\tdirection LR\n")
	b.WriteString("\t%% Create invisible nodes for the start and end of each line\n")

	// Only include connection types that are actually present in the diagram
	if len(spineLeafLinks) > 0 {
		b.WriteString("\tL1(( )) --- |\"Fabric Links\"| L2(( ))\n")
	}

	if len(meshLinks) > 0 {
		b.WriteString("\tL15(( )) --- |\"Mesh Links\"| L16(( ))\n")
	}

	if len(mclagLinks) > 0 {
		b.WriteString("\tL3(( )) --- |\"MCLAG Server Links\"| L4(( ))\n")
	}

	if len(bundledLinks) > 0 {
		bundledLabel := "Bundled Server Links"
		if maxParallelConnections["bundled"] > 1 {
			bundledLabel = fmt.Sprintf("Bundled Server Links (x%d)", maxParallelConnections["bundled"])
		}
		b.WriteString(fmt.Sprintf("\tL5(( )) --- |\"%s\"| L6(( ))\n", bundledLabel))
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

	if len(externalLinks) > 0 {
		b.WriteString("\tL13(( )) --- |\"External Links\"| L14(( ))\n")
	}

	if len(staticExternalLinks) > 0 {
		b.WriteString("\tL17(( )) --- |\"Static External Links\"| L18(( ))\n")
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

	if len(layers.External) > 0 {
		externalIDs := []string{}
		for _, node := range layers.External {
			externalIDs = append(externalIDs, cleanID(node.ID))
		}
		b.WriteString(fmt.Sprintf("class %s external\n", strings.Join(externalIDs, ",")))
	}

	// Use actual redundancy group names in subgraphs
	for groupName := range redundancySubgraphs {
		redundancyType := redundancyTypes[groupName]
		cleanGroupName := cleanID(groupName)

		switch redundancyType {
		case RedundancyTypeMCLAG:
			b.WriteString(fmt.Sprintf("class %s mclag\n", cleanGroupName))
		case RedundancyTypeESLAG:
			b.WriteString(fmt.Sprintf("class %s eslag\n", cleanGroupName))
		}
	}

	// Update hidden class to include P1,P2 and mesh legend nodes
	hiddenNodes := []string{"L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8", "L9", "L10", "L11", "L12", "P1", "P2"}
	if len(externalLinks) > 0 {
		hiddenNodes = append(hiddenNodes, "L13", "L14")
	}
	if len(staticExternalLinks) > 0 {
		hiddenNodes = append(hiddenNodes, "L17", "L18")
	}
	if len(meshLinks) > 0 {
		hiddenNodes = append(hiddenNodes, "L15", "L16")
	}
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

	if len(meshLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#0078D4,stroke-width:4px\n", formatIndices(meshLinks)))
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

	if len(externalLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#D79B00,stroke-width:2px\n", formatIndices(externalLinks)))
	}

	if len(staticExternalLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %s stroke:#D79B00,stroke-width:2px\n", formatIndices(staticExternalLinks)))
	}

	// Calculate legend link indices
	legendLinkStart := linkIndex
	legendLinkIndex := 0

	// Style the legend connection types that are present
	if len(spineLeafLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#B85450,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	if len(meshLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#0078D4,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
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

	if len(externalLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#D79B00,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	if len(staticExternalLinks) > 0 {
		b.WriteString(fmt.Sprintf("linkStyle %d stroke:#D79B00,stroke-width:2px\n", legendLinkStart+legendLinkIndex))
		legendLinkIndex++
	}

	// Style the label notation line - just use a single white stroke
	b.WriteString(fmt.Sprintf("linkStyle %d stroke:#FFFFFF\n", legendLinkStart+legendLinkIndex))

	b.WriteString("\n%% Make subgraph containers invisible\n")
	if len(layers.Gateway) > 0 {
		b.WriteString("style Gateways fill:none,stroke:none\n")
	}
	if len(leftExternals) > 0 {
		b.WriteString("style ExternalsLeft fill:none,stroke:none\n")
	}
	if len(layers.Spine) > 0 {
		b.WriteString("style Spines fill:none,stroke:none\n")
	}
	if len(rightExternals) > 0 {
		b.WriteString("style ExternalsRight fill:none,stroke:none\n")
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

func splitMermaidExternalNodes(externals []Node, links []Link, leaves []Node) ([]Node, []Node) {
	leftExternals := []Node{}
	rightExternals := []Node{}

	leafIndexMap := make(map[string]int)
	for i, leaf := range leaves {
		leafIndexMap[leaf.ID] = i
	}

	midpoint := len(leaves) / 2

	for _, node := range externals {
		leftConnections := 0
		rightConnections := 0

		for _, link := range links {
			var leafID string
			switch {
			case link.Source == node.ID:
				leafID = link.Target
			case link.Target == node.ID:
				leafID = link.Source
			default:
				continue
			}

			idx, exists := leafIndexMap[leafID]
			if !exists {
				continue
			}

			if idx < midpoint {
				leftConnections++
			} else {
				rightConnections++
			}
		}

		if leftConnections > rightConnections {
			leftExternals = append(leftExternals, node)
		} else {
			rightExternals = append(rightExternals, node)
		}
	}

	return leftExternals, rightExternals
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
