package vlab

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"golang.org/x/exp/maps"
)

const (
	VM_SIZE_DEFAULT = "default" // meaningful VM sizes for dev & testing
	VM_SIZE_COMPACT = "compact" // minimal working setup, applied on top of default
	VM_SIZE_FULL    = "full"    // full setup as specified in requirements and more real switch resources, applied on top of default
)

var VM_SIZES = []string{VM_SIZE_DEFAULT, VM_SIZE_COMPACT, VM_SIZE_FULL}

var DefaultControlVM = VMConfig{
	CPU:  6,
	RAM:  6144,
	Disk: 100,
}

var CompactControlVM = VMConfig{
	CPU:  4,
	RAM:  4096,
	Disk: 50,
}

var FullControlVM = VMConfig{
	CPU:  8,
	RAM:  16384,
	Disk: 250,
}

var DefaultServerVM = VMConfig{
	CPU:  2,
	RAM:  768,
	Disk: 10,
}

var CompactServerVM = VMConfig{
	CPU: 1,
}

var FullServerVM = VMConfig{
	RAM: 1024,
}

var DefaultSwitchVM = VMConfig{
	CPU:  4,
	RAM:  5120,
	Disk: 50,
}

var CompactSwitchVM = VMConfig{
	CPU:  3,
	RAM:  3584,
	Disk: 30,
}

var FullSwitchVM = VMConfig{
	RAM: 8192,
}

type VMManager struct {
	cfg *Config
	vms map[string]*VM
}

type VMType string

const (
	VMTypeControl  VMType = "control"
	VMTypeServer   VMType = "server"
	VMTypeSwitchVS VMType = "switch-vs"
	VMTypeSwitchHW VMType = "switch-hw" // fake VM to simplify calculations
)

type VM struct {
	ID         int
	Name       string
	Type       VMType
	Basedir    string
	Config     VMConfig
	Interfaces map[int]VMInterface

	Ready     fileMarker
	Installed fileMarker
}

type VMInterface struct {
	Connection  string
	Netdev      string
	Passthrough string
}

func NewVMManager(cfg *Config, data *wiring.Data, basedir string, size string) (*VMManager, error) {
	cfg.VMs.Control = cfg.VMs.Control.DefaultsFrom(DefaultControlVM)
	cfg.VMs.Server = cfg.VMs.Server.DefaultsFrom(DefaultServerVM)
	cfg.VMs.Switch = cfg.VMs.Switch.DefaultsFrom(DefaultSwitchVM)
	if size == VM_SIZE_DEFAULT {
		cfg.VMs.Control = cfg.VMs.Control.OverrideBy(DefaultControlVM)
		cfg.VMs.Server = cfg.VMs.Server.OverrideBy(DefaultServerVM)
		cfg.VMs.Switch = cfg.VMs.Switch.OverrideBy(DefaultSwitchVM)
	}
	if size == VM_SIZE_COMPACT {
		cfg.VMs.Control = cfg.VMs.Control.OverrideBy(CompactControlVM)
		cfg.VMs.Server = cfg.VMs.Server.OverrideBy(CompactServerVM)
		cfg.VMs.Switch = cfg.VMs.Switch.OverrideBy(CompactSwitchVM)
	}
	if size == VM_SIZE_FULL {
		cfg.VMs.Control = cfg.VMs.Control.OverrideBy(FullControlVM)
		cfg.VMs.Server = cfg.VMs.Server.OverrideBy(FullServerVM)
		cfg.VMs.Switch = cfg.VMs.Switch.OverrideBy(FullSwitchVM)
	}

	mngr := &VMManager{
		cfg: cfg,
		vms: map[string]*VM{},
	}

	vmID := 0

	for _, server := range data.Server.All() {
		if server.Spec.Type != wiringapi.ServerTypeControl {
			continue
		}

		if mngr.vms[server.Name] != nil {
			return nil, errors.Errorf("duplicate server/switch name: %s", server.Name)
		}

		mngr.vms[server.Name] = &VM{
			ID:     vmID,
			Name:   server.Name,
			Type:   VMTypeControl,
			Config: cfg.VMs.Control,
			Interfaces: map[int]VMInterface{
				0: {
					Connection: "host",
					// TODO optionally make control node isolated using ",restrict=yes"
					Netdev: fmt.Sprintf("user,hostfwd=tcp:0.0.0.0:%d-:22,hostfwd=tcp:0.0.0.0:%d-:6443,hostfwd=tcp:0.0.0.0:%d-:31000,hostname=%s,domainname=local,dnssearch=local,net=172.31.%d.0/24,dhcpstart=172.31.%d.10",
						sshPortFor(vmID), KUBE_PORT, REGISTRY_PORT, server.Name, vmID, vmID),
				},
			},
		}

		vmID++
	}

	if vmID == 0 {
		return nil, errors.Errorf("control node is required")
	}
	if vmID > 1 {
		return nil, errors.Errorf("multiple control nodes not support")
	}

	for _, server := range data.Server.All() {
		if server.Spec.Type != wiringapi.ServerTypeDefault {
			continue
		}

		if mngr.vms[server.Name] != nil {
			return nil, errors.Errorf("dublicate server/switch name: %s", server.Name)
		}

		mngr.vms[server.Name] = &VM{
			ID:     vmID,
			Name:   server.Name,
			Type:   VMTypeServer,
			Config: cfg.VMs.Server,
			Interfaces: map[int]VMInterface{
				0: {
					Connection: "host",
					Netdev: fmt.Sprintf("user,hostfwd=tcp:0.0.0.0:%d-:22,hostname=%s,domainname=local,dnssearch=local,net=172.31.%d.0/24,dhcpstart=172.31.%d.10,restrict=yes",
						sshPortFor(vmID), server.Name, vmID, vmID),
				},
			},
		}

		vmID++
	}

	for _, sw := range data.Switch.All() {
		if mngr.vms[sw.Name] != nil {
			return nil, errors.Errorf("dublicate server/switch name: %s", sw.Name)
		}

		if switchCfg, exists := mngr.cfg.Switches[sw.Name]; exists {
			if switchCfg.Type == ConfigSwitchTypeHW {
				mngr.vms[sw.Name] = &VM{
					Name: sw.Name,
					Type: VMTypeSwitchHW,
				}
				continue
			}
		}

		mngr.vms[sw.Name] = &VM{
			ID:         vmID,
			Name:       sw.Name,
			Type:       VMTypeSwitchVS,
			Config:     cfg.VMs.Switch,
			Interfaces: map[int]VMInterface{},
		}

		vmID++
	}

	for _, vm := range mngr.vms {
		if vm.Type == VMTypeSwitchHW {
			continue
		}

		vm.Basedir = filepath.Join(basedir, vm.Name)
		vm.Ready = fileMarker{path: filepath.Join(vm.Basedir, "ready")}
		vm.Installed = fileMarker{path: filepath.Join(vm.Basedir, "installed")}
	}

	for _, conn := range data.Connection.All() {
		links := [][2]wiringapi.IPort{}

		if conn.Spec.Unbundled != nil {
			links = append(links, [2]wiringapi.IPort{&conn.Spec.Unbundled.Link.Server, &conn.Spec.Unbundled.Link.Switch})
		} else if conn.Spec.Bundled != nil {
			for _, link := range conn.Spec.Bundled.Links {
				server := link.Server
				switch1 := link.Switch
				links = append(links, [2]wiringapi.IPort{&server, &switch1})
			}
		} else if conn.Spec.Management != nil {
			links = append(links, [2]wiringapi.IPort{&conn.Spec.Management.Link.Server, &conn.Spec.Management.Link.Switch})
		} else if conn.Spec.MCLAG != nil {
			for _, link := range conn.Spec.MCLAG.Links {
				server := link.Server
				switch1 := link.Switch
				links = append(links, [2]wiringapi.IPort{&server, &switch1})
			}
		} else if conn.Spec.ESLAG != nil {
			for _, link := range conn.Spec.ESLAG.Links {
				server := link.Server
				switch1 := link.Switch
				links = append(links, [2]wiringapi.IPort{&server, &switch1})
			}
		} else if conn.Spec.MCLAGDomain != nil {
			for _, link := range conn.Spec.MCLAGDomain.PeerLinks {
				switch1 := link.Switch1
				switch2 := link.Switch2
				links = append(links, [2]wiringapi.IPort{&switch1, &switch2})
			}
			for _, link := range conn.Spec.MCLAGDomain.SessionLinks {
				switch1 := link.Switch1
				switch2 := link.Switch2
				links = append(links, [2]wiringapi.IPort{&switch1, &switch2})
			}
		} else if conn.Spec.Fabric != nil {
			for _, link := range conn.Spec.Fabric.Links {
				spine := link.Spine
				leaf := link.Leaf
				links = append(links, [2]wiringapi.IPort{&spine, &leaf})
			}
		} else if conn.Spec.VPCLoopback != nil {
			for _, link := range conn.Spec.VPCLoopback.Links {
				switch1 := link.Switch1
				switch2 := link.Switch2
				links = append(links, [2]wiringapi.IPort{&switch1, &switch2})
			}
		} else if conn.Spec.External != nil {
			links = append(links, [2]wiringapi.IPort{&conn.Spec.External.Link.Switch, nil})
		} else {
			return nil, errors.Errorf("unsupported connection type %s", conn.Name)
		}

		for _, link := range links {
			err := mngr.AddLink(link[0], link[1], conn.Name)
			if err != nil {
				return nil, err
			}
			err = mngr.AddLink(link[1], link[0], conn.Name)
			if err != nil {
				return nil, err
			}
		}
	}

	// fill gaps in interfaces
	for _, vm := range mngr.vms {
		if vm.Type == VMTypeSwitchHW {
			continue
		}

		usedDevs := map[int]bool{}
		maxDevID := 0

		for iface := range vm.Interfaces {
			if iface > maxDevID {
				maxDevID = iface
			}
			usedDevs[iface] = true
		}

		for iface := 0; iface <= maxDevID; iface++ {
			if !usedDevs[iface] {
				vm.Interfaces[iface] = VMInterface{}
			}
		}
	}

	return mngr, nil
}

func (mngr *VMManager) AddLink(local wiringapi.IPort, dest wiringapi.IPort, conn string) error {
	if local == nil {
		return nil
	}

	localVM, exists := mngr.vms[local.DeviceName()]
	if !exists {
		return errors.Errorf("%s does not exist, conn %s", local.DeviceName(), conn)
	}

	if localVM.Type == VMTypeSwitchHW {
		return nil
	}

	localPortID, destPortID := -1, -1
	var destVM *VM
	var err error

	localPortID, err = portIdForName(local.LocalPortName())
	if err != nil {
		return err
	}

	var linkCfg *LinkConfig

	if dest != nil {
		destPortID, err = portIdForName(dest.LocalPortName())
		if err != nil {
			return err
		}
		destVM, exists = mngr.vms[dest.DeviceName()]
		if !exists {
			return errors.Errorf("dest %s does not exist for %s, conn %s", dest.DeviceName(), local.PortName(), conn)
		}

		if lCfg, exists := mngr.cfg.Links[dest.PortName()]; exists {
			linkCfg = &lCfg
		}
	}

	if _, exists := localVM.Interfaces[localPortID]; exists {
		return errors.Errorf("%s already has interface %d, can't add %s, conn %s", local.DeviceName(), localPortID, local.PortName(), conn)
	}

	if linkCfg != nil {
		if linkCfg.PCIAddress == "" {
			return errors.Errorf("pci address required for %s, conn %s", local.PortName(), conn)
		}

		if destVM.Type != VMTypeSwitchHW {
			return errors.Errorf("dest %s should be hardware switch if pci mapping specified, conn %s", dest.DeviceName(), conn)
		}

		localVM.Interfaces[localPortID] = VMInterface{
			Connection:  conn,
			Passthrough: linkCfg.PCIAddress,
		}
	} else if destVM.Type == VMTypeSwitchHW {
		return errors.Errorf("pci mapping is missing for %s, conn %s", dest.PortName(), conn)
	} else {
		netdev := fmt.Sprintf("socket,udp=127.0.0.1:%d", localVM.ifacePortFor(localPortID))
		if destVM != nil {
			netdev += fmt.Sprintf(",localaddr=127.0.0.1:%d", destVM.ifacePortFor(destPortID))
		}

		localVM.Interfaces[localPortID] = VMInterface{
			Connection: conn,
			Netdev:     netdev,
		}
	}

	return nil
}

func (mngr *VMManager) sortedVMs() []*VM {
	vms := maps.Values(mngr.vms)
	sort.Slice(vms, func(i, j int) bool {
		return vms[i].ID < vms[j].ID
	})

	return vms
}

func (mngr *VMManager) LogOverview() {
	for _, vm := range mngr.sortedVMs() {
		if vm.Type == VMTypeSwitchHW {
			continue
		}

		slog.Info("VM", "id", vm.ID, "name", vm.Name, "type", vm.Type)
		for ifaceID := 0; ifaceID < len(vm.Interfaces); ifaceID++ {
			iface := vm.Interfaces[ifaceID]
			slog.Debug(">>> Interface", "id", ifaceID, "netdev", iface.Netdev, "passthrough", iface.Passthrough, "conn", iface.Connection)
		}
	}

	for _, vm := range mngr.sortedVMs() {
		if vm.Type != VMTypeSwitchHW {
			continue
		}

		slog.Info("Hardware", "name", vm.Name, "type", vm.Type)
	}
}

func (vm *VM) UUID() string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", vm.ID)
}

func (vm *VM) macFor(iface int) string {
	return fmt.Sprintf(MAC_ADDR_TMPL, vm.ID, iface)
}

func (vm *VM) ifacePortFor(iface int) int {
	return IF_PORT_BASE + vm.ID*IF_PORT_VM_ID_MULT + iface*IF_PORT_PORT_ID_MULT
}

func (vm *VM) sshPort() int {
	return sshPortFor(vm.ID)
}

func sshPortFor(vmID int) int {
	return SSH_PORT_BASE + vmID
}

func portIdForName(name string) (int, error) {
	if strings.HasPrefix(name, "Management0") {
		return 0, nil
	} else if strings.HasPrefix(name, "Ethernet") { // sonic interface naming is straighforward
		port, _ := strings.CutPrefix(name, "Ethernet")
		idx, error := strconv.Atoi(port)

		return idx + 1, errors.Wrapf(error, "error converting port name '%s' to port id", name)
	} else if strings.HasPrefix(name, "port") { // just for simplicity to not try to guess port names on servers
		port, _ := strings.CutPrefix(name, "port")
		idx, error := strconv.Atoi(port)

		return idx, errors.Wrapf(error, "error converting port name '%s' to port id", name)
	} else if strings.HasPrefix(name, "enp2s") { // current port naming when using e1000 with flatcar
		port, _ := strings.CutPrefix(name, "enp2s")
		idx, error := strconv.Atoi(port)

		// ouch, this is a hack, but it seems like the only way to get the right port id for now
		return idx, errors.Wrapf(error, "error converting port name '%s' to port id", name)
	} else {
		return -1, errors.Errorf("unsupported port name '%s'", name)
	}
}
