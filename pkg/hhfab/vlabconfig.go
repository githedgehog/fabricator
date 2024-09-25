package hhfab

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dario.cat/mergo"
	fmeta "go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	VLABDir        = "vlab"
	VLABConfigFile = "config.yaml"
	VLABVMsDir     = "vms"

	VLABSwitchMACTmpl = "0c:20:12:ff:%02d:00"
	VLABMACTmpl       = "0c:20:12:fe:%02d:%02d"

	HHFabCfgPrefix             = ".hhfab.githedgehog.com"
	HHFabCfgType               = "type" + HHFabCfgPrefix
	HHFabCfgTypeHW             = "hw"
	HHFabCfgSerial             = "serial" + HHFabCfgPrefix
	HHFabCfgLinkPrefix         = "link" + HHFabCfgPrefix + "/"
	HHFabCfgPCIPrefix          = "pci@"
	HHFabCfgSerialSchemeSSH    = "ssh://"
	HHFabCfgSerialSchemeTelnet = "telnet://"
)

type VLABConfig struct {
	Sizes VMSizes             `json:"sizes"`
	VMs   map[string]VMConfig `json:"vms"`
}

type VMSizes struct {
	Control VMSize `json:"control"`
	Switch  VMSize `json:"switch"`
	Server  VMSize `json:"server"`
}

type VMConfig struct {
	Type       VMType            `json:"type"`
	Restricted bool              `json:"restricted"`
	NICs       map[string]string `json:"nics"`
}

type VMType string

const (
	VMTypeControl VMType = "control"
	VMTypeSwitch  VMType = "switch"
	VMTypeServer  VMType = "server"
)

var VMTypes = []VMType{
	VMTypeControl,
	VMTypeSwitch,
	VMTypeServer,
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
	Control: VMSize{CPU: 6, RAM: 6144, Disk: 100}, // TODO 8GB RAM?
	Switch:  VMSize{CPU: 4, RAM: 5120, Disk: 50},  // TODO 6GB RAM?
	Server:  VMSize{CPU: 2, RAM: 768, Disk: 10},   // TODO 1GB RAM?
}

type VLABConfigOpts struct {
	Recreate           bool
	ControlsRestricted bool
	ServersRestricted  bool
}

func (c *Config) PrepareVLAB(ctx context.Context, in VLABConfigOpts) (*VLAB, error) {
	vlabDir := filepath.Join(c.WorkDir, VLABDir)
	vlabVMsDir := filepath.Join(vlabDir, VLABVMsDir)

	if stat, err := os.Stat(vlabDir); err != nil {
		if os.IsNotExist(err) {
			if err := os.Mkdir(vlabDir, 0o700); err != nil {
				return nil, fmt.Errorf("creating VLAB directory: %w", err) //nolint:goerr113
			}
		} else {
			return nil, fmt.Errorf("checking VLAB directory: %w", err)
		}
	} else if !stat.IsDir() {
		return nil, fmt.Errorf("VLAB directory is not a directory: %q", vlabDir) //nolint:goerr113
	}

	createCfg := in.Recreate
	vlabCfgFile := filepath.Join(vlabDir, VLABConfigFile)
	if _, err := os.Stat(vlabCfgFile); err != nil {
		if os.IsNotExist(err) {
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

		vlabCfg, err := c.CreateVLABConfig(ctx, in)
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

	data, err := os.ReadFile(vlabCfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading VLAB config file: %w", err)
	}

	vlabCfg := &VLABConfig{}
	if err := yaml.Unmarshal(data, vlabCfg); err != nil {
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

	if !createCfg {
		slog.Info("VLAB config loaded", "file", filepath.Join(VLABDir, VLABConfigFile))
	}

	vlab, err := c.VLABFromConfig(vlabCfg)
	if err != nil {
		return nil, fmt.Errorf("creating VLAB: %w", err)
	}

	return vlab, nil
}

func (c *Config) CreateVLABConfig(ctx context.Context, in VLABConfigOpts) (*VLABConfig, error) {
	cfg := &VLABConfig{
		Sizes: DefaultSizes,
		VMs:   map[string]VMConfig{},
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

	for _, control := range c.Controls {
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

		cfg.VMs[control.Name] = VMConfig{
			Type:       VMTypeControl,
			Restricted: in.ControlsRestricted,
			NICs: map[string]string{
				"enp2s0": NICTypeUsernet,
				"enp2s1": mgmt,
			},
		}
	}

	servers := &wiringapi.ServerList{}
	if err := c.Wiring.List(ctx, servers); err != nil {
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
			Type:       VMTypeServer,
			Restricted: in.ServersRestricted,
			NICs: map[string]string{
				"enp2s0": NICTypeUsernet,
			},
		}
	}

	switches := &wiringapi.SwitchList{}
	if err := c.Wiring.List(ctx, switches); err != nil {
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
			Type:       VMTypeSwitch,
			Restricted: true,
			NICs: map[string]string{
				"M1": mgmt,
			},
		}
	}

	conns := &wiringapi.ConnectionList{}
	if err := c.Wiring.List(ctx, conns); err != nil {
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

	for _, conn := range conns.Items {
		if conn.Spec.Fabric != nil {
			for _, link := range conn.Spec.Fabric.Links {
				if err := addLink(link.Spine.Port, link.Leaf.Port); err != nil {
					return nil, fmt.Errorf("failed to add link for fabric connection %s: %w", conn.Name, err)
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
		}
	}

	return cfg, nil
}

func isHardware(obj client.Object) bool {
	if obj.GetAnnotations() != nil {
		t, exist := obj.GetAnnotations()[HHFabCfgType]
		if exist {
			if t == HHFabCfgTypeHW {
				return true
			}

			slog.Warn("Invalid annotation value: %s=%s", HHFabCfgType, t)
		}
	}

	return false
}

func getPassthroughLinks(obj client.Object) map[string]string {
	links := map[string]string{}

	for k, v := range obj.GetAnnotations() {
		if strings.HasPrefix(k, HHFabCfgLinkPrefix) {
			if !strings.HasPrefix(v, HHFabCfgPCIPrefix) {
				slog.Warn("Invalid link value: %s=%s", k, v)

				continue
			}
			port := k[len(HHFabCfgLinkPrefix):]
			port = strings.ReplaceAll(port, "_", "/")

			links[obj.GetName()+"/"+port] = v[len(HHFabCfgPCIPrefix):]
		}
	}

	return links
}

// MAC
// Types:
// - Direct
// - Passthrough
// - Usernet
// - Management network (tap, bridge)

// For VM we need:
// - direct SSH port

// Switch VM ports:
// M1 -> mgmt
// E1/1 -> direct:leaf01/E1/5
// E1/2 -> noop
// E1/3 -> passthrough:pci@0000:00:00.0

// Control VM ports:
// enp2s0 -> usernet
// enp2s1 -> mgmt

// Server VM ports:
// enp2s0 -> usernet
// enp2s1 -> direct:leaf01/E1/1
// enp2s2 -> direct:leaf01/E1/2

// VM manager needs to allocate:
// - MACs / ports
// - usernet subnets
// - tap devices

// type VM struct {
// 	Name string
// 	Type VMType
// 	ID   uint // ID -> Mac, UUID, usernet subnet, etc SSH port?
// 	// NICs map[string]NIC
// 	// - NICs (MAC, how to conf, etc)
// 	// - Size (RAM, CPU, etc)
// }

// type VMManager struct {
// 	vms map[string]*VM
// 	// mgmtTaps       map[string]uint
// 	// directPorts    map[string]uint
// 	// usernetSubnets map[string]netip.Prefix
// 	// nextSSHPort       uint
// 	// nextUsernetSubnet netip.Prefix
// 	// baseMAC
// 	// basePort?
// }

// func MewVMManager(cfg vlabConfig) *VMManager {
// 	// prep all VMs, etc
// 	return &VMManager{
// 		vms: map[string]*VM{},
// 	}
// }

// func (vmm *VMManager) VMs() iter.Seq2[string, *VM] {
// 	vals := slices.Collect(maps.Values(vmm.vms))
// 	slices.SortFunc(vals, func(a, b *VM) int {
// 		return 0
// 	})

// 	return func(yield func(string, *VM) bool) {
// 		for k, v := range vmm.vms {
// 			if !yield(k, v) {
// 				break
// 			}
// 		}
// 	}
// }
