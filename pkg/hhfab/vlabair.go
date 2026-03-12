// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	_ "embed"

	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

//go:embed vlabair_topo.tmpl.json
var vlabAirTopoTmpl string

//go:embed vlabair_setup_servers.tmpl.sh
var vlabAirSetupServersTmpl string

type AirTopoIn struct {
	Nodes []AirTopoNode
	Links []AirTopoLink
}

type AirTopoNode struct {
	Name    string
	NIC     string
	CPU     uint
	Memory  uint
	Storage uint
	OS      string
	IP      string
	MAC     string
}

type AirTopoLink struct {
	Endpoints []AirTopoLinkEndpoint
}

type AirTopoLinkEndpoint struct {
	Interface string
	Node      string
	MAC       string
}

type AirServersIn struct {
	Servers map[string]AirServersInServer
}

type AirServersInServer struct {
	Ifaces map[string]AirServersInServerIface
	Routes map[string]AirServersInServerRoute
}

type AirServersInServerIface struct {
	IP string
}

type AirServersInServerRoute struct {
	NextHops []string
}

func (c *Config) AirGenerate(ctx context.Context) error {
	if len(c.Controls) != 1 {
		return fmt.Errorf("exactly one control node is required") //nolint:err113
	}
	if len(c.Nodes) > 0 {
		return fmt.Errorf("nodes are not supported yet") //nolint:err113
	}
	if c.Fab.Spec.Config.Control.ManagementSubnet != "192.168.200.0/24" {
		return fmt.Errorf("unsupported management subnet: %s", c.Fab.Spec.Config.Control.ManagementSubnet) //nolint:err113
	}

	resultDir := filepath.Join(c.WorkDir, ResultDir, "air")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		return fmt.Errorf("creating result dir: %w", err)
	}

	// TODO cache template
	cfgTmpl, err := template.New("air_topo").Parse(vlabAirTopoTmpl)
	if err != nil {
		return fmt.Errorf("parsing topology template: %w", err)
	}

	topoIn := AirTopoIn{
		Nodes: []AirTopoNode{},
		Links: []AirTopoLink{},
	}

	serversIn := AirServersIn{
		Servers: map[string]AirServersInServer{},
	}

	staticIPs := map[string]string{}

	sws := &wiringapi.SwitchList{}
	if err := c.Client.List(ctx, sws); err != nil {
		return fmt.Errorf("listing switches: %w", err)
	}
	slices.SortFunc(sws.Items, func(a, b wiringapi.Switch) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, sw := range sws.Items {
		if sw.Spec.Profile != meta.SwitchProfileCmlsVX {
			return fmt.Errorf("unsupported switch profile: %s", sw.Spec.Profile) //nolint:err113
		}

		ip := strings.TrimSuffix(sw.Spec.IP, "/24")
		staticIPs[sw.Spec.Boot.MAC] = ip
		topoIn.Nodes = append(topoIn.Nodes, AirTopoNode{
			Name:    sw.Name,
			NIC:     "virtio",
			CPU:     2,
			Memory:  4096,
			Storage: 10,
			OS:      "cumulus-vx-5.15.0",
			IP:      ip,
			MAC:     sw.Spec.Boot.MAC,
		})
	}

	macID := 0
	nextMAC := func() string {
		mac := fmt.Sprintf(VLABMACTmpl, 1+macID/256, macID%256)
		macID++

		return mac
	}

	translatePort := func(in string) (string, string) {
		parts := strings.SplitN(in, "/", 2)
		if len(parts) != 2 {
			return "<invalid>", "<invalid>"
		}

		if port, ok := strings.CutPrefix(parts[1], "E1/"); ok {
			parts[1] = "swp" + port
		}

		// TODO replace with port base?
		if port, ok := strings.CutPrefix(parts[1], "enp2s"); ok {
			parts[1] = "eth" + port
		}

		return parts[0], parts[1]
	}

	attaches := &vpcapi.VPCAttachmentList{}
	if err := c.Client.List(ctx, attaches); err != nil {
		return fmt.Errorf("listing vpc attachments: %w", err)
	}
	connList := &wiringapi.ConnectionList{}
	if err := c.Client.List(ctx, connList); err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}
	conns := map[string]wiringapi.Connection{}
	for _, conn := range connList.Items {
		conns[conn.Name] = conn
	}

	servers := &wiringapi.ServerList{}
	if err := c.Client.List(ctx, servers); err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}
	slices.SortFunc(servers.Items, func(a, b wiringapi.Server) int {
		return strings.Compare(a.Name, b.Name)
	})
	for idx, server := range servers.Items {
		ip := fmt.Sprintf("192.168.200.%d", 100+idx)
		mac := nextMAC()
		staticIPs[mac] = ip
		topoIn.Nodes = append(topoIn.Nodes, AirTopoNode{
			Name:    server.Name,
			NIC:     "e1000",
			CPU:     1,
			Memory:  1024,
			Storage: 10,
			OS:      "generic/ubuntu2404",
			IP:      ip,
			MAC:     mac,
		})

		inServer := AirServersInServer{
			Ifaces: map[string]AirServersInServerIface{},
			Routes: map[string]AirServersInServerRoute{},
		}

		for _, attach := range attaches.Items {
			conn, ok := conns[attach.Spec.Connection]
			if !ok {
				return fmt.Errorf("connection not found for attachment %s", attach.Name) //nolint:err113
			}
			if conn.Spec.Unbundled == nil {
				return fmt.Errorf("VPCAttachment %s to non-unbundled connection %s", attach.Name, conn.Name) //nolint:err113
			}

			l := conn.Spec.Unbundled.Link
			device, port := translatePort(l.Server.Port)
			if device != server.Name {
				continue
			}

			p2pStr := attach.Annotations[vpcapi.AnnotationVPCAttachmentP2PLink]
			if p2pStr == "" {
				return fmt.Errorf("VPCAttachment %s missing p2p annotation", attach.Name) //nolint:err113
			}

			p2p, err := netip.ParsePrefix(p2pStr)
			if err != nil {
				return fmt.Errorf("VPCAttachment %s invalid p2p annotation: %w", attach.Name, err)
			}
			localIP := netip.PrefixFrom(p2p.Masked().Addr(), 31).String()
			swIP := p2p.Masked().Addr().Next().String()

			inServer.Ifaces[port] = AirServersInServerIface{
				IP: localIP,
			}

			// TODO limit to the VPC subnet?
			nsRoute, ok := inServer.Routes["10.0.0.0/8"]
			if !ok {
				inServer.Routes["10.0.0.0/8"] = AirServersInServerRoute{}
			}
			nsRoute.NextHops = append(nsRoute.NextHops, swIP)
			inServer.Routes["10.0.0.0/8"] = nsRoute

			// by convention we expect that the whole /24 is dedicated for a rail
			b := p2p.Masked().Addr().As4()
			b[3] = 0
			railPrefix := netip.PrefixFrom(netip.AddrFrom4(b), 24).String()
			inServer.Routes[railPrefix] = AirServersInServerRoute{
				NextHops: []string{swIP},
			}
		}

		serversIn.Servers[server.Name] = inServer
	}

	for _, conn := range connList.Items {
		_, _, _, links, err := conn.Spec.Endpoints()
		if err != nil {
			return fmt.Errorf("parsing connection endpoints: %w", err)
		}
		for l1, l2 := range links {
			d1, p1 := translatePort(l1)
			d2, p2 := translatePort(l2)

			topoIn.Links = append(topoIn.Links, AirTopoLink{
				Endpoints: []AirTopoLinkEndpoint{
					{
						Interface: p1,
						Node:      d1,
						MAC:       nextMAC(),
					},
					{
						Interface: p2,
						Node:      d2,
						MAC:       nextMAC(),
					},
				},
			})
		}
	}

	topoBuf := &bytes.Buffer{}
	if err := cfgTmpl.Execute(topoBuf, topoIn); err != nil {
		return fmt.Errorf("executing topology template: %w", err)
	}

	if err := os.WriteFile(filepath.Join(resultDir, "topology.json"), topoBuf.Bytes(), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing topology file: %w", err)
	}

	c.Controls[0].Spec.External.Interface = "eth0"
	c.Controls[0].Spec.Management.Interface = "eth1"
	c.Fab.Spec.Config.Control.ManagementSubnetAnyDevice = true
	c.Fab.Spec.Config.Control.ManagementSubnetStatic = staticIPs
	c.Fab.Spec.Config.Registry = fabapi.RegistryConfig{
		Mode: fabapi.RegistryModeUpstream,
		Upstream: &fabapi.ControlConfigRegistryUpstream{
			Repo:   "ghcr.io",
			Prefix: "githedgehog",
		},
	}

	cfgBug := &bytes.Buffer{}
	if err := apiutil.PrintKubeObject(&c.Fab, c.Client.Scheme(), cfgBug, false); err != nil {
		return fmt.Errorf("printing fabricator: %w", err)
	}
	for _, control := range c.Controls {
		_, err := fmt.Fprintf(cfgBug, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := apiutil.PrintKubeObject(&control, c.Client.Scheme(), cfgBug, false); err != nil {
			return fmt.Errorf("printing control node: %w", err)
		}
	}
	for _, n := range c.Nodes {
		_, err := fmt.Fprintf(cfgBug, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := apiutil.PrintKubeObject(&n, c.Client.Scheme(), cfgBug, false); err != nil {
			return fmt.Errorf("printing node: %w", err)
		}
	}

	if err := os.WriteFile(filepath.Join(resultDir, "config.yaml"), cfgBug.Bytes(), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing config file: %w", err)
	}

	// TODO cache template
	setupServersTmpl, err := template.New("air_setup_server").Parse(vlabAirSetupServersTmpl)
	if err != nil {
		return fmt.Errorf("parsing setup servers template: %w", err)
	}

	setupServersBuf := &bytes.Buffer{}
	if err := setupServersTmpl.Execute(setupServersBuf, serversIn); err != nil {
		return fmt.Errorf("executing setup servers template: %w", err)
	}

	if err := os.WriteFile(filepath.Join(resultDir, "setup_servers.sh"), setupServersBuf.Bytes(), 0o755); err != nil { //nolint:gosec
		return fmt.Errorf("writing setup servers file: %w", err)
	}

	includeBuf := &bytes.Buffer{}
	if err := apiutil.PrintInclude(ctx, c.Client, includeBuf); err != nil {
		return fmt.Errorf("printing include: %w", err)
	}

	if err := os.WriteFile(filepath.Join(resultDir, "include.yaml"), includeBuf.Bytes(), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing include file: %w", err)
	}

	return nil
}
