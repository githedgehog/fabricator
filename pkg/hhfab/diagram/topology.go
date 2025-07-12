// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	"context"
	"fmt"
	"sort"
	"strings"

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func extractPort(port string) string {
	if idx := strings.Index(port, "/"); idx >= 0 && idx < len(port)-1 {
		return port[idx+1:]
	}

	return port
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

func sortNodes(nodes []Node, links []Link) TieredNodes {
	var result TieredNodes
	leafOrder := make(map[string]int)

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

func GetTopologyFor(ctx context.Context, client kclient.Reader) (Topology, error) {
	topo := Topology{}
	nodeSet := make(map[string]bool)
	// Maps connection names to external names
	connectionToExternalMap := make(map[string][]string)

	// First load all ExternalAttachment resources to map connections to externals
	externalAttachments := &vpcapi.ExternalAttachmentList{}
	if err := client.List(ctx, externalAttachments); err != nil {
		return topo, fmt.Errorf("listing external attachments: %w", err)
	}

	// Build a map of connection name to external resources
	for _, attachment := range externalAttachments.Items {
		connectionToExternalMap[attachment.Spec.Connection] = append(
			connectionToExternalMap[attachment.Spec.Connection],
			attachment.Spec.External,
		)
	}

	// Load external resources as nodes
	externals := &vpcapi.ExternalList{}
	if err := client.List(ctx, externals); err != nil {
		return topo, fmt.Errorf("listing externals: %w", err)
	}
	for _, external := range externals.Items {
		if nodeSet[external.Name] {
			continue
		}

		nodeSet[external.Name] = true
		node := Node{
			ID:    external.Name,
			Type:  NodeTypeExternal,
			Label: external.Name,
			Properties: map[string]string{
				PropRole: SwitchRoleExternal,
			},
		}
		topo.Nodes = append(topo.Nodes, node)
	}

	nodes := &fabapi.FabNodeList{}
	if err := client.List(ctx, nodes); err != nil {
		return topo, fmt.Errorf("listing nodes: %w", err)
	}
	for _, node := range nodes.Items {
		if nodeSet[node.Name] {
			continue
		}

		// skip non-gateway nodes
		if len(node.Spec.Roles) != 1 || node.Spec.Roles[0] != fabapi.NodeRoleGateway {
			continue
		}

		nodeSet[node.Name] = true
		node := Node{
			ID:    node.Name,
			Type:  NodeTypeGateway,
			Label: node.Name,
		}
		topo.Nodes = append(topo.Nodes, node)
	}

	switches := &wiringapi.SwitchList{}
	if err := client.List(ctx, switches); err != nil {
		return topo, fmt.Errorf("listing switches: %w", err)
	}
	for _, sw := range switches.Items {
		if nodeSet[sw.Name] {
			continue
		}

		nodeSet[sw.Name] = true
		node := Node{
			ID:         sw.Name,
			Type:       NodeTypeSwitch,
			Label:      sw.Name,
			Properties: map[string]string{},
		}

		role := string(sw.Spec.Role)
		node.Properties[PropRole] = role
		node.Label = fmt.Sprintf("%s\n%s", sw.Name, role)

		node.Properties[PropDescription] = sw.Spec.Description

		topo.Nodes = append(topo.Nodes, node)
	}

	servers := &wiringapi.ServerList{}
	if err := client.List(ctx, servers); err != nil {
		return topo, fmt.Errorf("listing servers: %w", err)
	}
	for _, server := range servers.Items {
		if nodeSet[server.Name] {
			continue
		}

		nodeSet[server.Name] = true
		node := Node{
			ID:         server.Name,
			Type:       NodeTypeServer,
			Label:      server.Name,
			Properties: map[string]string{},
		}

		topo.Nodes = append(topo.Nodes, node)
	}

	// Process all connections
	conns := &wiringapi.ConnectionList{}
	if err := client.List(ctx, conns); err != nil {
		return topo, fmt.Errorf("listing connections: %w", err)
	}

	// First pass: collect all external connections
	externalConnections := make(map[string]string) // Maps connection name to switch port
	for _, conn := range conns.Items {
		if conn.Spec.External != nil && conn.Spec.External.Link.Switch.Port != "" {
			externalConnections[conn.Name] = conn.Spec.External.Link.Switch.Port
		}
	}

	// Second pass: process all non-external connections
	for _, conn := range conns.Items {
		_, _, _, links, err := conn.Spec.Endpoints()
		if err != nil {
			continue
		}

		// Handle standard (non-external) connections
		if conn.Spec.Fabric != nil || conn.Spec.Gateway != nil || conn.Spec.MCLAGDomain != nil ||
			conn.Spec.Unbundled != nil || conn.Spec.MCLAG != nil || conn.Spec.Bundled != nil ||
			conn.Spec.ESLAG != nil {
			for source, target := range links {
				link := Link{
					Source: wiringapi.SplitPortName(source)[0],
					Target: wiringapi.SplitPortName(target)[0],
					Type:   conn.Spec.Type(),
					Properties: map[string]string{
						PropSourcePort: source,
						PropTargetPort: target,
					},
				}

				if conn.Spec.MCLAGDomain != nil {
					link.Type = EdgeTypeMCLAG // just to keep compat with current diagram impl

					for _, connLink := range conn.Spec.MCLAGDomain.PeerLinks {
						if connLink.Switch1.Port == source && connLink.Switch2.Port == target {
							link.Properties[PropMCLAGType] = MCLAGTypePeer
						}
					}
					for _, connLink := range conn.Spec.MCLAGDomain.SessionLinks {
						if connLink.Switch1.Port == source && connLink.Switch2.Port == target {
							link.Properties[PropMCLAGType] = MCLAGTypeSession
						}
					}
				}

				topo.Links = append(topo.Links, link)
			}
		}
	}

	// Third pass: handle external connections
	for connName, switchPort := range externalConnections {
		switchID := wiringapi.SplitPortName(switchPort)[0]

		// Get the external names linked to this connection
		externalNames, ok := connectionToExternalMap[connName]
		if !ok || len(externalNames) == 0 {
			// If no mapping found, try to use any available external
			for _, node := range topo.Nodes {
				if node.Type == NodeTypeExternal {
					externalNames = []string{node.ID}

					break
				}
			}

			// If still no externals, skip this connection
			if len(externalNames) == 0 {
				continue
			}
		}

		// Create links from switch to each external
		for _, externalName := range externalNames {
			link := Link{
				Source: switchID,
				Target: externalName,
				Type:   EdgeTypeExternal,
				Properties: map[string]string{
					PropSourcePort:   switchPort,
					"connectionName": connName,
				},
			}
			topo.Links = append(topo.Links, link)
		}
	}

	return topo, nil
}

func getNodeTypeInfo(node Node) (string, string) {
	var nodeType, nodeRole string
	nodeType = node.Type
	if nodeType == NodeTypeSwitch && node.Properties != nil {
		nodeRole = node.Properties[PropRole]
	}

	return nodeType, nodeRole
}

func DetermineExternalSidePlacement(nodeID string, links []Link, leaves []Node) string {
	leafIndexMap := make(map[string]int)
	for i, leaf := range leaves {
		leafIndexMap[leaf.ID] = i
	}

	leftConnections := 0
	rightConnections := 0
	leftWeight := 0  // Weighted connections for left side
	rightWeight := 0 // Weighted connections for right side
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
	nodeIDSum := 0
	for _, c := range nodeID {
		nodeIDSum += int(c)
	}
	if nodeIDSum%2 == 0 {
		return NodeSideLeft
	}

	return NodeSideRight
}

func hasConnections(nodeID string, links []Link) bool {
	for _, link := range links {
		if link.Source == nodeID || link.Target == nodeID {
			return true
		}
	}

	return false
}

func findNode(nodes []Node, id string) Node {
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}

	return Node{}
}

// splitExternalNodes assigns external nodes to left or right side of the diagram
// based on their connections to leaf switches
func splitExternalNodes(externals []Node, links []Link, leaves []Node) ([]Node, []Node) {
	leftExternals := []Node{}
	rightExternals := []Node{}

	// If only one external, default to right side unless it clearly connects to the left
	if len(externals) == 1 {
		leftCount, rightCount := 0, 0
		for _, link := range links {
			var leafID string

			switch {
			case link.Source == externals[0].ID:
				leafID = link.Target
			case link.Target == externals[0].ID:
				leafID = link.Source
			default:
				continue
			}

			// Check if connected to a leaf
			for i, leaf := range leaves {
				if leaf.ID == leafID {
					if i < len(leaves)/2 {
						leftCount++
					} else {
						rightCount++
					}

					break
				}
			}
		}

		if leftCount > rightCount {
			leftExternals = append(leftExternals, externals[0])
		} else {
			rightExternals = append(rightExternals, externals[0])
		}

		return leftExternals, rightExternals
	}

	// For 2 or more externals, distribute them more carefully
	leafIndexMap := make(map[string]int)
	for i, leaf := range leaves {
		leafIndexMap[leaf.ID] = i
	}

	midpoint := len(leaves) / 2

	// First, calculate connection weights for each external
	nodeWeights := make(map[string]struct{ left, right int })
	for _, node := range externals {
		var weights struct{ left, right int }

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

			// Check if this is a leaf connection
			idx, exists := leafIndexMap[leafID]
			if !exists {
				continue
			}

			// Count as left or right based on leaf position
			if idx < midpoint {
				weights.left++
			} else {
				weights.right++
			}
		}

		nodeWeights[node.ID] = weights
	}

	// Sort externals by ID to ensure deterministic output
	sortedExternals := make([]Node, len(externals))
	copy(sortedExternals, externals)
	sort.Slice(sortedExternals, func(i, j int) bool {
		return sortedExternals[i].ID < sortedExternals[j].ID
	})

	// Assign nodes to left or right based on connection weights
	for _, node := range sortedExternals {
		weights := nodeWeights[node.ID]

		switch {
		case weights.left > weights.right:
			leftExternals = append(leftExternals, node)
		case weights.right > weights.left:
			rightExternals = append(rightExternals, node)
		default:
			// If weights are equal, try to balance the sides
			if len(leftExternals) <= len(rightExternals) {
				leftExternals = append(leftExternals, node)
			} else {
				rightExternals = append(rightExternals, node)
			}
		}
	}

	return leftExternals, rightExternals
}
