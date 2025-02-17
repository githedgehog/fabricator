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

func sortNodes(nodes []Node) LayeredNodes {
	var result LayeredNodes
	for _, node := range nodes {
		if node.Type == "switch" {
			if role, ok := node.Properties["role"]; ok && role == "spine" {
				result.Spine = append(result.Spine, node)
			} else {
				result.Leaf = append(result.Leaf, node)
			}
		} else if node.Type == "server" {
			result.Server = append(result.Server, node)
		}
	}

	sort.Slice(result.Spine, func(i, j int) bool { return result.Spine[i].ID < result.Spine[j].ID })
	sort.Slice(result.Leaf, func(i, j int) bool { return result.Leaf[i].ID < result.Leaf[j].ID })
	sort.Slice(result.Server, func(i, j int) bool { return result.Server[i].ID < result.Server[j].ID })

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
