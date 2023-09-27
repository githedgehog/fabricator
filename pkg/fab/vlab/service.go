package vlab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/pkg/errors"
	"github.com/shirou/gopsutil/v3/process"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"golang.org/x/exp/maps"
	"golang.org/x/sync/errgroup"
)

const (
	MAC_ADDR_TMPL        = "0c:20:12:fe:%02d:%02d" // if changing update onie-qcow2-eeprom-edit config too
	KUBE_PORT            = 6443
	REGISTRY_PORT        = 31000
	SSH_PORT_BASE        = 22000
	IF_PORT_BASE         = 30000
	IF_PORT_VM_ID_MULT   = 100
	IF_PORT_PORT_ID_MULT = 1
)

var RequiredCommands = []string{
	"qemu-system-x86_64",
	"qemu-img",
	"tpm2",
	"swtpm_setup",
	"ssh",
	"scp",
	"sudo",
}

type Service struct {
	cfg *Config
	vms map[string]*VM
}

type Config struct {
	DryRun            bool
	Compact           bool
	Basedir           string
	Wiring            *wiring.Data
	ControlIgnition   string
	ServerIgnitionDir string
	ControlInstaller  string
	FilesDir          string
	SshKey            string
}

func Load(cfg *Config) (*Service, error) {
	if cfg.Wiring == nil {
		return nil, errors.Errorf("wiring data is not specified")
	}
	if cfg.ControlIgnition == "" {
		return nil, errors.Errorf("control ignition file is not specified")
	}
	if cfg.ServerIgnitionDir == "" {
		return nil, errors.Errorf("server ignition dir is not specified")
	}
	if cfg.ControlInstaller == "" {
		return nil, errors.Errorf("control installer file is not specified")
	}
	if cfg.FilesDir == "" {
		return nil, errors.Errorf("files dir is not specified")
	}
	if cfg.SshKey == "" {
		return nil, errors.Errorf("ssh key is not specified")
	}

	svc := &Service{
		cfg: cfg,
		vms: map[string]*VM{},
	}

	for _, server := range cfg.Wiring.Server.All() {
		if !server.IsControl() {
			continue
		}
		err := svc.AddVM(server.Name, VMOS_FLATCAR, true)
		if err != nil {
			return nil, errors.Wrapf(err, "error adding control VM %s", server.Name)
		}

		vm := svc.vms[server.Name]
		err = svc.AddControlHostFwdLink(vm)
		if err != nil {
			return nil, errors.Wrapf(err, "error adding control host fwd link for VM %s", server.Name)
		}

	}

	for _, sw := range cfg.Wiring.Switch.All() {
		err := svc.AddVM(sw.Name, VMOS_ONIE, false)
		if err != nil {
			return nil, errors.Wrapf(err, "error adding switch VM %s", sw.Name)
		}
	}

	for _, server := range cfg.Wiring.Server.All() {
		if server.IsControl() {
			continue
		}
		err := svc.AddVM(server.Name, VMOS_FLATCAR, false)
		if err != nil {
			return nil, errors.Wrapf(err, "error adding server VM %s", server.Name)
		}
	}

	for _, conn := range cfg.Wiring.Connection.All() {
		err := svc.AddConnection(conn)
		if err != nil {
			return nil, errors.Wrapf(err, "error adding connection %s", conn.Name)
		}
	}

	for _, vm := range svc.vms {
		sort.Slice(vm.Links, func(i, j int) bool {
			return vm.Links[i].DevID < vm.Links[j].DevID
		})
	}

	return svc, nil
}

func (svc *Service) checkForStaleVMs(ctx context.Context, killStaleVMs bool) error {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return errors.Wrap(err, "error getting processes")
	}

	stale := []int32{}
	for _, pr := range processes {
		cmd, err := pr.CmdlineSliceWithContext(ctx)
		if err != nil {
			return errors.Wrap(err, "error getting process cmdline")
		}

		// only one instance of VLAB supported at the same time
		if len(cmd) < 6 || cmd[0] != "qemu-system-x86_64" || cmd[1] != "-name" || cmd[3] != "-uuid" {
			continue
		}
		if !strings.HasPrefix(cmd[4], "00000000-0000-0000-0000-0000000000") {
			continue
		}

		if killStaleVMs {
			slog.Warn("Found stale VM process, killing it", "pid", pr.Pid)
			err = pr.KillWithContext(ctx)
			if err != nil {
				return errors.Wrapf(err, "error killing stale VM process %d", pr.Pid)
			}
		} else {
			slog.Error("Found stale VM process", "pid", pr.Pid)
			stale = append(stale, pr.Pid)
			// return errors.Errorf("found stale VM process %d, kill it and try again or run vlab with --kill-stale-vms", pr.Pid)
		}
	}

	if len(stale) > 0 {
		return errors.Errorf("found stale VM processes %v, kill them and try again or run vlab with --kill-stale-vms", stale)
	}

	return nil
}

func (svc *Service) StartServer(killStaleVMs bool, compact bool) error {
	svc.cfg.Compact = compact

	for _, cmd := range RequiredCommands {
		_, err := exec.LookPath(cmd)
		if err != nil {
			return errors.Errorf("required command '%s' is not available", cmd)
		}
	}

	slog.Info("Starting VLAB server...", "basedir", svc.cfg.Basedir, "dry-run", svc.cfg.DryRun)

	err := svc.checkForStaleVMs(context.TODO(), killStaleVMs)
	if err != nil {
		return errors.Wrapf(err, "error checking for stale VMs")
	}

	for _, vm := range svc.sortedVMs() {
		slog.Info("VM", "id", vm.ID, "name", vm.Name)

		sort.Slice(vm.Links, func(i, j int) bool {
			return vm.Links[i].DevID < vm.Links[j].DevID
		})

		for _, link := range vm.Links {
			slog.Info(">>> Link", "dev", link.DevID, "mac", link.MAC, "local", link.LocalPortName, "dest", link.DestPortName)
		}
	}

	err = InitTPMConfig(context.Background(), svc.cfg)
	if err != nil {
		return errors.Wrapf(err, "error initializing TPM config")
	}

	vms := svc.sortedVMs()
	eg, ctx := errgroup.WithContext(context.Background())

	for idx := range vms {
		err := vms[idx].Prepare(ctx, svc.cfg)
		if err != nil {
			return errors.Wrapf(err, "error preparing VM %s", vms[idx].Name)
		}
	}

	for idx := range vms {
		vms[idx].Run(ctx, eg)
	}

	return eg.Wait()
}

func (svc *Service) sortedVMs() []*VM {
	vms := maps.Values(svc.vms)
	sort.Slice(vms, func(i, j int) bool {
		return vms[i].ID < vms[j].ID
	})

	return vms
}

func (svc *Service) AddVM(name string, os VMOS, control bool) error {
	if _, exists := svc.vms[name]; exists {
		return errors.Errorf("vm with name '%s' already exists", name)
	}

	vm := &VM{
		Basedir:   filepath.Join(svc.cfg.Basedir, name),
		ID:        len(svc.vms),
		Name:      name,
		OS:        os,
		IsControl: control,
		Cfg:       svc.cfg,
	}
	vm.Ready = fileMarker{path: filepath.Join(vm.Basedir, "ready")}
	vm.Installed = fileMarker{path: filepath.Join(vm.Basedir, "installed")}

	svc.vms[name] = vm

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
	localVM := svc.vms[local.DeviceName()]
	destVM := svc.vms[dest.DeviceName()]

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
	sshPort := svc.sshPortFor(vm)

	vm.Links = append(vm.Links, &Link{
		DevID:        "eth0",
		MAC:          svc.macFor(vm, 0),
		IsHostFwd:    true,
		SSHPort:      sshPort,
		KubePort:     KUBE_PORT,
		RegistryPort: REGISTRY_PORT,
	})

	vm.SSHPort = sshPort

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
	} else if strings.HasPrefix(name, "eth") {
		port, _ := strings.CutPrefix(name, "eth")
		idx, error := strconv.Atoi(port)

		return idx, errors.Wrapf(error, "error converting port name '%s' to port id", name)
	} else {
		return -1, errors.Errorf("unsupported port name '%s'", name)
	}
}

func (svc *Service) sshPortFor(vm *VM) int {
	return SSH_PORT_BASE + svc.vms[vm.Name].ID
}

func (svc *Service) macFor(vm *VM, port int) string {
	return fmt.Sprintf(MAC_ADDR_TMPL, svc.vms[vm.Name].ID, port)
}

func (svc *Service) ifPortFor(vm *VM, port int) int {
	return IF_PORT_BASE + svc.vms[vm.Name].ID*IF_PORT_VM_ID_MULT + port*IF_PORT_PORT_ID_MULT
}

const (
	VM_SELECTOR_SSH = "ssh"
	VM_SELECTOR_ALL = "all"
)

func (svc *Service) vmSelector(name string, mode string, msg string) (*VM, error) {
	vms := []*VM{}

	for _, vm := range svc.sortedVMs() {
		if name != "" && vm.Name == name {
			return vm, nil
		}
		if name == "control" && vm.IsControl {
			return vm, nil
		}
		if mode == VM_SELECTOR_SSH && (vm.SSHPort > 0 || vm.OS == VMOS_ONIE) { // TODO only works for directly attached switches
			vms = append(vms, vm)
		}
		if mode == VM_SELECTOR_ALL {
			vms = append(vms, vm)
		}
	}

	extraNote := ""
	if mode == VM_SELECTOR_SSH {
		extraNote = "{{ if le .SSHPort 0 }}{{ \" (control as jump host)\" | faint }}{{ end }}"
	}

	templates := &promptui.SelectTemplates{
		Label:    "{{ .Name }}",
		Active:   "\U0001F994 {{ .Name | cyan }}" + extraNote,
		Inactive: "{{ .Name | cyan }}" + extraNote,
		Selected: "\U0001F994 {{ .Name | red | cyan }}" + extraNote,
		Details: `
----------- VM Details ------------
{{ "ID:" | faint }}	{{ .ID }}
{{ "Name:" | faint }}	{{ .Name }}
{{ "OS:" | faint }}	{{ .OS }}
{{ "Control:" | faint }}	{{ .IsControl }}
{{ "SSH:" | faint }}	{{ if gt .SSHPort 0 }}127.0.0.1:{{ .SSHPort }}{{ end }}
{{ "Ready:" | faint }}	{{ .Ready.Is }}
{{ "Installed:" | faint }}	{{ eq .IsControl .Installed.Is }}
{{ "Basedir:" | faint }}	{{ .Basedir }}
{{ "Links:" | faint }}
{{ range .Links }} {{ .DevID }} {{ .MAC }} {{ .LocalPortName }} {{ .DestPortName }}
{{ end }}`,
	}

	searcher := func(input string, index int) bool {
		pepper := vms[index]
		name := strings.Replace(strings.ToLower(pepper.Name), " ", "", -1)
		input = strings.Replace(strings.ToLower(input), " ", "", -1)

		return strings.Contains(name, input)
	}

	prompt := promptui.Select{
		Label:     msg,
		Items:     vms,
		Templates: templates,
		Size:      5,
		Searcher:  searcher,
	}

	selected, _, err := prompt.Run()
	if err != nil {
		return nil, errors.Wrap(err, "error selecting VM")
	}

	return vms[selected], nil
}

func (svc *Service) findJumpForSwitch(name string) (string, string, error) {
	control := ""
	target := ""

	for _, conn := range svc.cfg.Wiring.Connection.All() {
		if conn.Spec.Management != nil {
			if conn.Spec.Management.Link.Switch.DeviceName() == name {
				control = conn.Spec.Management.Link.Server.DeviceName()
				target = conn.Spec.Management.Link.Switch.IP

				break
			}
		}
	}

	if control == "" || target == "" {
		return "", "", errors.Errorf("failed to find suitable control node to use as jump host for vm %s", name)
	}

	var controlVM *VM
	for _, vm := range svc.sortedVMs() {
		if vm.Name == control {
			controlVM = vm
			break
		}
	}

	if controlVM == nil {
		return "", "", errors.Errorf("failed to find control vm %s to use as jump host", control)
	}

	target = strings.SplitN(target, "/", 2)[0] // we don't need the mask
	target = "admin@" + target
	proxyCmd := fmt.Sprintf("ssh %s -i %s -W %%h:%%p -p %d core@127.0.0.1",
		strings.Join(SSH_QUIET_FLAGS, " "), svc.cfg.SshKey, controlVM.SSHPort)

	return proxyCmd, target, nil
}

func (svc *Service) SSH(name string, args []string) error {
	vm, err := svc.vmSelector(name, VM_SELECTOR_SSH, "SSH to VM:")
	if err != nil {
		return err
	}

	proxyCmd := ""
	target := "core@127.0.0.1"

	if vm.SSHPort == 0 {
		if vm.OS == VMOS_ONIE {
			proxyCmd, target, err = svc.findJumpForSwitch(vm.Name)
			if err != nil {
				return err
			}
		} else {
			return errors.Errorf("selected vm %s does not have ssh port", vm.Name)
		}
	}

	slog.Info("SSH", "vm", vm.Name)

	cmdArgs := append(SSH_QUIET_FLAGS, "-i", svc.cfg.SshKey)

	if proxyCmd != "" {
		cmdArgs = append(cmdArgs,
			"-o", "ProxyCommand="+proxyCmd,
		)
	} else {
		cmdArgs = append(cmdArgs,
			"-p", fmt.Sprintf("%d", vm.SSHPort),
		)
	}

	cmdArgs = append(cmdArgs, target)
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("ssh", cmdArgs...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	slog.Debug("Running ssh", "args", strings.Join(cmdArgs, " "))

	return cmd.Run()
}

func (svc *Service) Serial(name string) error {
	vm, err := svc.vmSelector(name, VM_SELECTOR_ALL, "Connect to VM serial console:")
	if err != nil {
		return err
	}

	slog.Info("Serial", "vm", vm.Name)

	cmdArgs := []string{
		"-,raw,echo=0,escape=0x1d",
		fmt.Sprintf("unix-connect:%s", filepath.Join(vm.Basedir, "serial.sock")),
	}

	cmd := exec.Command("socat", cmdArgs...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	slog.Debug("Running socat", "args", strings.Join(cmdArgs, " "))

	slog.Warn("Use Ctrl+] to escape, if no output try Enter, safe to use Ctrl+C/Ctrl+Z")

	return cmd.Run()
}

func (svc *Service) List() error {
	_, err := svc.vmSelector("", VM_SELECTOR_ALL, "Select VM for detailed info:")
	if err != nil {
		return err
	}

	return nil
}
