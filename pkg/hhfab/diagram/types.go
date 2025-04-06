// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package diagram

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
	EdgeTypeFabric     = "fabric"
	EdgeTypeMCLAG      = "mclag"
	EdgeTypeSpine      = "spine"
	EdgeTypeBundled    = "bundled"
	EdgeTypeUnbundled  = "unbundled"
	EdgeTypeESLAG      = "eslag"
	EdgeTypeGateway    = "gateway"
	EdgeTypeExternal   = "external"
	NodeTypeSwitch     = "switch"
	NodeTypeServer     = "server"
	NodeTypeGateway    = "gateway"
	NodeTypeExternal   = "external"
	SwitchRoleSpine    = "spine"
	SwitchRoleLeaf     = "leaf"
	SwitchRoleExternal = "external"
)

const (
	DrawioFilename  = "diagram.drawio"
	DotFilename     = "diagram.dot"
	MermaidFilename = "diagram.mmd"
)

const (
	BaseLayerID        = "base_layer"
	ConnectionsLayerID = "connections_layer"
)

const (
	BaseLayerName        = "Base (Nodes & Switches)"
	ConnectionsLayerName = "Fabric Connections"
)
