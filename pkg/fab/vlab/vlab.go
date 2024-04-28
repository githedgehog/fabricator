// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vlab

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/pkg/wiring"
	"golang.org/x/sync/errgroup"
)

const (
	MACAddrTmpl      = "0c:20:12:fe:%02d:%02d" // if changing update onie-qcow2-eeprom-edit config too
	KubePort         = 6443
	RegistryPort     = 31000
	SSHPortBase      = 22000
	IfPortBase       = 30000
	IfPortNull       = IfPortBase + 9000
	IfPortVMIDMult   = 100
	IfPortPortIDMult = 1
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
	cfg  *ServiceConfig
	mngr *VMManager
}

type ServiceConfig struct {
	DryRun            bool
	Size              string
	SudoSwtpm         bool
	CharNBDDev        string
	InstallComplete   bool
	RunComplete       string
	OnReady           []string
	RestrictServers   bool
	Basedir           string
	Wiring            *wiring.Data
	ControlIgnition   string
	ServerIgnitionDir string
	ControlInstaller  string
	ServerInstaller   string
	FilesDir          string
	SSHKey            string
}

func Load(cfg *ServiceConfig) (*Service, error) {
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
	if cfg.ServerInstaller == "" {
		return nil, errors.Errorf("server installer file is not specified")
	}
	if cfg.FilesDir == "" {
		return nil, errors.Errorf("files dir is not specified")
	}
	if cfg.SSHKey == "" {
		return nil, errors.Errorf("ssh key is not specified")
	}

	vlabConfig, err := readConfigFromWiring(cfg.Wiring)
	if err != nil {
		return nil, errors.Wrapf(err, "error reading VLAB config from wiring")
	}

	mngr, err := NewVMManager(vlabConfig, cfg.Wiring, cfg.Basedir, cfg.Size, cfg.RestrictServers)
	if err != nil {
		return nil, errors.Wrapf(err, "error creating VM manager")
	}

	svc := &Service{
		cfg:  cfg,
		mngr: mngr,
	}

	return svc, nil
}

func (svc *Service) StartServer(killStaleVMs bool, charNBDDev string, installComplete bool, runComplete string, onReady []string) error {
	svc.cfg.CharNBDDev = charNBDDev
	svc.cfg.InstallComplete = installComplete
	svc.cfg.RunComplete = runComplete
	svc.cfg.OnReady = onReady

	for _, cmd := range RequiredCommands {
		_, err := exec.LookPath(cmd)
		if err != nil {
			return errors.Errorf("required command '%s' is not available", cmd)
		}
	}

	slog.Info("Starting VLAB server...", "basedir", svc.cfg.Basedir, "vm-size", svc.cfg.Size, "dry-run", svc.cfg.DryRun)

	err := checkForStaleVMs(context.TODO(), killStaleVMs)
	if err != nil {
		return errors.Wrapf(err, "error checking for stale VMs")
	}

	if killStaleVMs {
		slog.Info("Stale VMs killed, exiting, restart without --kill-stale-vms")

		return nil
	}

	svc.mngr.LogOverview()

	svc.checkResources()

	for _, vm := range svc.mngr.sortedVMs() {
		for _, iface := range vm.Interfaces {
			if iface.Passthrough != "" && !isDeviceBoundToVFIO(iface.Passthrough) {
				return errors.Errorf("pci device %s is not bound to vfio-pci, used by conn %s, run 'sudo hhfab vlab vfio-pci-bind' to bind", iface.Passthrough, iface.Connection)
			}
		}
	}

	err = InitTPMConfig(context.Background(), svc.cfg)
	if err != nil {
		return errors.Wrapf(err, "error initializing TPM config")
	}

	vms := svc.mngr.sortedVMs()
	eg, ctx := errgroup.WithContext(context.Background())

	for idx := range vms {
		if vms[idx].Type == VMTypeSwitchHW {
			continue
		}

		err := vms[idx].Prepare(ctx, svc.cfg)
		if err != nil {
			return errors.Wrapf(err, "error preparing VM %s", vms[idx].Name)
		}
	}

	for idx := range vms {
		if vms[idx].Type == VMTypeSwitchHW {
			continue
		}

		vms[idx].Run(ctx, eg, svc)
		time.Sleep(200 * time.Millisecond)
	}

	return errors.Wrapf(eg.Wait(), "error starting VMs")
}

func (svc *Service) checkResources() {
	cpu := 0
	ram := 0
	disk := 0
	for _, vm := range svc.mngr.vms {
		cpu += vm.Config.CPU
		ram += vm.Config.RAM
		disk += vm.Config.Disk
	}

	slog.Info("Total VM resources", "cpu", fmt.Sprintf("%d vCPUs", cpu), "ram", fmt.Sprintf("%d MB", ram), "disk", fmt.Sprintf("%d GB", disk))
}

func (svc *Service) VFIOPCIBindAll() error {
	checked := 0

	for _, vm := range svc.mngr.sortedVMs() {
		for _, iface := range vm.Interfaces {
			if iface.Passthrough != "" {
				checked++

				var err error
				for attempt := 0; attempt < 6; attempt++ {
					err = bindDeviceToVFIO(iface.Passthrough)
					if err == nil {
						break
					}
					time.Sleep(500 * time.Millisecond)
				}
				if err != nil {
					return errors.Wrapf(err, "error binding device %s to vfio-pci", iface.Passthrough)
				}

				slog.Debug("Device is ready (bound to vfio-pci)", "device", iface.Passthrough)
			}
		}
	}

	slog.Info("All devices are ready (bound to vfio-pci)", "devices", checked)

	return nil
}

const (
	VMSelectorSSH = "ssh"
	VMSelectorAll = "all"
)

func (svc *Service) vmSelector(name string, mode string, msg string) (*VM, error) {
	vms := []*VM{}

	for _, vm := range svc.mngr.sortedVMs() {
		if name != "" && vm.Name == name {
			return vm, nil
		}
		if name == "control" && vm.Type == VMTypeControl {
			return vm, nil
		}
		if mode == VMSelectorAll || mode == VMSelectorSSH {
			vms = append(vms, vm)
		}
	}

	templates := &promptui.SelectTemplates{
		Label:    "{{ .Name }}",
		Active:   "\U0001F994 {{ .Name | cyan }}",
		Inactive: "{{ .Name | cyan }}",
		Selected: "\U0001F994 {{ .Name | red | cyan }}",
		Details: `
----------- VM Details ------------
{{ "ID:" | faint }}	{{ .ID }}
{{ "Name:" | faint }}	{{ .Name }}
{{ "Ready:" | faint }}	{{ .Ready.Is }}
{{ "Basedir:" | faint }}	{{ .Basedir }}`,
	}

	searcher := func(input string, index int) bool {
		vm := vms[index]
		name := strings.Replace(strings.ToLower(vm.Name), " ", "", -1)
		input = strings.Replace(strings.ToLower(input), " ", "", -1)

		return strings.Contains(name, input)
	}

	prompt := promptui.Select{
		Label:     msg,
		Items:     vms,
		Templates: templates,
		Size:      20,
		Searcher:  searcher,
	}

	selected, _, err := prompt.Run()
	if err != nil {
		return nil, errors.Wrap(err, "error selecting VM")
	}

	return vms[selected], nil
}

func (svc *Service) findJumpForSwitch(name string) (string, string, error) {
	var controlVM *VM
	for _, vm := range svc.mngr.sortedVMs() {
		if vm.Type == VMTypeControl {
			controlVM = vm

			break
		}
	}

	if controlVM == nil {
		return "", "", errors.Errorf("failed to find control vm to use as jump host")
	}

	target := ""
	for _, sw := range svc.cfg.Wiring.Switch.All() {
		if sw.Name == name {
			target = sw.Spec.IP

			break
		}
	}

	if target == "" {
		return "", "", errors.Errorf("failed to find switch IP for %s", name)
	}

	target = strings.SplitN(target, "/", 2)[0] // we don't need the mask
	target = "admin@" + target
	proxyCmd := fmt.Sprintf("ssh %s -i %s -W %%h:%%p -p %d core@127.0.0.1",
		strings.Join(SSHQuietFlags, " "), svc.cfg.SSHKey, controlVM.sshPort())

	return proxyCmd, target, nil
}

func (svc *Service) SSH(name string, args []string) error {
	vm, err := svc.vmSelector(name, VMSelectorSSH, "SSH to VM:")
	if err != nil {
		return err
	}

	target := "core@127.0.0.1"
	cmdArgs := append(SSHQuietFlags, "-i", svc.cfg.SSHKey)

	if vm.Type == VMTypeControl || vm.Type == VMTypeServer {
		cmdArgs = append(cmdArgs,
			"-p", fmt.Sprintf("%d", vm.sshPort()),
		)
	} else if vm.Type == VMTypeSwitchVS || vm.Type == VMTypeSwitchHW {
		var proxyCmd string
		proxyCmd, target, err = svc.findJumpForSwitch(vm.Name)
		if err != nil {
			return err
		}
		cmdArgs = append(cmdArgs,
			"-o", "ProxyCommand="+proxyCmd,
		)
	} else {
		return errors.Errorf("unsupported VM type %s", vm.Type)
	}

	slog.Info("SSH", "vm", vm.Name)

	cmdArgs = append(cmdArgs, target)
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("ssh", cmdArgs...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	slog.Debug("Running ssh", "args", strings.Join(cmdArgs, " "))

	return errors.Wrapf(cmd.Run(), "error running ssh")
}

func (svc *Service) Serial(name string) error {
	vm, err := svc.vmSelector(name, VMSelectorAll, "Connect to VM serial console:")
	if err != nil {
		return err
	}

	slog.Info("Serial", "vm", vm.Name)

	var cmdArgs []string
	var cmd *exec.Cmd
	if vm.Type == VMTypeSwitchHW {
		if switchCfg, exists := svc.mngr.cfg.Switches[vm.Name]; exists {
			if switchCfg.Type != ConfigSwitchTypeHW {
				return errors.Errorf("switch %s expected to be HW switch but it's not", vm.Name)
			}
			if switchCfg.Serial == "" {
				return errors.Errorf("switch %s doesn't have serial console specified in vlab config", vm.Name)
			}

			parts := strings.Split(switchCfg.Serial, ":")
			if len(parts) != 2 {
				return errors.Errorf("switch %s serial console is malformed", vm.Name)
			}

			cmdArgs = []string{parts[0], parts[1]}
			cmd = exec.Command("telnet", cmdArgs...)
		} else {
			return errors.Errorf("failed to find switch config for %s", vm.Name)
		}
	} else {
		cmdArgs = []string{
			"socat",
			"-,raw,echo=0,escape=0x1d",
			fmt.Sprintf("unix-connect:%s", filepath.Join(vm.Basedir, "serial.sock")),
		}
		cmd = exec.Command("sudo", cmdArgs...)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	slog.Debug("Running socat", "args", strings.Join(cmdArgs, " "))

	slog.Warn("Use Ctrl+] to escape, if no output try Enter, safe to use Ctrl+C/Ctrl+Z")

	return errors.Wrapf(cmd.Run(), "error running socat")
}

func (svc *Service) List() error {
	_, err := svc.vmSelector("", VMSelectorAll, "Select VM for detailed info:")
	if err != nil {
		return err
	}

	return nil
}
