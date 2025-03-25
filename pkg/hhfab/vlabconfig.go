// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"dario.cat/mergo"
	"github.com/charmbracelet/keygen"
	fmeta "go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	VLABDir        = "vlab"
	VLABConfigFile = "config.yaml"
	VLABSSHKeyFile = "sshkey"
	VLABVMsDir     = "vms"

	VLABSwitchMACTmpl = "0c:20:12:ff:%02x:00"
	VLABMACTmpl       = "0c:20:12:fe:%02x:%02x"

	HHFabCfgPrefix     = ".hhfab.githedgehog.com"
	HHFabCfgType       = "type" + HHFabCfgPrefix
	HHFabCfgTypeHW     = "hw"
	HHFabCfgLinkPrefix = "link" + HHFabCfgPrefix + "/"
	HHFabCfgPCIPrefix  = "pci@"
	HHFabCfgASNPrefix  = "asn"
	// HHFabCfgSerial          = "serial" + HHFabCfgPrefix
	// HHFabCfgSerialSchemeSSH = "ssh://"

	ExternalVMName   = "external"
	MaxExternalConns = 48
)

const (
	VLABPCIBridgePrefix  = "pcibr"
	VLABNICsPerPCIBridge = 32
	VLABPCIBridges       = 2
	VLABMaxNICs          = VLABNICsPerPCIBridge * VLABPCIBridges
	VLABBaseSSHPort      = 22000
	VLABBaseDirectPort   = 22100
	VLABTapPrefix        = "hhtap"
	VLABBridge           = "hhbr"
	VLABUUIDPrefix       = "77924ab4-a93b-41d4-928e-"
	VLABUUIDTmpl         = VLABUUIDPrefix + "%012d"
)

type VLAB struct {
	SSHKey       string
	VMs          []VM
	Taps         int
	Passthroughs []string
	Externals    ExternalsCfg
}

type VM struct {
	ID         uint
	Name       string
	Type       VMType
	Restricted bool
	NICs       []string
	Size       VMSize
}

type ExternalAttachCfg struct {
	Prefix     string // IP prefix to configure on the NIC
	NeighborIP string // IP of the BGP neighbor
	NeighborAS string // AS of the BGP neighbor
	Vlan       uint16 // VLAN ID to configure on the NIC, 0 is untagged
	VRF        string // VRF name to configure on the NIC (name of the external)
}

type ExternalNICCfg struct {
	Attachments []ExternalAttachCfg // the external attachments for this NIC, one per VLAN
	Untagged    bool                // whether there's an untagged attachment. Separate to simplify ignition template
	UntaggedCfg ExternalAttachCfg   // if untagged, the configuration for the untagged attachment
}

type ExternalVRFCfg struct {
	TableID      uint32
	ASN          uint32
	InCommunity  string
	OutCommunity string
}

type ExternalsCfg struct {
	NICs    map[string]ExternalNICCfg // the key is the external connection NIC name
	VRFs    map[string]ExternalVRFCfg // a VRF associated with an external. The key is the external name
	Invalid bool                      // if true, the external VRF configuration is invalid

}

type VLABConfig struct {
	SSHKey    string              `json:"-"`
	Sizes     VMSizes             `json:"sizes"`
	VMs       map[string]VMConfig `json:"vms"`
	Externals ExternalsCfg        `json:"externals"`
}

type VMSizes struct {
	Control  VMSize `json:"control"`
	Switch   VMSize `json:"switch"`
	Server   VMSize `json:"server"`
	Gateway  VMSize `json:"gateway"`
	External VMSize `json:"external"`
}

type VMConfig struct {
	Type VMType            `json:"type"`
	NICs map[string]string `json:"nics"`
}

type VMType string

const (
	VMTypeControl  VMType = "control"
	VMTypeSwitch   VMType = "switch"
	VMTypeServer   VMType = "server"
	VMTypeGateway  VMType = "gateway"
	VMTypeExternal VMType = "external"
)

var VMTypes = []VMType{
	VMTypeControl,
	VMTypeSwitch,
	VMTypeServer,
	VMTypeGateway,
	VMTypeExternal,
}

const (
	NICTypeSep         = ":"
	NICTypeNoop        = "noop"
	NICTypeUsernet     = "usernet"
	NICTypeManagement  = "management"
	NICTypeDirect      = "direct"
	NICTypePassthrough = "passthrough"
)

var NICTypes = []string{
	NICTypeNoop,
	NICTypeUsernet,
	NICTypeManagement,
	NICTypeDirect,
	NICTypePassthrough,
}

type VMSize struct {
	CPU  uint `json:"cpu"`  // in cores
	RAM  uint `json:"ram"`  // in MB
	Disk uint `json:"disk"` // in GB
}

var DefaultSizes = VMSizes{
	Control:  VMSize{CPU: 6, RAM: 6144, Disk: 100}, // TODO 8GB RAM?
	Switch:   VMSize{CPU: 4, RAM: 5120, Disk: 50},  // TODO 6GB RAM?
	Server:   VMSize{CPU: 2, RAM: 768, Disk: 10},   // TODO 1GB RAM?
	Gateway:  VMSize{CPU: 8, RAM: 6144, Disk: 50},  // TODO proper min size
	External: VMSize{CPU: 4, RAM: 5120, Disk: 50},  // TODO 6GB RAM?
}

func (c *Config) PrepareVLAB(ctx context.Context, opts VLABUpOpts) (*VLAB, error) {
	vlabDir := filepath.Join(c.WorkDir, VLABDir)
	vlabVMsDir := filepath.Join(vlabDir, VLABVMsDir)

	if stat, err := os.Stat(vlabDir); err != nil {
		if os.IsNotExist(err) {
			if opts.NoCreate {
				return nil, fmt.Errorf("VLAB directory does not exist: %q", vlabDir) //nolint:goerr113
			}
			if err := os.Mkdir(vlabDir, 0o700); err != nil {
				return nil, fmt.Errorf("creating VLAB directory: %w", err) //nolint:goerr113
			}
		} else {
			return nil, fmt.Errorf("checking VLAB directory: %w", err)
		}
	} else if !stat.IsDir() {
		return nil, fmt.Errorf("VLAB directory is not a directory: %q", vlabDir) //nolint:goerr113
	}

	createCfg := opts.ReCreate
	vlabCfgFile := filepath.Join(vlabDir, VLABConfigFile)
	if _, err := os.Stat(vlabCfgFile); err != nil {
		if os.IsNotExist(err) {
			if opts.NoCreate {
				return nil, fmt.Errorf("VLAB config file does not exist: %q", vlabCfgFile) //nolint:goerr113
			}
			createCfg = true
		} else {
			return nil, fmt.Errorf("checking VLAB config file: %w", err)
		}
	}

	if createCfg {
		if err := os.RemoveAll(vlabDir); err != nil {
			return nil, fmt.Errorf("removing VLAB directory: %w", err)
		}

		if err := os.MkdirAll(vlabVMsDir, 0o700); err != nil {
			return nil, fmt.Errorf("creating VLAB directories: %w", err)
		}

		vlabCfg, err := createVLABConfig(ctx, c.Controls, c.Nodes, c.Wiring)
		if err != nil {
			return nil, fmt.Errorf("creating VLAB config: %w", err)
		}

		data, err := yaml.Marshal(vlabCfg)
		if err != nil {
			return nil, fmt.Errorf("marshaling VLAB config: %w", err)
		}

		if err := os.WriteFile(vlabCfgFile, data, 0o600); err != nil {
			return nil, fmt.Errorf("writing VLAB config file: %w", err)
		}

		slog.Info("VLAB config created", "file", filepath.Join(VLABDir, VLABConfigFile))
	}

	// TODO optionally patch control node(s) with their IP/etc instead of DHCP by default

	data, err := os.ReadFile(vlabCfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading VLAB config file: %w", err)
	}

	vlabCfg := &VLABConfig{}
	if err := yaml.UnmarshalStrict(data, vlabCfg); err != nil {
		return nil, fmt.Errorf("unmarshaling VLAB config: %w", err)
	}

	if err := mergo.Merge(&vlabCfg.Sizes, DefaultSizes); err != nil {
		return nil, fmt.Errorf("merging VLAB sizes: %w", err)
	}

	for name, vm := range vlabCfg.VMs {
		if !slices.Contains(VMTypes, vm.Type) {
			return nil, fmt.Errorf("invalid VM type %q for VM %q", vm.Type, name) //nolint:goerr113
		}

		for nicName, nicConfig := range vm.NICs {
			if _, err := getNICID(nicName); err != nil {
				return nil, fmt.Errorf("getting NIC ID for %q: %w", nicName, err)
			}

			nicType := strings.SplitN(nicConfig, NICTypeSep, 2)[0]
			if !slices.Contains(NICTypes, nicType) {
				return nil, fmt.Errorf("invalid NIC type %q for NIC %q of VM %q", nicType, nicName, name) //nolint:goerr113
			}

			// TODO some more validation
		}
	}

	if vlabCfg.Externals.Invalid && opts.SpawnExternal {
		return nil, fmt.Errorf("could not derive external VRF configuration from wiring, disable external spawning or check wiring") //nolint:goerr113
	}

	sshKeyPath := filepath.Join(vlabDir, VLABSSHKeyFile)
	pub, prv, err := getOrCreateSSHKey(sshKeyPath)
	if err != nil {
		return nil, fmt.Errorf("getting or creating SSH key: %w", err)
	}
	vlabCfg.SSHKey = prv

	c.Fab.Spec.Config.Control.DefaultUser.AuthorizedKeys = append(c.Fab.Spec.Config.Control.DefaultUser.AuthorizedKeys, pub)

	for username, user := range c.Fab.Spec.Config.Fabric.DefaultSwitchUsers {
		user.AuthorizedKeys = append(user.AuthorizedKeys, pub)
		c.Fab.Spec.Config.Fabric.DefaultSwitchUsers[username] = user
	}

	for idx := range c.Controls {
		if !isHardware(&c.Controls[idx]) {
			c.Controls[idx].Spec.Bootstrap.Disk = "/dev/vda"
		}
	}

	if !createCfg {
		slog.Info("VLAB config loaded", "file", filepath.Join(VLABDir, VLABConfigFile))
	}

	vlab, err := vlabFromConfig(vlabCfg, opts.VLABRunOpts)
	if err != nil {
		return nil, fmt.Errorf("creating VLAB: %w", err)
	}

	return vlab, nil
}

func createVLABConfig(ctx context.Context, controls []fabapi.ControlNode, nodes []fabapi.Node, wiring client.Reader) (*VLABConfig, error) {
	cfg := &VLABConfig{
		Sizes: DefaultSizes,
		VMs:   map[string]VMConfig{},
		Externals: ExternalsCfg{
			NICs:    make(map[string]ExternalNICCfg),
			VRFs:    make(map[string]ExternalVRFCfg),
			Invalid: false,
		},
	}

	hw := map[string]bool{}
	passthrough := map[string]string{}
	usedPassthroughs := map[string]bool{}

	addPassthroughLinks := func(obj client.Object) (map[string]string, error) {
		links := getPassthroughLinks(obj)

		for k, v := range links {
			if _, exist := usedPassthroughs[v]; exist {
				return nil, fmt.Errorf("duplicate pci address: %q", v) //nolint:goerr113
			}
			usedPassthroughs[v] = true

			if _, exist := passthrough[k]; exist {
				return nil, fmt.Errorf("duplicate passthrough link: %q", k) //nolint:goerr113
			}

			passthrough[k] = v
		}

		return links, nil
	}

	for _, control := range controls {
		if _, exists := cfg.VMs[control.Name]; exists {
			return nil, fmt.Errorf("duplicate VM name (control): %q", control.Name) //nolint:goerr113
		}

		links, err := addPassthroughLinks(&control)
		if err != nil {
			return nil, fmt.Errorf("failed to add passthrough links for control %q: %w", control.Name, err)
		}

		mgmt := NICTypeManagement
		if pci := links[control.Name+"/enp2s1"]; pci != "" {
			mgmt = NICTypePassthrough + NICTypeSep + pci
		}

		delete(links, control.Name+"/enp2s1")

		if len(links) > 0 {
			return nil, fmt.Errorf("unexpected passthrough links for control %q: %v", control.Name, links) //nolint:goerr113
		}

		if isHardware(&control) {
			return nil, fmt.Errorf("control VM %q can't be hardware", control.Name) //nolint:goerr113
		}

		cfg.VMs[control.Name] = VMConfig{
			Type: VMTypeControl,
			NICs: map[string]string{
				"enp2s0": NICTypeUsernet,
				"enp2s1": mgmt,
			},
		}
	}

	// TODO deduplicate
	for _, node := range nodes {
		if len(node.Spec.Roles) != 1 || node.Spec.Roles[0] != fabapi.NodeRoleGateway {
			return nil, fmt.Errorf("node %q isn't a gateway role", node.Name) //nolint:goerr113
		}

		if _, exists := cfg.VMs[node.Name]; exists {
			return nil, fmt.Errorf("duplicate VM name (node): %q", node.Name) //nolint:goerr113
		}

		links, err := addPassthroughLinks(&node)
		if err != nil {
			return nil, fmt.Errorf("failed to add passthrough links for node %q: %w", node.Name, err)
		}

		mgmt := NICTypeManagement
		if pci := links[node.Name+"/enp2s0"]; pci != "" {
			mgmt = NICTypePassthrough + NICTypeSep + pci
		}

		delete(links, node.Name+"/enp2s0")

		if len(links) > 0 {
			return nil, fmt.Errorf("unexpected passthrough links for node %q: %v", node.Name, links) //nolint:goerr113
		}

		if isHardware(&node) {
			return nil, fmt.Errorf("node VM %q can't be hardware", node.Name) //nolint:goerr113
		}

		cfg.VMs[node.Name] = VMConfig{
			Type: VMTypeGateway,
			NICs: map[string]string{
				"enp2s0": mgmt,
			},
		}
	}

	servers := &wiringapi.ServerList{}
	if err := wiring.List(ctx, servers); err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}
	slices.SortFunc(servers.Items, func(a, b wiringapi.Server) int {
		return cmp.Compare(a.Name, b.Name)
	})

	for _, server := range servers.Items {
		if _, exists := cfg.VMs[server.Name]; exists {
			return nil, fmt.Errorf("duplicate VM name (server): %q", server.Name) //nolint:goerr113
		}

		if _, err := addPassthroughLinks(&server); err != nil {
			return nil, fmt.Errorf("failed to add passthrough links for server %q: %w", server.Name, err)
		}

		if isHardware(&server) {
			hw[server.Name] = true

			continue
		}

		cfg.VMs[server.Name] = VMConfig{
			Type: VMTypeServer,
			NICs: map[string]string{
				"enp2s0": NICTypeUsernet,
			},
		}
	}

	externals := &vpcapi.ExternalList{}
	if err := wiring.List(ctx, externals); err != nil {
		return nil, fmt.Errorf("failed to list externals: %w", err)
	}
	if len(externals.Items) > 0 {
		if _, exists := cfg.VMs[ExternalVMName]; exists {
			return nil, fmt.Errorf("VM name %s is reserved for externals", ExternalVMName) //nolint:goerr113
		}

		cfg.VMs[ExternalVMName] = VMConfig{
			Type: VMTypeExternal,
			NICs: map[string]string{
				"enp2s0": NICTypeUsernet,
			},
		}
		tableID := uint32(1000)
		for _, external := range externals.Items {
			if _, exists := cfg.Externals.VRFs[external.Name]; exists {
				return nil, fmt.Errorf("duplicate external VRF name: %q", external.Name) //nolint:goerr113
			}
			asn := getAsn(&external)
			if asn == 0 {
				slog.Debug("external has no ASN annotation, will attempt to fetch it from the external attachments", "name", external.Name)
			}
			cfg.Externals.VRFs[external.Name] = ExternalVRFCfg{
				TableID: tableID,
				ASN:     asn,
				// Invert inbound and outbound communities
				InCommunity:  external.Spec.OutboundCommunity,
				OutCommunity: external.Spec.InboundCommunity,
			}
		}
	}

	switches := &wiringapi.SwitchList{}
	if err := wiring.List(ctx, switches); err != nil {
		return nil, fmt.Errorf("failed to list switches: %w", err)
	}
	slices.SortFunc(switches.Items, func(a, b wiringapi.Switch) int {
		return cmp.Compare(a.Name, b.Name)
	})

	for _, sw := range switches.Items {
		if _, exists := cfg.VMs[sw.Name]; exists {
			return nil, fmt.Errorf("duplicate VM name (switch): %q", sw.Name) //nolint:goerr113
		}

		links, err := addPassthroughLinks(&sw)
		if err != nil {
			return nil, fmt.Errorf("failed to add passthrough links for switch %q: %w", sw.Name, err)
		}

		if isHardware(&sw) {
			hw[sw.Name] = true

			continue
		}

		if sw.Spec.Profile != fmeta.SwitchProfileVS {
			return nil, fmt.Errorf("switch %q has unsupported profile: %q", sw.Name, sw.Spec.Profile) //nolint:goerr113
		}

		if sw.Spec.Boot.MAC == "" {
			return nil, fmt.Errorf("switch %q has no MAC", sw.Name) //nolint:goerr113
		}

		mgmt := NICTypeManagement + NICTypeSep + sw.Spec.Boot.MAC
		if pci := links[sw.Name+"/M1"]; pci != "" {
			mgmt = NICTypePassthrough + NICTypeSep + pci
		}

		cfg.VMs[sw.Name] = VMConfig{
			Type: VMTypeSwitch,
			NICs: map[string]string{
				"M1": mgmt,
			},
		}
	}

	conns := &wiringapi.ConnectionList{}
	if err := wiring.List(ctx, conns); err != nil {
		return nil, fmt.Errorf("failed to list connections: %w", err)
	}
	slices.SortFunc(conns.Items, func(a, b wiringapi.Connection) int {
		return cmp.Compare(a.Name, b.Name)
	})

	addLink := func(from, to string) error {
		fromParts := strings.SplitN(from, "/", 2)
		fromName, fromNIC := fromParts[0], fromParts[1]

		toParts := strings.SplitN(to, "/", 2)
		toName, toNIC := toParts[0], toParts[1]

		fromHW, toHW := hw[fromName], hw[toName]
		if fromHW && toHW {
			return nil
		}

		fromVM, fromVMExist := cfg.VMs[fromName]
		toVM, toVMExist := cfg.VMs[toName]

		if !fromHW && !fromVMExist {
			return fmt.Errorf("VM %s not found", fromName) //nolint:goerr113
		}
		if !toHW && !toVMExist {
			return fmt.Errorf("VM %s not found", toName) //nolint:goerr113
		}

		if fromVM.Type == VMTypeControl || toVM.Type == VMTypeControl {
			return fmt.Errorf("control VMs can't have links from wiring") //nolint:goerr113
		}

		if !fromHW && !toHW {
			if _, exist := fromVM.NICs[fromNIC]; exist {
				return fmt.Errorf("NIC %s/%s is already in use", fromName, fromNIC) //nolint:goerr113
			}
			if _, exist := toVM.NICs[toNIC]; exist {
				return fmt.Errorf("NIC %s/%s is already in use", toName, toNIC) //nolint:goerr113
			}

			fromVM.NICs[fromNIC] = NICTypeDirect + NICTypeSep + to
			toVM.NICs[toNIC] = NICTypeDirect + NICTypeSep + from
		} else if fromHW {
			pci, exist := passthrough[from]
			if !exist {
				return fmt.Errorf("missing passthrough link for %s", from) //nolint:goerr113
			}

			toVM.NICs[toNIC] = NICTypePassthrough + NICTypeSep + pci
		} else if toHW {
			pci, exist := passthrough[to]
			if !exist {
				return fmt.Errorf("missing passthrough link for %s", to) //nolint:goerr113
			}

			fromVM.NICs[fromNIC] = NICTypePassthrough + NICTypeSep + pci
		}

		return nil
	}

	externalID := 1
	externalAttachs := &vpcapi.ExternalAttachmentList{}
	if err := wiring.List(ctx, externalAttachs); err != nil {
		return nil, fmt.Errorf("failed to list external attachments: %w", err)
	}

	for _, conn := range conns.Items {
		if conn.Spec.Fabric != nil {
			for _, link := range conn.Spec.Fabric.Links {
				if err := addLink(link.Spine.Port, link.Leaf.Port); err != nil {
					return nil, fmt.Errorf("failed to add link for fabric connection %s: %w", conn.Name, err)
				}
			}
		} else if conn.Spec.Gateway != nil {
			for _, link := range conn.Spec.Gateway.Links {
				if err := addLink(link.Spine.Port, link.Gateway.Port); err != nil {
					return nil, fmt.Errorf("failed to add link for gateway connection %s: %w", conn.Name, err)
				}
			}
		} else if conn.Spec.Unbundled != nil {
			if err := addLink(conn.Spec.Unbundled.Link.Server.Port, conn.Spec.Unbundled.Link.Switch.Port); err != nil {
				return nil, fmt.Errorf("failed to add link for unbundled connection %s: %w", conn.Name, err)
			}
		} else if conn.Spec.Bundled != nil {
			for _, link := range conn.Spec.Bundled.Links {
				if err := addLink(link.Server.Port, link.Switch.Port); err != nil {
					return nil, fmt.Errorf("failed to add link for bundled connection %s: %w", conn.Name, err)
				}
			}
		} else if conn.Spec.MCLAG != nil {
			for _, link := range conn.Spec.MCLAG.Links {
				if err := addLink(link.Server.Port, link.Switch.Port); err != nil {
					return nil, fmt.Errorf("failed to add link for MCLAG connection %s: %w", conn.Name, err)
				}
			}
		} else if conn.Spec.ESLAG != nil {
			for _, link := range conn.Spec.ESLAG.Links {
				if err := addLink(link.Server.Port, link.Switch.Port); err != nil {
					return nil, fmt.Errorf("failed to add link for ESLAG connection %s: %w", conn.Name, err)
				}
			}
		} else if conn.Spec.VPCLoopback != nil {
			for _, link := range conn.Spec.VPCLoopback.Links {
				if err := addLink(link.Switch1.Port, link.Switch2.Port); err != nil {
					return nil, fmt.Errorf("failed to add link for VPC loopback connection %s: %w", conn.Name, err)
				}
			}
		} else if conn.Spec.MCLAGDomain != nil {
			for _, link := range conn.Spec.MCLAGDomain.SessionLinks {
				if err := addLink(link.Switch1.Port, link.Switch2.Port); err != nil {
					return nil, fmt.Errorf("failed to add session link for MCLAG domain connection %s: %w", conn.Name, err)
				}
			}
			for _, link := range conn.Spec.MCLAGDomain.PeerLinks {
				if err := addLink(link.Switch1.Port, link.Switch2.Port); err != nil {
					return nil, fmt.Errorf("failed to add peer link for MCLAG domain connection %s: %w", conn.Name, err)
				}
			}
		} else if conn.Spec.External != nil && !cfg.Externals.Invalid {
			if externalID > MaxExternalConns {
				slog.Warn("too many external connections", "max-external-connections", MaxExternalConns)
				cfg.Externals.Invalid = true

				continue
			}

			switchName := conn.Spec.External.Link.Switch.DeviceName()
			// add the link representing the external connection
			nicName := fmt.Sprintf("enp2s%d", externalID)
			externalID++
			toStr := fmt.Sprintf("%s/%s", ExternalVMName, nicName)
			if err := addLink(conn.Spec.External.Link.Switch.Port, toStr); err != nil {
				slog.Warn("failed to add link for external connection", "connection", conn.Name, "error", err)
				cfg.Externals.Invalid = true

				continue
			}
			extNicCfg := ExternalNICCfg{
				Attachments: make([]ExternalAttachCfg, 0),
				Untagged:    false,
			}

			// check if there is any external attachment using this connection
			for _, extAttach := range externalAttachs.Items {
				if extAttach.Spec.Connection != conn.Name {
					continue
				}
				extName := extAttach.Spec.External
				extVrf, ok := cfg.Externals.VRFs[extName]
				if !ok {
					slog.Warn("external attachment has no associated VRF for the external", "attachment", extAttach.Name, "external", extName)
					cfg.Externals.Invalid = true

					break
				}
				if extVrf.ASN == 0 {
					slog.Debug("Setting ASN for external", "external", extName, "attachment", extAttach.Name, "ASN", extAttach.Spec.Neighbor.ASN)
					extVrf.ASN = extAttach.Spec.Neighbor.ASN
					cfg.Externals.VRFs[extName] = extVrf
				} else if extVrf.ASN != extAttach.Spec.Neighbor.ASN {
					slog.Warn("external attachment reports inconsistent ASN", "external", extName, "attachment", extAttach.Name, "ASN", extAttach.Spec.Neighbor.ASN, "previously known ASN", extVrf.ASN)
					cfg.Externals.Invalid = true

					break
				}
				// Fetch the switch to retrieve its ASN
				sw := &wiringapi.Switch{}
				if err := wiring.Get(ctx, client.ObjectKey{Name: switchName, Namespace: conn.Namespace}, sw); err != nil {
					slog.Warn("failed to get switch", "switch", switchName, "error", err)
					cfg.Externals.Invalid = true

					break
				}

				// We get the external IP as the "neighbor IP" in the wiring, but we need to
				// convert it to a prefix. Conversely, the fabric "switch IP" is given as a prefix
				// and we need to convert it to an address for the BGP neighbor commands.
				// So we take the prefix length of the switch IP and apply it to the external IP.
				fabSwitchPrefix := netip.MustParsePrefix(extAttach.Spec.Switch.IP)
				extAddr := netip.MustParseAddr(extAttach.Spec.Neighbor.IP)
				extPrefix := netip.PrefixFrom(extAddr, fabSwitchPrefix.Bits())
				attachCfg := ExternalAttachCfg{
					Prefix:     extPrefix.String(),
					NeighborIP: fabSwitchPrefix.Addr().String(),
					NeighborAS: strconv.FormatUint(uint64(sw.Spec.ASN), 10),
					Vlan:       extAttach.Spec.Switch.VLAN,
					VRF:        extName,
				}
				if extAttach.Spec.Switch.VLAN == 0 {
					if extNicCfg.Untagged {
						slog.Warn("multiple untagged attachments for the same external NIC", "attachment", extAttach.Name, "external", extName)
						cfg.Externals.Invalid = true

						break
					}
					extNicCfg.Untagged = true
					extNicCfg.UntaggedCfg = attachCfg
				} else {
					extNicCfg.Attachments = append(extNicCfg.Attachments, attachCfg)
				}
				slog.Info("Added attachment to external",
					"NIC name",
					nicName,
					"external",
					extName,
					"switch",
					switchName,
					"VLAN",
					attachCfg.Vlan,
					"switch IP",
					attachCfg.NeighborIP,
					"switch ASN",
					attachCfg.NeighborAS,
					"external Prefix",
					attachCfg.Prefix,
					"external ASN",
					extVrf.ASN,
				)
			}
			cfg.Externals.NICs[nicName] = extNicCfg
		}
	}

	return cfg, nil
}

func vlabFromConfig(cfg *VLABConfig, opts VLABRunOpts) (*VLAB, error) {
	orderedVMNames := slices.Collect(maps.Keys(cfg.VMs))
	slices.SortFunc(orderedVMNames, func(a, b string) int {
		vma, vmb := cfg.VMs[a], cfg.VMs[b]

		if vma.Type != vmb.Type {
			return cmp.Compare(vma.Type, vmb.Type)
		}

		return cmp.Compare(a, b)
	})

	vmIDs := map[string]uint{}
	for idx, name := range orderedVMNames {
		vmIDs[name] = uint(idx) //nolint:gosec
	}

	vms := []VM{}
	passthroughs := []string{}
	tapID := 0
	controlID := 0
	for _, name := range orderedVMNames {
		vm := cfg.VMs[name]
		vmID := vmIDs[name]
		maxNICID := uint(0)
		nics := map[uint]string{}

		for nicName, nicConfig := range vm.NICs {
			nicID, err := getNICID(nicName)
			if err != nil {
				return nil, fmt.Errorf("getting NIC ID for %q: %w", nicName, err)
			}
			maxNICID = max(maxNICID, nicID)

			nics[nicID] = nicConfig
		}

		if maxNICID >= VLABMaxNICs {
			return nil, fmt.Errorf("too many NICs for VM %q: %d", name, len(nics)) //nolint:goerr113
		}

		paddedNICs := make([]string, int(maxNICID)+1)
		for idx := uint(0); idx <= maxNICID; idx++ {
			if nic, ok := nics[idx]; ok {
				paddedNICs[idx] = nic
			} else {
				paddedNICs[idx] = NICTypeNoop
			}
		}

		for nicID, nicCfgRaw := range paddedNICs {
			if nicID >= VLABMaxNICs {
				return nil, fmt.Errorf("too many NICs for VM %q: %d", name, len(nics)) //nolint:goerr113
			}

			mac := fmt.Sprintf(VLABMACTmpl, vmID, nicID)

			nicCfgParts := strings.SplitN(nicCfgRaw, NICTypeSep, 2)
			nicType := nicCfgParts[0]
			nicCfg := ""
			if len(nicCfgParts) > 1 {
				nicCfg = nicCfgParts[1]
			}

			netdev := ""
			device := ""
			usernet := 0
			if nicType == NICTypeNoop || nicType == NICTypeDirect {
				port := getDirectNICPort(vmID, uint(nicID)) //nolint:gosec
				netdev = fmt.Sprintf("socket,udp=127.0.0.1:%d", port)

				if nicCfg != "" {
					parts := strings.SplitN(nicCfg, "/", 2)
					if len(parts) != 2 {
						return nil, fmt.Errorf("invalid NIC config %q for VM %q", nicCfg, name) //nolint:goerr113
					}

					otherVM, otherNIC := parts[0], parts[1]
					otherVMID, ok := vmIDs[otherVM]
					if !ok {
						return nil, fmt.Errorf("unknown VM %q in NIC config %q for VM %q", otherVM, nicCfg, name) //nolint:goerr113
					}

					otherNICID, err := getNICID(otherNIC)
					if err != nil {
						return nil, fmt.Errorf("getting NIC ID for %q: %w", nicCfg, err)
					}

					otherPort := getDirectNICPort(otherVMID, otherNICID)
					netdev += fmt.Sprintf(",localaddr=127.0.0.1:%d", otherPort)
				} else if nicType == NICTypeDirect {
					return nil, fmt.Errorf("missing NIC config for direct NIC %d of VM %q, nicCfgRaw: %q", nicID, name, nicCfgRaw) //nolint:goerr113
				}
			} else if nicType == NICTypeUsernet {
				if usernet > 0 {
					return nil, fmt.Errorf("multiple usernet NICs for VM %q", name) //nolint:goerr113
				}
				usernet++

				if vm.Type == VMTypeSwitch {
					slog.Warn("Usernet NICs are not supposed to be used for switch", "vm", name)
				}

				sshPort := getSSHPort(vmID)
				// TODO make subnet configurable
				netdev = fmt.Sprintf("user,hostname=%s,hostfwd=tcp:0.0.0.0:%d-:22,net=172.31.%d.0/24,dhcpstart=172.31.%d.10", name, sshPort, vmID, vmID)
				if vm.Type == VMTypeControl && controlID == 0 {
					// TODO use consts and enable for other control VMs
					netdev += ",hostfwd=tcp:0.0.0.0:6443-:6443,hostfwd=tcp:0.0.0.0:31000-:31000"
				}
				if vm.Type == VMTypeControl && opts.ControlsRestricted || vm.Type == VMTypeServer && opts.ServersRestricted {
					netdev += ",restrict=yes"
				}
			} else if nicType == NICTypeManagement {
				if nicCfg != "" {
					mac = nicCfg
				}
				netdev = fmt.Sprintf("tap,ifname=%s%d,script=no,downscript=no", VLABTapPrefix, tapID)
				tapID++
			} else if nicType == NICTypePassthrough {
				if nicCfg == "" {
					return nil, fmt.Errorf("missing NIC config for passthrough NIC %d of VM %q", nicID, name) //nolint:goerr113
				}

				passthroughs = append(passthroughs, nicCfg)
				device = fmt.Sprintf("vfio-pci,host=%s", nicCfg)
			} else {
				return nil, fmt.Errorf("unknown NIC type %q for VM %q", nicType, name) //nolint:goerr113
			}

			if netdev != "" {
				netdev += fmt.Sprintf(",id=eth%02d", nicID)
			}

			if device == "" {
				device = fmt.Sprintf("e1000,netdev=eth%02d,mac=%s", nicID, mac)
			}
			device += fmt.Sprintf(",bus=%s%d,addr=0x%x", VLABPCIBridgePrefix, nicID/VLABNICsPerPCIBridge, nicID%VLABNICsPerPCIBridge)

			nic := ""
			if netdev != "" {
				nic += "-netdev " + netdev + " "
			}
			nic += "-device " + device

			paddedNICs[nicID] = nic
		}

		if vm.Type == VMTypeControl {
			controlID++
		}

		size := cfg.Sizes.Server
		if vm.Type == VMTypeSwitch {
			size = cfg.Sizes.Switch
		} else if vm.Type == VMTypeControl {
			size = cfg.Sizes.Control
		} else if vm.Type == VMTypeGateway {
			size = cfg.Sizes.Gateway
		} else if vm.Type == VMTypeExternal {
			size = cfg.Sizes.External
		}

		vms = append(vms, VM{
			ID:   vmID,
			Name: name,
			Type: vm.Type,
			NICs: paddedNICs,
			Size: size,
		})
	}

	return &VLAB{
		SSHKey:       cfg.SSHKey,
		VMs:          vms,
		Taps:         tapID,
		Passthroughs: passthroughs,
		Externals:    cfg.Externals,
	}, nil
}

func getAsn(obj client.Object) uint32 {
	if obj.GetAnnotations() != nil {
		annotationLabel := HHFabCfgASNPrefix + HHFabCfgPrefix
		t, exist := obj.GetAnnotations()[annotationLabel]
		if exist {
			asn, err := strconv.ParseUint(t, 10, 32)
			if err == nil {
				return uint32(asn)
			}

			slog.Warn("Invalid ASN annotation value", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName(), annotationLabel, t)
		}
	}

	return 0
}

func isHardware(obj client.Object) bool {
	if obj.GetAnnotations() != nil {
		t, exist := obj.GetAnnotations()[HHFabCfgType]
		if exist {
			if t == HHFabCfgTypeHW {
				return true
			}

			slog.Warn("Invalid type annotation value", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName(), HHFabCfgType, t)
		}
	}

	return false
}

func getPassthroughLinks(obj client.Object) map[string]string {
	links := map[string]string{}

	for k, v := range obj.GetAnnotations() {
		if strings.HasPrefix(k, HHFabCfgLinkPrefix) {
			if !strings.HasPrefix(v, HHFabCfgPCIPrefix) {
				slog.Warn("Invalid link annotation value", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName(), k, v)

				continue
			}
			port := k[len(HHFabCfgLinkPrefix):]
			port = strings.ReplaceAll(port, "_", "/")

			links[obj.GetName()+"/"+port] = v[len(HHFabCfgPCIPrefix):]
		}
	}

	return links
}

const (
	srvPrefix = "enp2s"
	swMPrefix = "M1"
	swEPrefix = "E1/"
)

func getNICID(nic string) (uint, error) {
	if nic == swMPrefix {
		return 0, nil
	}

	raw := ""
	if strings.HasPrefix(nic, srvPrefix) {
		raw = nic[len(srvPrefix):]
	} else if strings.HasPrefix(nic, swEPrefix) {
		raw = nic[len(swEPrefix):]
	} else {
		return 0, fmt.Errorf("invalid NIC ID %q", nic) //nolint:goerr113
	}

	v, err := strconv.ParseUint(raw, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("parsing NIC ID %q: %w", nic, err)
	}

	return uint(v), nil
}

func getDirectNICPort(vmID uint, nicID uint) uint {
	return VLABBaseDirectPort + 100*vmID + nicID
}

func getSSHPort(vmID uint) uint {
	return VLABBaseSSHPort + vmID
}

func getOrCreateSSHKey(path string) (string, string, error) {
	kp, err := keygen.New(path, keygen.WithWrite(), keygen.WithKeyType(keygen.Ed25519))
	if err != nil {
		return "", "", fmt.Errorf("preparing key pair: %w", err)
	}

	return kp.AuthorizedKey(), string(kp.RawPrivateKey()), nil
}
