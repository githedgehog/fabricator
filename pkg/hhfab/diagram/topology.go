// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// extractNodeID extracts the node ID portion from a port identifier string
// in the format "nodeid/portname"
func extractNodeID(port string) string {
	if idx := strings.Index(port, "/"); idx > 0 {
		return port[:idx]
	}

	return port
}

// extractPort extracts the port name portion from a port identifier string
// in the format "nodeid/portname"
func extractPort(port string) string {
	if idx := strings.Index(port, "/"); idx >= 0 && idx < len(port)-1 {
		return port[idx+1:]
	}

	return port
}

// LayeredNodes organizes nodes into their respective network layers
// for proper layout in the diagram
type LayeredNodes struct {
	Spine    []Node
	Leaf     []Node
	Server   []Node
	Gateway  []Node
	External []Node // External nodes connecting to the network
}

// serverConnection tracks the connectivity patterns for a server
// including its primary and secondary leaf connections
type serverConnection struct {
	primaryLeaf   string
	secondaryLeaf string
	connTypes     map[string][]string // leaf -> connection types
	mclagPair     string
	eslagPair     string
}

// hasConnections checks if a node has any connections in the provided links
func hasConnections(nodeID string, links []Link) bool {
	for _, link := range links {
		if link.Source == nodeID || link.Target == nodeID {
			return true
		}
	}

	return false
}

// findConnectionTypes analyzes all links to determine server connection patterns
// and identifies MCLAG and ESLAG server pairs
func findConnectionTypes(links []Link) map[string]*serverConnection {
	serverConns := make(map[string]*serverConnection)

	// Build initial connection map
	for _, link := range links {
		var server, leaf string
		if strings.HasPrefix(link.Source, "server-") {
			server = link.Source
			leaf = link.Target
		} else if strings.HasPrefix(link.Target, "server-") {
			server = link.Target
			leaf = link.Source
		}

		if server == "" || leaf == "" {
			continue
		}

		if serverConns[server] == nil {
			serverConns[server] = &serverConnection{
				connTypes: make(map[string][]string),
			}
		}
		conn := serverConns[server]
		conn.connTypes[leaf] = append(conn.connTypes[leaf], link.Type)
	}

	// Find MCLAG/ESLAG pairs
	for s1, conn1 := range serverConns {
		if conn1.mclagPair != "" || conn1.eslagPair != "" {
			continue
		}

		for s2, conn2 := range serverConns {
			if s1 >= s2 {
				continue
			}

			if len(conn1.connTypes) != len(conn2.connTypes) {
				continue
			}

			// Check if connection patterns match
			match := true
			pairType := ""
			for leaf, types1 := range conn1.connTypes {
				types2, exists := conn2.connTypes[leaf]
				if !exists || len(types1) != len(types2) {
					match = false

					break
				}

				sort.Strings(types1)
				sort.Strings(types2)
				for i := range types1 {
					if types1[i] != types2[i] {
						match = false

						break
					}
					if types1[i] == EdgeTypeMCLAG {
						pairType = EdgeTypeMCLAG
					} else if types1[i] == EdgeTypeESLAG && pairType == "" {
						pairType = EdgeTypeESLAG
					}
				}
			}

			if match && pairType != "" {
				if pairType == EdgeTypeMCLAG {
					conn1.mclagPair = s2
					conn2.mclagPair = s1
				} else {
					conn1.eslagPair = s2
					conn2.eslagPair = s1
				}
			}
		}
	}

	return serverConns
}

// sortNodes organizes nodes into their respective layers (spine, leaf, server, gateway, external)
// and sorts them appropriately within each layer for optimal diagram layout
func sortNodes(nodes []Node, links []Link) LayeredNodes {
	var result LayeredNodes
	leafOrder := make(map[string]int)

	// First pass: categorize nodes by type
	for _, node := range nodes {
		switch node.Type {
		case NodeTypeSwitch:
			if role, ok := node.Properties["role"]; ok && role == SwitchRoleSpine {
				result.Spine = append(result.Spine, node)
			} else {
				result.Leaf = append(result.Leaf, node)
			}
		case NodeTypeServer:
			result.Server = append(result.Server, node)
		case NodeTypeGateway:
			result.Gateway = append(result.Gateway, node)
		case NodeTypeExternal:
			// Only include external nodes that have connections
			if hasConnections(node.ID, links) {
				result.External = append(result.External, node)
			}
		}
	}

	// Sort spine nodes by description first, then by ID
	sort.Slice(result.Spine, func(i, j int) bool {
		descI, hasDescI := result.Spine[i].Properties["description"]
		descJ, hasDescJ := result.Spine[j].Properties["description"]

		if hasDescI && hasDescJ { //nolint:gocritic
			if descI != descJ {
				return descI < descJ
			}
		} else if hasDescI {
			return true
		} else if hasDescJ {
			return false
		}

		return result.Spine[i].ID < result.Spine[j].ID
	})

	// Sort leaf nodes by description first, then by ID
	sort.Slice(result.Leaf, func(i, j int) bool {
		descI, hasDescI := result.Leaf[i].Properties["description"]
		descJ, hasDescJ := result.Leaf[j].Properties["description"]

		if hasDescI && hasDescJ { //nolint:gocritic
			if descI != descJ {
				return descI < descJ
			}
		} else if hasDescI {
			return true
		} else if hasDescJ {
			return false
		}

		return result.Leaf[i].ID < result.Leaf[j].ID
	})

	// Sort gateway nodes by ID
	sort.Slice(result.Gateway, func(i, j int) bool {
		return result.Gateway[i].ID < result.Gateway[j].ID
	})

	// Sort external nodes by ID
	sort.Slice(result.External, func(i, j int) bool {
		return result.External[i].ID < result.External[j].ID
	})

	// Create leaf order map
	for i, leaf := range result.Leaf {
		leafOrder[leaf.ID] = i
	}

	// Get server connection information
	serverConns := findConnectionTypes(links)

	// Determine primary leaf for each server
	for _, conn := range serverConns {
		if len(conn.connTypes) == 1 {
			// Single-homed server
			for leaf := range conn.connTypes {
				conn.primaryLeaf = leaf
			}

			continue
		}

		// Multi-homed server
		var leaves []string
		for leaf := range conn.connTypes {
			leaves = append(leaves, leaf)
		}
		sort.Slice(leaves, func(i, j int) bool {
			return leafOrder[leaves[i]] < leafOrder[leaves[j]]
		})

		// Prefer bundled connection for primary leaf
		primarySet := false
		for _, leaf := range leaves {
			for _, t := range conn.connTypes[leaf] {
				if t == EdgeTypeBundled {
					conn.primaryLeaf = leaf
					for _, l := range leaves {
						if l != leaf {
							conn.secondaryLeaf = l
						}
					}
					primarySet = true

					break
				}
			}
			if primarySet {
				break
			}
		}

		if !primarySet {
			// No bundled connection, use leftmost leaf as primary
			conn.primaryLeaf = leaves[0]
			if len(leaves) > 1 {
				conn.secondaryLeaf = leaves[1]
			}
		}
	}

	// Group servers by primary leaf
	serversByLeaf := make(map[string][]string)
	for server, conn := range serverConns {
		if conn.primaryLeaf != "" {
			serversByLeaf[conn.primaryLeaf] = append(serversByLeaf[conn.primaryLeaf], server)
		}
	}

	// Sort servers within each leaf group
	for leaf, servers := range serversByLeaf {
		sort.Slice(servers, func(i, j int) bool {
			s1, s2 := servers[i], servers[j]
			c1, c2 := serverConns[s1], serverConns[s2]

			// Keep pairs together
			if c1.mclagPair == s2 {
				return s1 < s2 // Smaller ID first in MCLAG pair
			}
			if c2.mclagPair == s1 {
				return s2 < s1 // Smaller ID first in MCLAG pair
			}
			if c1.eslagPair == s2 {
				return s1 < s2 // Smaller ID first in ESLAG pair
			}
			if c2.eslagPair == s1 {
				return s2 < s1 // Smaller ID first in ESLAG pair
			}

			// For ESLAG pairs, try to place them next to their connected servers
			if c1.eslagPair != "" && c2.eslagPair == "" {
				return false // Non-ESLAG servers first
			}
			if c1.eslagPair == "" && c2.eslagPair != "" {
				return true // Non-ESLAG servers first
			}

			// Handle left vs right leaf
			isLeftLeaf := leafOrder[leaf] < len(result.Leaf)/2

			// For left leaves: single-homed first, multi-homed last
			// For right leaves: multi-homed first, single-homed last
			if (c1.secondaryLeaf == "") != (c2.secondaryLeaf == "") {
				return (isLeftLeaf == (c1.secondaryLeaf == ""))
			}

			// Both single-homed or both multi-homed: sort by ID
			return s1 < s2
		})
		serversByLeaf[leaf] = servers
	}

	// Create final ordered server list
	var orderedServers []string
	seenServers := make(map[string]bool)

	// Process each leaf
	for _, leaf := range result.Leaf {
		for _, server := range serversByLeaf[leaf.ID] {
			if !seenServers[server] {
				orderedServers = append(orderedServers, server)
				seenServers[server] = true

				// Add pair immediately after if not yet seen
				conn := serverConns[server]
				if conn.mclagPair != "" && !seenServers[conn.mclagPair] {
					orderedServers = append(orderedServers, conn.mclagPair)
					seenServers[conn.mclagPair] = true
				}
				if conn.eslagPair != "" && !seenServers[conn.eslagPair] {
					orderedServers = append(orderedServers, conn.eslagPair)
					seenServers[conn.eslagPair] = true
				}
			}
		}
	}

	// Sort server nodes based on the ordered list
	sort.Slice(result.Server, func(i, j int) bool {
		posI := -1
		posJ := -1
		for idx, id := range orderedServers {
			if result.Server[i].ID == id {
				posI = idx
			}
			if result.Server[j].ID == id {
				posJ = idx
			}
		}
		if posI == -1 {
			return false
		}
		if posJ == -1 {
			return true
		}

		return posI < posJ
	})

	return result
}

// ConvertJSONToTopology converts the JSON representation of the network
// into a Topology structure with nodes and links for visualization
func ConvertJSONToTopology(jsonData []byte) (Topology, error) {
	var raw []struct {
		Kind       string                 `json:"kind"`
		APIVersion string                 `json:"apiVersion"`
		Metadata   map[string]interface{} `json:"metadata"`
		Spec       map[string]interface{} `json:"spec"`
	}

	if err := json.Unmarshal(jsonData, &raw); err != nil {
		return Topology{}, fmt.Errorf("parsing JSON: %w", err)
	}

	var topo Topology
	nodeSet := make(map[string]bool)
	externalResourceMap := make(map[string]bool)       // Map to track external resources
	externalConnections := make(map[string]string)     // Maps connection names to switch ports
	connectionToExternals := make(map[string][]string) // Maps connection names to external resources

	// First pass: Collect all External resources and create nodes for them
	for _, obj := range raw {
		if obj.Kind == "External" {
			name, ok := obj.Metadata["name"].(string)
			if !ok {
				continue
			}

			// Track external resource names
			externalResourceMap[name] = true

			// Create an external node for this resource
			if !nodeSet[name] {
				nodeSet[name] = true
				node := Node{
					ID:    name,
					Type:  NodeTypeExternal,
					Label: fmt.Sprintf("%s\n%s", name, SwitchRoleExternal),
				}
				if node.Properties == nil {
					node.Properties = make(map[string]string)
				}
				node.Properties["role"] = SwitchRoleExternal
				topo.Nodes = append(topo.Nodes, node)
			}
		}
	}

	// Second pass: Map external attachments to connection names
	for _, obj := range raw {
		if obj.Kind == "ExternalAttachment" {
			if conn, ok := obj.Spec["connection"].(string); ok {
				if ext, ok := obj.Spec["external"].(string); ok {
					connectionToExternals[conn] = append(connectionToExternals[conn], ext)
				}
			}
		}
	}

	// Third pass: Create all other nodes and gather external connection information
	for _, obj := range raw {
		switch obj.Kind {
		case "Switch":
			name, ok := obj.Metadata["name"].(string)
			if !ok {
				continue
			}
			if !nodeSet[name] {
				nodeSet[name] = true
				node := Node{
					ID:    name,
					Type:  "switch",
					Label: name,
				}

				// Initialize properties map if needed
				if node.Properties == nil {
					node.Properties = make(map[string]string)
				}

				// Extract role from spec
				if role, ok := obj.Spec["role"].(string); ok {
					node.Properties["role"] = role
					node.Label = fmt.Sprintf("%s\n%s", name, role)
				}

				// Extract description from spec
				if description, ok := obj.Spec["description"].(string); ok {
					node.Properties["description"] = description
				}

				topo.Nodes = append(topo.Nodes, node)
			}

		case "Server":
			name, ok := obj.Metadata["name"].(string)
			if !ok {
				continue
			}
			if !nodeSet[name] {
				nodeSet[name] = true
				node := Node{
					ID:    name,
					Type:  "server",
					Label: name,
				}
				topo.Nodes = append(topo.Nodes, node)
			}

		case "Node":
			name, ok := obj.Metadata["name"].(string)
			if !ok {
				continue
			}
			if !nodeSet[name] {
				nodeSet[name] = true

				isGateway := false
				if roles, ok := obj.Spec["roles"].([]interface{}); ok {
					for _, role := range roles {
						if r, ok := role.(string); ok && r == "gateway" {
							isGateway = true

							break
						}
					}
				}

				if isGateway {
					node := Node{
						ID:    name,
						Type:  "gateway",
						Label: name,
					}
					topo.Nodes = append(topo.Nodes, node)
				}
			}

		case "Connection":
			name, ok := obj.Metadata["name"].(string)
			if !ok {
				continue
			}

			// Check if this is an external connection by looking for the 'external' field in spec
			if externalSpec, ok := obj.Spec["external"].(map[string]interface{}); ok {
				if linkInfo, ok := externalSpec["link"].(map[string]interface{}); ok {
					if switchInfo, ok := linkInfo["switch"].(map[string]interface{}); ok {
						if port, ok := switchInfo["port"].(string); ok {
							externalConnections[name] = port
						}
					}
				}
			}
		}
	}

	// Fourth pass: Process all connections and create links
	for _, obj := range raw {
		if obj.Kind == "Connection" {
			for key, val := range obj.Spec {
				switch key {
				case "fabric", "mclag", "bundled", "eslag", "vpcLoopback":
					if m, ok := val.(map[string]interface{}); ok {
						if arr, ok := m["links"].([]interface{}); ok {
							for _, linkObj := range arr {
								if linkMap, ok := linkObj.(map[string]interface{}); ok {
									props := make(map[string]string)
									var source, target string
									if key == "fabric" {
										if spine, ok := linkMap["spine"].(map[string]interface{}); ok {
											if port, ok := spine["port"].(string); ok {
												source = extractNodeID(port)
												props["sourcePort"] = port
											}
										}
										if leaf, ok := linkMap["leaf"].(map[string]interface{}); ok {
											if port, ok := leaf["port"].(string); ok {
												target = extractNodeID(port)
												props["targetPort"] = port
											}
										}
									} else {
										if server, ok := linkMap["server"].(map[string]interface{}); ok {
											if port, ok := server["port"].(string); ok {
												source = extractNodeID(port)
												props["sourcePort"] = port
											}
										}
										if sw, ok := linkMap["switch"].(map[string]interface{}); ok {
											if port, ok := sw["port"].(string); ok {
												target = extractNodeID(port)
												props["targetPort"] = port
											}
										}
									}
									if source != "" && target != "" {
										topo.Links = append(topo.Links, Link{
											Source:     source,
											Target:     target,
											Type:       key,
											Properties: props,
										})
									}
								}
							}
						}
					}

				case "unbundled":
					if m, ok := val.(map[string]interface{}); ok {
						if linkVal, ok := m["link"].(map[string]interface{}); ok {
							m = linkVal
						}
						props := make(map[string]string)
						var source, target string
						if server, ok := m["server"].(map[string]interface{}); ok {
							if port, ok := server["port"].(string); ok {
								source = extractNodeID(port)
								props["sourcePort"] = port
							}
						}
						if sw, ok := m["switch"].(map[string]interface{}); ok {
							if port, ok := sw["port"].(string); ok {
								target = extractNodeID(port)
								props["targetPort"] = port
							}
						}
						if source != "" && target != "" {
							topo.Links = append(topo.Links, Link{
								Source:     source,
								Target:     target,
								Type:       key,
								Properties: props,
							})
						}
					}

				case "mclagDomain":
					if m, ok := val.(map[string]interface{}); ok {
						if arr, ok := m["peerLinks"].([]interface{}); ok {
							for _, linkObj := range arr {
								if linkMap, ok := linkObj.(map[string]interface{}); ok {
									props := make(map[string]string)
									var source, target string
									if sw1, ok := linkMap["switch1"].(map[string]interface{}); ok {
										if port, ok := sw1["port"].(string); ok {
											source = extractNodeID(port)
											props["sourcePort"] = port
										}
									}
									if sw2, ok := linkMap["switch2"].(map[string]interface{}); ok {
										if port, ok := sw2["port"].(string); ok {
											target = extractNodeID(port)
											props["targetPort"] = port
										}
									}
									props["mclagType"] = "peer"
									if source != "" && target != "" {
										topo.Links = append(topo.Links, Link{
											Source:     source,
											Target:     target,
											Type:       "mclag",
											Properties: props,
										})
									}
								}
							}
						}
						if arr, ok := m["sessionLinks"].([]interface{}); ok {
							for _, linkObj := range arr {
								if linkMap, ok := linkObj.(map[string]interface{}); ok {
									props := make(map[string]string)
									var source, target string
									if sw1, ok := linkMap["switch1"].(map[string]interface{}); ok {
										if port, ok := sw1["port"].(string); ok {
											source = extractNodeID(port)
											props["sourcePort"] = port
										}
									}
									if sw2, ok := linkMap["switch2"].(map[string]interface{}); ok {
										if port, ok := sw2["port"].(string); ok {
											target = extractNodeID(port)
											props["targetPort"] = port
										}
									}
									props["mclagType"] = "session"
									if source != "" && target != "" {
										topo.Links = append(topo.Links, Link{
											Source:     source,
											Target:     target,
											Type:       "mclag",
											Properties: props,
										})
									}
								}
							}
						}
					}
				case "gateway":
					if m, ok := val.(map[string]interface{}); ok {
						if arr, ok := m["links"].([]interface{}); ok {
							for _, linkObj := range arr {
								if linkMap, ok := linkObj.(map[string]interface{}); ok {
									props := make(map[string]string)
									var source, target string
									if gateway, ok := linkMap["gateway"].(map[string]interface{}); ok {
										if port, ok := gateway["port"].(string); ok {
											source = extractNodeID(port)
											props["sourcePort"] = port
										}
									}
									if spine, ok := linkMap["spine"].(map[string]interface{}); ok {
										if port, ok := spine["port"].(string); ok {
											target = extractNodeID(port)
											props["targetPort"] = port
										}
									}
									if source != "" && target != "" {
										topo.Links = append(topo.Links, Link{
											Source:     source,
											Target:     target,
											Type:       "gateway",
											Properties: props,
										})
									}
								}
							}
						}
					}
				}
			}

			// Process external connections to create links between External nodes and switches
			name, ok := obj.Metadata["name"].(string)
			if !ok {
				continue
			}

			// Check if this is a connection with an external link
			if port, exists := externalConnections[name]; exists {
				switchID := extractNodeID(port)

				// Get the externals associated with this connection from ExternalAttachments
				externalNames := connectionToExternals[name]

				// If there are no explicit mappings, we still want to show the physical connection
				// So pick just one external to represent it
				if len(externalNames) == 0 {
					// Find one external to represent the connection
					for extName := range externalResourceMap {
						externalNames = []string{extName}

						break
					}
				} else {
					// If there are multiple externals for this connection, just use the first one
					// to represent the physical connection
					externalNames = externalNames[:1]
				}

				// Create one link to represent the physical connection
				for _, externalName := range externalNames {
					props := make(map[string]string)
					props["targetPort"] = port
					props["connectionName"] = name
					props["externalName"] = externalName

					topo.Links = append(topo.Links, Link{
						Source:     externalName,
						Target:     switchID,
						Type:       EdgeTypeExternal,
						Properties: props,
					})

					// Only create one physical connection per port
					break
				}
			}
		}
	}

	// Ensure unique links by filtering duplicates
	var uniqueLinks []Link
	seen := make(map[string]bool)

	for _, link := range topo.Links {
		// Create a key for this link to detect duplicates
		key := fmt.Sprintf("%s-%s-%s-%s", link.Source, link.Target, link.Type, link.Properties["targetPort"])
		if !seen[key] {
			seen[key] = true
			uniqueLinks = append(uniqueLinks, link)
		}
	}

	topo.Links = uniqueLinks

	return topo, nil
}

// DetermineExternalSidePlacement decides whether an external node should be placed
// on the left or right side of the diagram, based on its connectivity
func DetermineExternalSidePlacement(nodeID string, links []Link, leaves []Node) string {
	leafIndexMap := make(map[string]int)
	for i, leaf := range leaves {
		leafIndexMap[leaf.ID] = i
	}

	leftConnections := 0
	rightConnections := 0
	leftWeight := 0  // Weighted connections for left side (emphasizing far left)
	rightWeight := 0 // Weighted connections for right side (emphasizing far right)
	midpoint := len(leaves) / 2

	// Count connections to leaves on left vs right side
	for _, link := range links {
		var leafID string

		switch {
		case link.Source == nodeID:
			leafID = link.Target
		case link.Target == nodeID:
			leafID = link.Source
		default:
			continue
		}

		// Check if this is a leaf connection
		idx, exists := leafIndexMap[leafID]
		if !exists {
			continue
		}

		// Calculate position weight based on distance from center
		positionWeight := (midpoint - idx)
		if positionWeight < 0 {
			positionWeight = -positionWeight
		}
		positionWeight++ // Ensure at least weight 1

		// Count as left or right based on leaf position
		if idx < midpoint {
			leftConnections++
			leftWeight += positionWeight
		} else {
			rightConnections++
			rightWeight += positionWeight
		}
	}

	// Position based on weighted connections
	if leftWeight > rightWeight {
		return NodeSideLeft
	} else if rightWeight > leftWeight {
		return NodeSideRight
	}

	// If weights are equal, use connection count
	if leftConnections > rightConnections {
		return NodeSideLeft
	} else if rightConnections > leftConnections {
		return NodeSideRight
	}

	// If still equal, use node ID to ensure deterministic placement
	// This helps balance when multiple externals have identical connection patterns
	nodeIDSum := 0
	for _, c := range nodeID {
		nodeIDSum += int(c)
	}
	if nodeIDSum%2 == 0 {
		return NodeSideLeft
	}

	return NodeSideRight
}
