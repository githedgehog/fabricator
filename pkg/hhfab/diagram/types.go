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
	EdgeTypeFabric    = "fabric"
	EdgeTypeMCLAG     = "mclag"
	EdgeTypeSpine     = "spine"
	EdgeTypeBundled   = "bundled"
	EdgeTypeUnbundled = "unbundled"
	EdgeTypeESLAG     = "eslag"
	EdgeTypeGateway   = "gateway"
	NodeTypeSwitch    = "switch"
	NodeTypeServer    = "server"
	NodeTypeGateway   = "gateway"
	SwitchRoleSpine   = "spine"
	SwitchRoleLeaf    = "leaf"
)
