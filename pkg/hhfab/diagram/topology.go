// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func extractNodeID(port string) string {
	if idx := strings.Index(port, "/"); idx > 0 {
		return port[:idx]
	}

	return port
}

func extractPort(port string) string {
	if idx := strings.Index(port, "/"); idx >= 0 && idx < len(port)-1 {
		return port[idx+1:]
	}

	return port
}

type LayeredNodes struct {
	Spine  []Node
	Leaf   []Node
	Server []Node
}

type serverConnection struct {
	primaryLeaf   string
	secondaryLeaf string
	connTypes     map[string][]string // leaf -> connection types
	mclagPair     string
	eslagPair     string
}

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

func sortNodes(nodes []Node, links []Link) LayeredNodes {
	var result LayeredNodes
	leafOrder := make(map[string]int)

	// First separate nodes by type and sort spines
	for _, node := range nodes {
		if node.Type == NodeTypeSwitch {
			if role, ok := node.Properties["role"]; ok && role == SwitchRoleSpine {
				result.Spine = append(result.Spine, node)
			} else {
				result.Leaf = append(result.Leaf, node)
			}
		} else if node.Type == NodeTypeServer {
			result.Server = append(result.Server, node)
		}
	}

	// Sort spine and leaf nodes by ID
	sort.Slice(result.Spine, func(i, j int) bool {
		return result.Spine[i].ID < result.Spine[j].ID
	})
	sort.Slice(result.Leaf, func(i, j int) bool {
		return result.Leaf[i].ID < result.Leaf[j].ID
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
				if role, ok := obj.Spec["role"].(string); ok {
					if node.Properties == nil {
						node.Properties = make(map[string]string)
					}
					node.Properties["role"] = role
					node.Label = fmt.Sprintf("%s\n%s", name, role)
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

		case "Connection":
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
				}
			}
		}
	}

	return topo, nil
}
