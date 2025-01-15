// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/manifoldco/promptui"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VLABAccessType string

const (
	VLABAccessSSH       VLABAccessType = "ssh"
	VLABAccessSerial    VLABAccessType = "serial"
	VLABAccessSerialLog VLABAccessType = "serial log"
)

var SSHQuietFlags = []string{
	"-o", "GlobalKnownHostsFile=/dev/null",
	"-o", "UserKnownHostsFile=/dev/null",
	"-o", "StrictHostKeyChecking=no",
	"-o", "LogLevel=ERROR",
}

func (c *Config) VLABAccess(ctx context.Context, vlab *VLAB, t VLABAccessType, name string, inArgs []string) error {
	if len(inArgs) > 0 && t != VLABAccessSSH {
		return fmt.Errorf("arguments only supported for ssh") //nolint:goerr113
	}

	if err := c.checkForBins(); err != nil {
		return err
	}

	entries := map[string]VLABAccessInfo{}
	for _, vm := range vlab.VMs {
		sshPort := uint(0)

		if len(vm.NICs) > 0 && strings.Contains(vm.NICs[0], "user,") && (vm.Type == VMTypeControl || vm.Type == VMTypeServer) {
			sshPort = getSSHPort(vm.ID)
		}

		vmDir := filepath.Join(VLABDir, VLABVMsDir, vm.Name)
		entries[vm.Name] = VLABAccessInfo{
			SSHPort:    sshPort,
			SerialSock: filepath.Join(vmDir, VLABSerialSock),
			SerialLog:  filepath.Join(vmDir, VLABSerialLog),
			IsSwitch:   vm.Type == VMTypeSwitch,
		}
	}

	switches := wiringapi.SwitchList{}
	if err := c.Wiring.List(ctx, &switches); err != nil {
		return fmt.Errorf("failed to list switches: %w", err)
	}

	for _, sw := range switches.Items {
		entry := entries[sw.Name]
		entry.RemoteSerial = hhfctl.GetSerialInfo(&sw)
		entry.IsSwitch = true
		entries[sw.Name] = entry
	}

	if name == "" {
		names := []string{}

		for name := range entries {
			names = append(names, name)
		}

		slices.Sort(names)

		prompt := promptui.Select{
			Label: fmt.Sprintf("Select target for %s:", t),
			Items: names,
			Templates: &promptui.SelectTemplates{
				Label:    "{{ . }}",
				Active:   "\U0001F994 {{ . | cyan }}",
				Inactive: "{{ . | cyan }}",
				Selected: "\U0001F994 {{ . | red | cyan }}",
			},
			Size: 20,
			Searcher: func(input string, index int) bool {
				name := names[index]
				name = strings.ReplaceAll(strings.ToLower(name), " ", "")
				input = strings.ReplaceAll(strings.ToLower(input), " ", "")

				return strings.Contains(name, input)
			},
		}

		selected, _, err := prompt.Run()
		if err != nil {
			return fmt.Errorf("failed to select: %w", err)
		}

		name = names[selected]
	}

	entry, ok := entries[name]
	if !ok {
		return fmt.Errorf("access info not found: %s", name) //nolint:goerr113
	}

	sudo := false
	cmdName := ""
	var args []string

	if t == VLABAccessSSH {
		if entry.SSHPort > 0 {
			slog.Info("SSH using local port", "name", name, "port", entry.SSHPort)

			cmdName = VLABCmdSSH
			args = append(SSHQuietFlags,
				"-p", fmt.Sprintf("%d", entry.SSHPort),
				"-i", filepath.Join(VLABDir, VLABSSHKeyFile),
				"core@127.0.0.1",
			)
		} else if entry.IsSwitch {
			slog.Info("SSH through control node", "name", name, "type", "switch")

			kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
			kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig, wiringapi.SchemeBuilder)
			if err != nil {
				return fmt.Errorf("creating kube client: %w", err)
			}

			sw := &wiringapi.Switch{}
			if err := kube.Get(ctx, client.ObjectKey{Name: name, Namespace: metav1.NamespaceDefault}, sw); err != nil {
				return fmt.Errorf("getting switch: %w", err)
			}

			if sw.Spec.IP == "" {
				return fmt.Errorf("switch IP not found: %s", name) //nolint:goerr113
			}

			swIP, err := netip.ParsePrefix(sw.Spec.IP)
			if err != nil {
				return fmt.Errorf("parsing switch IP: %w", err)
			}

			proxyCmd := fmt.Sprintf("ssh %s -i %s -W %%h:%%p -p %d core@127.0.0.1",
				strings.Join(SSHQuietFlags, " "),
				filepath.Join(VLABDir, VLABSSHKeyFile),
				getSSHPort(0), // TODO get control node ID
			)

			cmdName = VLABCmdSSH
			args = append(SSHQuietFlags,
				"-i", filepath.Join(VLABDir, VLABSSHKeyFile),
				"-o", "ProxyCommand="+proxyCmd,
				"admin@"+swIP.Addr().String(),
			)
		} else {
			return fmt.Errorf("SSH not available: %s", name) //nolint:goerr113
		}

		if len(inArgs) > 0 {
			args = append(args, "PATH=$PATH:/opt/bin "+strings.Join(inArgs, " "))
		}
	} else if t == VLABAccessSerial {
		if entry.SerialSock != "" {
			slog.Info("Serial using local socket", "name", name, "path", entry.SerialSock)

			sudo = true
			cmdName = VLABCmdSocat
			args = []string{
				"-,raw,echo=0,escape=0x1d",
				fmt.Sprintf("unix-connect:%s", entry.SerialSock),
			}
			slog.Info("Use Ctrl+] to escape, if no output try Enter, safe to use Ctrl+C/Ctrl+Z")
		} else if entry.RemoteSerial != "" {
			slog.Info("Remote serial (hardware)", "name", name, "remote", entry.RemoteSerial)

			parts := strings.SplitN(entry.RemoteSerial, ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid remote serial (expected host:port): %s", entry.RemoteSerial) //nolint:goerr113
			}

			cmdName = VLABCmdSSH
			args = append(SSHQuietFlags, "-p", parts[1], parts[0])
		} else {
			return fmt.Errorf("Serial not available: %s", name) //nolint:goerr113
		}
	} else if t == VLABAccessSerialLog {
		if entry.SerialLog != "" {
			slog.Info("Serial log", "name", name, "path", entry.SerialLog)

			cmdName = VLABCmdLess
			args = []string{"-r", entry.SerialLog}
		} else {
			return fmt.Errorf("Serial log not available: %s", name) //nolint:goerr113
		}
	} else {
		return fmt.Errorf("unknown access type: %s", t) //nolint:goerr113
	}

	slog.Debug("Running", "cmd", strings.Join(append([]string{cmdName}, args...), " "))

	if sudo {
		args = append([]string{cmdName}, args...)
		cmdName = VLABCmdSudo
	}

	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = c.WorkDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run command: %w", err)
	}

	return nil
}

type VLABAccessInfo struct {
	SSHPort      uint // local ssh port
	SerialSock   string
	SerialLog    string
	IsSwitch     bool   // ssh through control node only
	RemoteSerial string // ssh to get serial
}

//go:embed vlabhelpers_reinstall.exp
var reinstallScript string

func (c *Config) prepareReinstallScript() (func(), string, error) {
	dir, err := os.MkdirTemp(c.CacheDir, "vlabhelpers_reinstall-*")
	if err != nil {
		return nil, "", fmt.Errorf("creating temp dir for reinstall script: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	path := filepath.Join(dir, "vlabhelpers_reinstall.exp")
	if err := os.WriteFile(path, []byte(reinstallScript), 0o755); err != nil { //nolint:gosec
		return cleanup, "", fmt.Errorf("failed to write reinstall script: %w", err)
	}

	return cleanup, path, nil
}

func (c *Config) VLABSwitchReinstall(ctx context.Context, opts SwitchReinstallOpts) error {
	start := time.Now()

	slog.Info("Reinstalling switches", "mode", opts.Mode, "switches", opts.Switches)

	_, err := exec.LookPath(VLABCmdExpect)
	if err != nil {
		return fmt.Errorf("required command %q is not available", VLABCmdExpect) //nolint:goerr113
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	switches := wiringapi.SwitchList{}
	if err := c.Wiring.List(ctx, &switches); err != nil {
		return fmt.Errorf("failed to list switches: %w", err)
	}

	if len(opts.Switches) > 0 {
		sws := map[string]bool{}
		for _, sw := range switches.Items {
			sws[sw.Name] = true
		}

		for _, sw := range opts.Switches {
			if !sws[sw] {
				return fmt.Errorf("switch not found: %s", sw) //nolint:goerr113
			}
		}
	}

	cleanup, script, err := c.prepareReinstallScript()
	if err != nil {
		if cleanup != nil {
			cleanup()
		}

		return err
	}
	defer cleanup()

	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex

	for _, sw := range switches.Items {
		if len(opts.Switches) > 0 && !slices.Contains(opts.Switches, sw.Name) {
			continue
		}

		wg.Add(1)
		go func(sw wiringapi.Switch) {
			defer wg.Done()

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			args := []string{sw.Name}
			if opts.Mode == ReinstallModeReboot {
				args = append(args, opts.SwitchUsername, opts.SwitchPassword)
			}
			if opts.WaitReady {
				args = append(args, "--wait-ready")
			}

			cmd := exec.CommandContext(ctx, script, args...)
			cmd.Stdout = logutil.NewSink(ctx, slog.Debug, sw.Name+": ")
			cmd.Stderr = logutil.NewSink(ctx, slog.Debug, sw.Name+": ")

			if err := cmd.Run(); err != nil {
				mu.Lock()
				defer mu.Unlock()

				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
					errs = append(errs, fmt.Errorf("%s: timeout (context deadline exceeded)", sw.Name)) //nolint:goerr113
				} else if exitErr != nil {
					errs = append(errs, fmt.Errorf("%s: killed by signal: %w", sw.Name, exitErr)) //nolint:goerr113
				} else {
					errs = append(errs, fmt.Errorf("%s: %w", sw.Name, err)) //nolint:goerr113
				}

				slog.Error("Failed to reinstall switch", "name", sw.Name, "error", err)

				return
			}

			if opts.WaitReady {
				slog.Info("Switch reinstalled successfully", "name", sw.Name)
			} else {
				slog.Info("Switch placed into NOS Install Mode", "name", sw.Name)
			}
		}(sw)
	}

	if opts.Mode == ReinstallModeHardReset {
		time.Sleep(1 * time.Second)

		if err := c.VLABSwitchPower(ctx, SwitchPowerOpts{
			Switches:    opts.Switches,
			Action:      pdu.ActionCycle,
			PDUUsername: opts.PDUUsername,
			PDUPassword: opts.PDUPassword,
		}); err != nil {
			return fmt.Errorf("executing hard-reset on switches: %w", err)
		}
	}

	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("reinstalling switches: %w", errors.Join(errs...))
	}

	if opts.WaitReady {
		slog.Info("All switches reinstalled successfully", "took", time.Since(start))
	} else {
		slog.Info("All switches placed into NOS Install Mode", "took", time.Since(start))
	}

	return nil
}

func (c *Config) VLABSwitchPower(ctx context.Context, opts SwitchPowerOpts) error {
	slog.Info("Power managing switches", "action", opts.Action, "switches", opts.Switches)

	if opts.PDUUsername == "" || opts.PDUPassword == "" {
		return errors.New("PDU credentials required") //nolint:goerr113
	}

	switches := wiringapi.SwitchList{}
	if err := c.Wiring.List(ctx, &switches); err != nil {
		return fmt.Errorf("failed to list switches: %w", err)
	}

	if len(opts.Switches) > 0 {
		sws := map[string]bool{}
		for _, sw := range switches.Items {
			sws[sw.Name] = true
		}

		for _, sw := range opts.Switches {
			if !sws[sw] {
				return fmt.Errorf("switch not found: %s", sw) //nolint:goerr113
			}
		}
	}

	for _, sw := range switches.Items {
		if len(opts.Switches) > 0 && !slices.Contains(opts.Switches, sw.Name) {
			continue
		}

		powerInfo := hhfctl.GetPowerInfo(&sw)
		if len(powerInfo) == 0 {
			return fmt.Errorf("no power info found for switch: %s", sw.Name) //nolint:goerr113
		}

		for psuName, url := range powerInfo {
			outletID, err := pdu.ExtractOutletID(url)
			if err != nil {
				return fmt.Errorf("extracting outlet ID from URL %s: %w", url, err)
			}
			pduIP, err := pdu.GetPDUIPFromURL(url)
			if err != nil {
				return fmt.Errorf("extracting PDU IP from URL %s: %w", url, err)
			}

			slog.Debug("Calling PDU API", "switch", sw.Name, "psu", psuName, "action", opts.Action)
			if err := pdu.ControlOutlet(ctx, pduIP, opts.PDUUsername, opts.PDUPassword, outletID, opts.Action); err != nil {
				return fmt.Errorf("failed to power %s switch %s %s: %w", opts.Action, sw.Name, psuName, err)
			}
		}
	}

	return nil
}
