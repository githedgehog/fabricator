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
