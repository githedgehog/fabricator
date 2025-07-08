// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

import (
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

type Node struct {
	ID          string
	Type        string
	Label       string
	Properties  map[string]string
	Description string
}

type Link struct {
	Source     string
	Target     string
	Type       string
	Speed      string
	Count      int
	Properties map[string]string
}

type LinkGroup struct {
	Source string
	Target string
	Links  []Link
}

type Topology struct {
	Nodes []Node
	Links []Link
}

const (
	EdgeTypeFabric    = wiringapi.ConnectionTypeFabric
	EdgeTypeMCLAG     = wiringapi.ConnectionTypeMCLAG
	EdgeTypeSpine     = "spine" // TODO
	EdgeTypeBundled   = wiringapi.ConnectionTypeBundled
	EdgeTypeUnbundled = wiringapi.ConnectionTypeUnbundled
	EdgeTypeESLAG     = wiringapi.ConnectionTypeESLAG
	EdgeTypeGateway   = wiringapi.ConnectionTypeGateway
	NodeTypeSwitch    = "switch"
	NodeTypeServer    = "server"
	NodeTypeGateway   = "gateway"
	SwitchRoleSpine   = "spine"
	SwitchRoleLeaf    = "server-leaf"
)

const (
	DrawioFilename  = "diagram.drawio"
	DotFilename     = "diagram.dot"
	MermaidFilename = "diagram.mmd"
)

type TieredNodes struct {
	Spine   []Node
	Leaf    []Node
	Server  []Node
	Gateway []Node
}

type NodeMetrics struct {
	ConnectionCount   int
	AngleDistribution map[int]int
	ParentConnections []string
	ChildConnections  []string
}

const (
	PropMCLAGType   = "mclagType"
	PropSourcePort  = "sourcePort"
	PropTargetPort  = "targetPort"
	PropBundled     = "bundled"
	PropESLAGServer = "eslag_server"
	PropGateway     = "gateway"
	PropDescription = "description"
	PropRole        = "role"
)

const (
	MCLAGTypePeer    = "peer"
	MCLAGTypeSession = "session"
)

const (
	LegendKeyFabric       = "fabric"
	LegendKeyMCLAGPeer    = "mclag_peer"
	LegendKeyMCLAGSession = "mclag_session"
	LegendKeyMCLAGServer  = "mclag_server"
	LegendKeyBundled      = "bundled"
	LegendKeyUnbundled    = "unbundled"
	LegendKeyESLAGServer  = "eslag_server"
	LegendKeyGateway      = "gateway"
)

const (
	ConnTypeLeafToLeaf        = "leaf-to-leaf"
	ConnTypeSpineToLeaf       = "spine-to-leaf"
	ConnTypeLeafToSpine       = "leaf-to-spine"
	ConnTypeSwitchToSwitch    = "switch-to-switch"
	ConnTypeServerConnection  = "server-connection"
	ConnTypeGatewayConnection = "gateway-connection"
	ConnTypeUnknown           = "unknown"
)
