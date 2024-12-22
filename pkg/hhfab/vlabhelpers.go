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
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu/netio"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu/utils"
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

//go:embed grub-selector.exp
var grubSelectorScript string

const VLABCmdGrubSelect string = "./grub-selector.exp"

const AllSwitches string = "ALL"

func copyGrubSelectScript() error {
	scriptPath := "./grub-selector.exp"

	existingContent, err := os.ReadFile(scriptPath)
	if err == nil {
		// Compare the content with the embedded script
		if string(existingContent) == grubSelectorScript {
			// No changes, skip writing the file
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read existing script: %w", err)
	}

	if err := os.WriteFile(scriptPath, []byte(grubSelectorScript), 0755); err != nil { //nolint:gosec
		return fmt.Errorf("failed to write script: %w", err)
	}

	return nil
}

func (c *Config) SwitchReinstall(ctx context.Context, name, mode, user, password string, verbose bool, pduConf *PDUConfig) error {
	// List switches
	switches := wiringapi.SwitchList{}
	if err := c.Wiring.List(ctx, &switches); err != nil {
		return fmt.Errorf("failed to list switches: %w", err)
	}

	// Filter switches
	var targets []wiringapi.Switch
	if name == AllSwitches {
		targets = switches.Items
	} else {
		for _, sw := range switches.Items {
			if sw.Name == name {
				targets = append(targets, sw)

				break
			}
		}
	}

	if len(targets) == 0 {
		return fmt.Errorf("no switches found for the given name: %s", name) //nolint:goerr113
	}

	if err := copyGrubSelectScript(); err != nil {
		fmt.Println("Error copying script:", err)

		return err
	}
	// Run commands in parallel
	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	start := time.Now()

	for _, sw := range targets {
		wg.Add(1)
		go func(sw wiringapi.Switch) {
			defer wg.Done()

			cmdName := VLABCmdGrubSelect
			cmd := &exec.Cmd{}
			cmd.Dir = c.WorkDir
			cmd.Stdin = os.Stdin
			var args []string

			if mode == "reload" {
				args = []string{sw.Name, user, password}
			}
			if mode == "soft-reset" || mode == "hard-reset" {
				args = []string{sw.Name}
			}

			if verbose {
				_, err := exec.LookPath("byobu")
				if err == nil {
					// byobu exists, use it to run the command
					byobuCmd := fmt.Sprintf("byobu new-window -d -n %s '%s %s'", sw.Name, cmdName, strings.Join(args, " "))
					cmd = exec.CommandContext(ctx, "sh", "-c", byobuCmd)
					cmd.Stdout = os.Stdout
					cmd.Stderr = nil
				} else {
					cmd = exec.CommandContext(ctx, cmdName, args...)
					cmd.Stdout = os.Stdout
					cmd.Stderr = nil
				}
			} else {
				cmd = exec.CommandContext(ctx, cmdName, args...)
				cmd.Stdout = nil
				cmd.Stderr = os.Stderr // expect messages logged to stderr to show progress
			}

			slog.Debug("Running cmd " + cmdName + " " + strings.Join(args, " ") + "...")
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
			}
		}(sw)
	}

	if mode == "hard-reset" {
		for _, sw := range targets {
			time.Sleep(1 * time.Second)
			slog.Info("Executing hard-reset on", "switch", sw.Name+"...")
			_ = c.VLABPower(ctx, sw.Name, "CYCLE", pduConf)
		}
	}

	if mode == "soft-reset" {
		for _, sw := range targets {
			time.Sleep(1 * time.Second)
			slog.Info("Executing soft-reset on", "switch", sw.Name+"...")
			_ = hhfctl.SwitchPowerReset(ctx, sw.Name)
		}
	}
	wg.Wait()

	if len(errs) > 0 {
		elapsed := time.Since(start)

		return fmt.Errorf("failed switches: %v (took %v)", errs, elapsed) //nolint:goerr113
	}

	if verbose {
		fmt.Println("Switch reinstall running in Byobu tabs until completed. Switch to them to check progress (Hit F4).")
	} else {
		if name == AllSwitches {
			fmt.Println("All switches reinstalled successfully.", "took", time.Since(start))
		} else {
			fmt.Println("Switch", name, "reinstalled successfully.", "took", time.Since(start))
		}
	}

	return nil
}

func (c *Config) VLABPower(ctx context.Context, name string, action string, pduConf *PDUConfig) error {
	entries := map[string]VLABPowerInfo{}

	// Fetch the switch list
	switches := wiringapi.SwitchList{}
	if err := c.Wiring.List(ctx, &switches); err != nil {
		return fmt.Errorf("Failed to list switches: %w", err)
	}

	// Populate entries
	foundAnnotations := false
	for _, sw := range switches.Items {
		// Skip if the name is not "--all" and doesn't match sw.Name
		if name != "--all" && sw.Name != name {
			continue
		}
		powerInfo := hhfctl.GetPowerInfo(&sw)
		if len(powerInfo) > 0 {
			foundAnnotations = true
		}
		slog.Debug("Switch", "sw", sw.Name, "annotations", fmt.Sprintf("%v", powerInfo))

		entry := VLABPowerInfo{
			SwitchPSUs: powerInfo,
		}
		entries[sw.Name] = entry
	}

	if len(entries) == 0 {
		return fmt.Errorf("no switches found for the given name: %s", name) //nolint:goerr113
	}

	if !foundAnnotations {
		targetSW := name
		if name == "--all" {
			targetSW = "any switches"
		}

		return fmt.Errorf("no annotations found for %s", targetSW) //nolint:goerr113
	}

	// Power action request to PDU API
	for swName, entry := range entries {
		for psuName, url := range entry.SwitchPSUs {
			outletID, err := utils.ExtractOutletID(url)
			if err != nil {
				return fmt.Errorf("error extracting outlet ID from URL %s", url) //nolint:goerr113
			}
			pduIP, err := utils.GetPDUIPFromURL(url)
			if err != nil {
				return fmt.Errorf("error extracting PDU IP from URL %s", url) //nolint:goerr113
			}
			// Query credentials from PDU config
			creds, found := pduConf.PDUs[pduIP]
			if !found {
				slog.Error("no credentials found for", "pduIP", pduIP)

				continue
			}
			slog.Info("Performing power", "action", action, "on switch", swName, "psu", psuName)
			if err := netio.ControlOutlet(ctx, pduIP, creds.User, creds.Password, outletID, action); err != nil {
				return fmt.Errorf("failed to power %s switch %s %s: %w", action, swName, psuName, err)
			}
		}
	}

	return nil
}

type VLABPowerInfo struct {
	SwitchPSUs map[string]string // PSU names and their URLs
}
