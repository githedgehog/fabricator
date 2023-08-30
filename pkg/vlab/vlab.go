package vlab

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"golang.org/x/exp/maps"
)

const (
	MAC_ADDR_TMPL        = "0c:20:0a:0a:%02d:%02d"
	KUBE_PORT_BASE       = 16443
	CONSOLE_PORT_BASE    = 21000
	SSH_PORT_BASE        = 22000
	IF_PORT_BASE         = 30000
	IF_PORT_VM_ID_MULT   = 100
	IF_PORT_PORT_ID_MULT = 1
)

type Service struct {
	VMs map[string]*VM
}

type VM struct {
	ID   int
	Name string
	// DeviceType  string
	Links       []*Link
	ConsolePort int
	// TODO add OS and TPM
}

type Link struct {
	DevID string
	MAC   string

	LocalIfPort   int
	LocalPortName string
	DestName      string
	DestIfPort    int
	DestPortName  string

	HostFwd  bool
	SSHPort  int
	KubePort int
}

func StartServer(from string) error {
	data, err := wiring.LoadDataFrom(from)
	if err != nil {
		return err
	}

	svc := &Service{
		VMs: map[string]*VM{},
	}

	for _, server := range data.Server.All() {
		if !server.IsControl() {
			continue
		}
		err := svc.AddVM(server.Name)
		if err != nil {
			return err
		}

		vm := svc.VMs[server.Name]
		err = svc.AddControlHostFwdLink(vm)
		if err != nil {
			return err
		}
	}

	for _, sw := range data.Switch.All() {
		err := svc.AddVM(sw.Name)
		if err != nil {
			return err
		}
	}

	for _, server := range data.Server.All() {
		if server.IsControl() {
			continue
		}
		err := svc.AddVM(server.Name)
		if err != nil {
			return err
		}
	}

	for _, conn := range data.Connection.All() {
		svc.AddConnection(conn)
	}

	vms := maps.Values(svc.VMs)
	sort.Slice(vms, func(i, j int) bool {
		return vms[i].ID < vms[j].ID
	})

	for _, vm := range vms {
		log.Println("VM", vm.ID, vm.Name, vm.ConsolePort)

		sort.Slice(vm.Links, func(i, j int) bool {
			return vm.Links[i].DevID < vm.Links[j].DevID
		})

		for _, link := range vm.Links {
			log.Println(">>> Link", *link)
		}
	}

	return nil
}

func (svc *Service) AddVM(name string) error {
	if _, exists := svc.VMs[name]; exists {
		return fmt.Errorf("vm with name '%s' already exists", name)
	}

	vm := &VM{
		ID:   len(svc.VMs),
		Name: name,
	}
	vm.ConsolePort = svc.consolePortFor(vm)
	svc.VMs[name] = vm

	return nil
}

func (svc *Service) AddConnection(conn *wiringapi.Connection) error {
	links := [][2]wiringapi.IPort{}

	if conn.Spec.Unbundled != nil {
		links = append(links, [2]wiringapi.IPort{&conn.Spec.Unbundled.Link.Server, &conn.Spec.Unbundled.Link.Switch})
	} else if conn.Spec.Management != nil {
		links = append(links, [2]wiringapi.IPort{&conn.Spec.Management.Link.Server, &conn.Spec.Management.Link.Switch})
	} else if conn.Spec.MCLAG != nil {
		for _, link := range conn.Spec.MCLAG.Links {
			server := link.Server
			switch1 := link.Switch
			links = append(links, [2]wiringapi.IPort{&server, &switch1})
		}
	} else if conn.Spec.MCLAGDomain != nil {
		for _, link := range conn.Spec.MCLAGDomain.Links {
			switch1 := link.Switch1
			switch2 := link.Switch2
			links = append(links, [2]wiringapi.IPort{&switch1, &switch2})
		}
	}

	for _, link := range links {
		err := svc.AddLink(link[0], link[1])
		if err != nil {
			return err
		}
		err = svc.AddLink(link[1], link[0])
		if err != nil {
			return err
		}
	}

	return nil
}

func (svc *Service) AddLink(local wiringapi.IPort, dest wiringapi.IPort) error {
	localVM := svc.VMs[local.DeviceName()]
	destVM := svc.VMs[dest.DeviceName()]

	localPortID, err := portIdForName(local.LocalPortName())
	if err != nil {
		return err
	}
	destPortID, err := portIdForName(dest.LocalPortName())
	if err != nil {
		return err
	}

	localVM.Links = append(localVM.Links, &Link{
		DevID:         fmt.Sprintf("eth%d", localPortID),
		MAC:           svc.macFor(localVM, localPortID),
		LocalIfPort:   svc.ifPortFor(localVM, localPortID),
		LocalPortName: local.PortName(),
		DestName:      destVM.Name,
		DestIfPort:    svc.ifPortFor(destVM, destPortID),
		DestPortName:  dest.PortName(),
	})

	return nil
}

func (svc *Service) AddControlHostFwdLink(vm *VM) error {
	vm.Links = append(vm.Links, &Link{
		DevID:    "eth0",
		MAC:      svc.macFor(vm, 0),
		HostFwd:  true,
		SSHPort:  svc.sshPortFor(vm),
		KubePort: KUBE_PORT_BASE,
	})

	return nil
}

// TODO replace with logic from SwitchProfile and ServerProfile
func portIdForName(name string) (int, error) {
	if strings.HasPrefix(name, "Management0") {
		return 0, nil
	} else if strings.HasPrefix(name, "Ethernet") {
		port, _ := strings.CutPrefix(name, "Ethernet")
		idx, error := strconv.Atoi(port)

		return idx + 1, errors.Wrapf(error, "error converting port name '%s' to port id", name)
	} else if strings.HasPrefix(name, "nic0/port") {
		port, _ := strings.CutPrefix(name, "nic0/port")
		idx, error := strconv.Atoi(port)

		return idx, errors.Wrapf(error, "error converting port name '%s' to port id", name)
	} else {
		return -1, fmt.Errorf("unsupported port name '%s'", name)
	}
}

func (svc *Service) consolePortFor(vm *VM) int {
	return CONSOLE_PORT_BASE + vm.ID
}

func (svc *Service) sshPortFor(vm *VM) int {
	return SSH_PORT_BASE + svc.VMs[vm.Name].ID
}

func (svc *Service) macFor(vm *VM, port int) string {
	return fmt.Sprintf(MAC_ADDR_TMPL, svc.VMs[vm.Name].ID, port)
}

func (svc *Service) ifPortFor(vm *VM, port int) int {
	return IF_PORT_BASE + svc.VMs[vm.Name].ID*IF_PORT_VM_ID_MULT + port*IF_PORT_PORT_ID_MULT
}
