// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/samber/lo"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu"
	"go.githedgehog.com/fabricator/pkg/support"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
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

	// Get VM and Switch entries from VLAB
	entries, err := c.getVLABEntries(ctx, vlab)
	if err != nil {
		return fmt.Errorf("retrieving VM and switch entries: %w", err)
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

	switch t {
	case VLABAccessSSH:
		if entry.SSHPort > 0 { //nolint:gocritic
			slog.Info("SSH using local port", "name", name, "port", entry.SSHPort)

			cmdName = VLABCmdSSH
			args = append(slices.Clone(SSHQuietFlags),
				"-p", fmt.Sprintf("%d", entry.SSHPort),
				"-i", filepath.Join(VLABDir, VLABSSHKeyFile),
				"core@127.0.0.1",
			)
		} else if entry.IsSwitch {
			slog.Info("SSH through control node", "name", name, "type", "switch")

			swIP, err := c.getSwitchIP(ctx, name)
			if err != nil {
				return fmt.Errorf("getting switch IP: %w", err)
			}

			proxyCmd := fmt.Sprintf("ssh %s -i %s -W %%h:%%p -p %d core@127.0.0.1",
				strings.Join(SSHQuietFlags, " "),
				filepath.Join(VLABDir, VLABSSHKeyFile),
				getSSHPort(0), // TODO get control node ID
			)

			cmdName = VLABCmdSSH
			args = append(slices.Clone(SSHQuietFlags),
				"-i", filepath.Join(VLABDir, VLABSSHKeyFile),
				"-o", "ProxyCommand="+proxyCmd,
				"admin@"+swIP,
			)
		} else if entry.IsNode {
			slog.Info("SSH through control node", "name", name, "type", "gateway")

			nodeIP, err := c.getNodeIP(ctx, name)
			if err != nil {
				return fmt.Errorf("getting node IP: %w", err)
			}

			proxyCmd := fmt.Sprintf("ssh %s -i %s -W %%h:%%p -p %d core@127.0.0.1",
				strings.Join(SSHQuietFlags, " "),
				filepath.Join(VLABDir, VLABSSHKeyFile),
				getSSHPort(0), // TODO get control node ID
			)

			cmdName = VLABCmdSSH
			args = append(slices.Clone(SSHQuietFlags),
				"-i", filepath.Join(VLABDir, VLABSSHKeyFile),
				"-o", "ProxyCommand="+proxyCmd,
				"core@"+nodeIP,
			)
		} else {
			return fmt.Errorf("SSH not available: %s", name) //nolint:goerr113
		}

		if len(inArgs) > 0 {
			args = append(args, "PATH=$PATH:/opt/bin "+strings.Join(inArgs, " "))
		}
	case VLABAccessSerial:
		if entry.SerialSock != "" { //nolint:gocritic
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
			args = append(slices.Clone(SSHQuietFlags), "-p", parts[1], parts[0])
		} else {
			return fmt.Errorf("serial not available: %s", name) //nolint:goerr113
		}
	case VLABAccessSerialLog:
		if entry.SerialLog != "" {
			slog.Info("Serial log", "name", name, "path", entry.SerialLog)

			cmdName = VLABCmdLess
			args = []string{"-r", entry.SerialLog}
		} else {
			return fmt.Errorf("serial log not available: %s", name) //nolint:goerr113
		}
	default:
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
	IsSwitch     bool // ssh through control node only
	IsControl    bool // Needed to distinguish VM type in show-tech
	IsServer     bool
	IsNode       bool // ssh through control node only
	IsExternal   bool
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
	if err := os.WriteFile(path, []byte(reinstallScript), 0o700); err != nil { //nolint:gosec
		return cleanup, "", fmt.Errorf("failed to write reinstall script: %w", err)
	}

	return cleanup, path, nil
}

const (
	errorConsole = iota + 1
	errorLogin
	errorInstall
	errorHHFab
	errorUnknown
)

var (
	ErrConsole = errors.New("connection to console failed")
	ErrLogin   = errors.New("login to switch failed")
	ErrInstall = errors.New("os install failed")
	ErrHHFab   = errors.New("hhfab vlab serial failed")
	ErrUnknown = errors.New("unknown expect error")
)

func wrapError(switchName string, exitCode int) error {
	var baseErr error
	switch exitCode {
	case errorConsole:
		baseErr = ErrConsole
	case errorLogin:
		baseErr = ErrLogin
	case errorInstall:
		baseErr = ErrInstall
	case errorHHFab:
		baseErr = ErrHHFab
	case errorUnknown:
		baseErr = ErrUnknown
	default:
		return fmt.Errorf("%s: unknown error (exit code: %d)", switchName, exitCode) //nolint:goerr113
	}

	return fmt.Errorf("%s: %w (error code: %d)", switchName, baseErr, exitCode)
}

func (c *Config) VLABSwitchReinstall(ctx context.Context, opts SwitchReinstallOpts) error {
	start := time.Now()
	slog.Info("Reinstalling switches", "mode", opts.Mode, "switches", opts.Switches)

	_, err := exec.LookPath(VLABCmdExpect)
	if err != nil {
		return fmt.Errorf("required command %q is not available", VLABCmdExpect) //nolint:goerr113
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	switches := wiringapi.SwitchList{}
	if err := c.Client.List(ctx, &switches); err != nil {
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

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}
	if err := os.Setenv("HHFAB_BIN", self); err != nil {
		return fmt.Errorf("setting HHFAB_BIN env variable: %w", err)
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

			maxAttempts := 3
			var lastErr error

			baseBackoff := 1

			for attempt := 1; attempt <= maxAttempts; attempt++ {
				func() {
					ctx, cancel := context.WithCancel(ctx)
					defer cancel()

					args := []string{sw.Name}
					if opts.Mode == ReinstallModeReboot {
						args = append(args, opts.SwitchUsername, opts.SwitchPassword)
					}
					if opts.WaitReady {
						args = append(args, "--wait-ready")
					}
					args = append(args, "-v")

					cmd := exec.CommandContext(ctx, script, args...)
					cmd.Stdout = logutil.NewSink(ctx, slog.Debug, sw.Name+": ")
					cmd.Stderr = logutil.NewSink(ctx, slog.Debug, sw.Name+": ")

					err := cmd.Run()
					if err == nil {
						lastErr = nil

						return
					}

					var exitErr *exec.ExitError
					if errors.As(err, &exitErr) && exitErr.ExitCode() == errorConsole {
						slog.Info("Reinstall attempt failed", "attempt", attempt, "switch", sw.Name, "reason", "console connection error, initiating hard reset")
						backoffSeconds := baseBackoff * (1 << (attempt - 1))

						slog.Info("Reinstall attempt failed",
							"attempt", attempt,
							"switch", sw.Name,
							"reason", "console connection error, initiating hard reset",
							"next_retry_delay", fmt.Sprintf("%ds", backoffSeconds))

						powerOpts := SwitchPowerOpts{
							Switches:    []string{sw.Name},
							Action:      pdu.ActionCycle,
							PDUUsername: opts.PDUUsername,
							PDUPassword: opts.PDUPassword,
						}
						if err := c.VLABSwitchPower(ctx, powerOpts); err != nil {
							slog.Error("Failed to perform hard reset for switch", "name", sw.Name, "error", err)
						}

						time.Sleep(time.Second * time.Duration(backoffSeconds))
						lastErr = wrapError(sw.Name, exitErr.ExitCode())

						return
					} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						lastErr = fmt.Errorf("%s: timeout (context deadline exceeded)", sw.Name) //nolint:goerr113

						return
					}

					backoffSeconds := baseBackoff * (1 << (attempt - 1))
					slog.Info("Reinstall attempt failed with non-console error",
						"attempt", attempt,
						"switch", sw.Name,
						"next_retry_delay", fmt.Sprintf("%ds", backoffSeconds))
					time.Sleep(time.Second * time.Duration(backoffSeconds))

					lastErr = fmt.Errorf("%s: %w", sw.Name, err)
				}()

				if lastErr == nil {
					break
				}
			}

			if lastErr != nil {
				mu.Lock()
				errs = append(errs, lastErr)
				mu.Unlock()
				slog.Error("Failed to reinstall switch after retries", "name", sw.Name, "error", lastErr)
			} else {
				if opts.WaitReady {
					slog.Info("Switch reinstalled successfully", "name", sw.Name)
				} else {
					slog.Info("Switch placed into NOS Install Mode", "name", sw.Name)
				}
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
		slog.Info("Switches power cycled")
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

func (c *Config) getVLABEntries(ctx context.Context, vlab *VLAB) (map[string]VLABAccessInfo, error) {
	entries := map[string]VLABAccessInfo{}

	// Gather VM entries
	for _, vm := range vlab.VMs {
		sshPort := uint(0)

		if len(vm.NICs) > 0 && strings.Contains(vm.NICs[0], "user,") && (vm.Type == VMTypeControl || vm.Type == VMTypeServer || vm.Type == VMTypeExternal) {
			sshPort = getSSHPort(vm.ID)
		}

		vmDir := filepath.Join(VLABDir, VLABVMsDir, vm.Name)
		entries[vm.Name] = VLABAccessInfo{
			SSHPort:    sshPort,
			SerialSock: filepath.Join(vmDir, VLABSerialSock),
			SerialLog:  filepath.Join(vmDir, VLABSerialLog),
			IsSwitch:   vm.Type == VMTypeSwitch,
			IsServer:   vm.Type == VMTypeServer,
			IsControl:  vm.Type == VMTypeControl,
			IsNode:     vm.Type == VMTypeGateway,
			IsExternal: vm.Type == VMTypeExternal,
		}
	}

	// Gather switch entries
	switches := wiringapi.SwitchList{}
	if err := c.Client.List(ctx, &switches); err != nil {
		return nil, fmt.Errorf("failed to list switches: %w", err)
	}

	for _, sw := range switches.Items {
		entry := entries[sw.Name]
		entry.RemoteSerial = hhfctl.GetSerialInfo(&sw)
		entry.IsSwitch = true
		entries[sw.Name] = entry
	}

	return entries, nil
}

//go:embed show-tech/server.sh
var serverScript []byte

//go:embed show-tech/control.sh
var controlScript []byte

//go:embed show-tech/switch.sh
var switchScript []byte

//go:embed show-tech/gateway.sh
var gatewayScript []byte

//go:embed show-tech/runner.sh
var runnerScript []byte

type ShowTechScript struct {
	Scripts map[VMType][]byte
}

func DefaultShowTechScript() ShowTechScript {
	return ShowTechScript{
		Scripts: map[VMType][]byte{
			VMTypeServer:   serverScript,
			VMTypeControl:  controlScript,
			VMTypeSwitch:   switchScript,
			VMTypeGateway:  gatewayScript,
			VMTypeExternal: serverScript,
		},
	}
}

func (c *Config) VLABShowTech(ctx context.Context, vlab *VLAB) error {
	scriptConfig := DefaultShowTechScript()

	outputDir := filepath.Join(c.WorkDir, "show-tech-output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Collect runner host diagnostics first
	if err := c.collectRunnerShowTech(ctx, outputDir); err != nil {
		slog.Warn("Failed to collect runner diagnostics", "err", err)
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(vlab.VMs))

	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			slog.Warn("Context cancelled, but continuing to collect available diagnostics")
		case <-done:
			return
		}
	}()

	var successCount atomic.Int32

	for _, vm := range vlab.VMs {
		name := vm.Name
		wg.Add(1)
		go func(name string, vm VM) {
			defer wg.Done()
			ssh, err := c.SSHVM(ctx, vlab, vm)
			if err != nil {
				errChan <- fmt.Errorf("getting ssh config for entry %s: %w", name, err)

				return
			}

			collectionCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			script, ok := scriptConfig.Scripts[vm.Type]
			if !ok {
				slog.Debug("No show-tech script available for", "vm", vm.Name, "type", vm.Type)

				return
			}

			if err := c.collectShowTech(collectionCtx, name, ssh, script, outputDir); err != nil {
				errChan <- fmt.Errorf("collecting show-tech for entry %s: %w", name, err)
			} else {
				successCount.Add(1)
			}
		}(name, vm)
	}

	wg.Wait()
	close(errChan)

	errors := lo.ChannelToSlice(errChan)

	if successCount.Load() == 0 {
		return fmt.Errorf("failed to collect any diagnostics: %v", errors) //nolint:goerr113
	}

	if len(errors) > 0 {
		slog.Warn("Some diagnostics collection failed",
			"success_count", successCount.Load(),
			"total_count", len(vlab.VMs),
			"errors", errors)
	}

	slog.Info("Show tech files saved in", "folder", outputDir)

	return nil
}

func (c *Config) VLABSwitchPower(ctx context.Context, opts SwitchPowerOpts) error {
	slog.Info("Power managing switches", "action", opts.Action, "switches", opts.Switches)

	if opts.PDUUsername == "" || opts.PDUPassword == "" {
		return errors.New("PDU credentials required") //nolint:goerr113
	}

	switches := wiringapi.SwitchList{}
	if err := c.Client.List(ctx, &switches); err != nil {
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

			slog.Info("Calling PDU API", "switch", sw.Name, "psu", psuName, "pduIP", pduIP, "outletID", outletID, "action", opts.Action)
			if err := pdu.ControlOutlet(ctx, pduIP, opts.PDUUsername, opts.PDUPassword, outletID, opts.Action); err != nil {
				return fmt.Errorf("failed to power %s switch %s %s: %w", opts.Action, sw.Name, psuName, err)
			}
		}
	}

	return nil
}

func (c *Config) collectShowTech(ctx context.Context, entryName string, ssh *sshutil.Config, script []byte, outputDir string) error {
	// Determine the script for the VM type
	remoteScriptPath := "/tmp/show-tech.sh"
	remoteOutputPath := "/tmp/show-tech.log"

	tmpfile, err := os.CreateTemp("", "script-*")
	if err != nil {
		return fmt.Errorf("creating temporary script file: %w", err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	if _, err := tmpfile.Write(script); err != nil {
		return fmt.Errorf("writing script to temporary file: %w", err)
	}

	if err := tmpfile.Sync(); err != nil {
		return fmt.Errorf("syncing temporary script file: %w", err)
	}

	err = ssh.UploadPath(tmpfile.Name(), remoteScriptPath)
	if err != nil {
		return fmt.Errorf("uploading script to %s: %w", entryName, err)
	}

	chmodCmd := fmt.Sprintf("chmod +x %s && %s", remoteScriptPath, remoteScriptPath)
	chmodCtx, chmodCancel := context.WithTimeout(ctx, 150*time.Second)
	defer chmodCancel()
	_, stderr, err := ssh.Run(chmodCtx, chmodCmd)
	if err != nil {
		return fmt.Errorf("executing show-tech on %s: %w", entryName, err) //nolint:goerr113
	}

	if stderr != "" {
		slog.Debug("show-tech execution produced stderr", "entry", entryName, "stderr", stderr)
	}

	localFilePath := filepath.Join(outputDir, entryName+"-show-tech.log")
	err = ssh.DownloadPath(remoteOutputPath, localFilePath)
	if err != nil {
		return fmt.Errorf("downloading show-tech output from %s: %w", entryName, err)
	}

	// Also download the error summary file if it exists
	remoteErrorPath := "/tmp/show-tech-errors.log"
	localErrorPath := filepath.Join(outputDir, entryName+"-show-tech-errors.log")
	if err := ssh.DownloadPath(remoteErrorPath, localErrorPath); err != nil {
		// Don't fail if error file doesn't exist (older scripts might not generate it)
		slog.Debug("Error file not available", "entry", entryName, "err", err)
	} else {
		slog.Debug("Error summary collected", "entry", entryName, "output", localErrorPath)
	}

	slog.Debug("Show tech collected successfully", "entry", entryName, "output", localFilePath)

	return nil
}

func (c *Config) collectRunnerShowTech(ctx context.Context, outputDir string) error {
	// Create temp file for the runner script
	tmpfile, err := os.CreateTemp("", "runner-show-tech-*")
	if err != nil {
		return fmt.Errorf("creating temporary script file: %w", err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	if _, err := tmpfile.Write(runnerScript); err != nil {
		return fmt.Errorf("writing script to temporary file: %w", err)
	}

	if err := tmpfile.Sync(); err != nil {
		return fmt.Errorf("syncing temporary script file: %w", err)
	}

	if err := tmpfile.Close(); err != nil {
		return fmt.Errorf("closing temporary script file: %w", err)
	}

	// Make script executable
	if err := os.Chmod(tmpfile.Name(), 0o755); err != nil {
		return fmt.Errorf("making script executable: %w", err)
	}

	// Execute the script locally
	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "/bin/bash", tmpfile.Name()) //nolint:gosec // tmpfile.Name() is from os.CreateTemp, not user input
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Warn("Runner show-tech script execution failed", "err", err, "output", string(output))
		// Don't return error, save whatever output we got
	}

	// Save output to show-tech-output directory
	localFilePath := filepath.Join(outputDir, "runner-show-tech.log")
	if err := os.WriteFile(localFilePath, output, 0o600); err != nil {
		return fmt.Errorf("writing runner show-tech output: %w", err)
	}

	slog.Debug("Runner show-tech collected successfully", "output", localFilePath)

	return nil
}

func (c *Config) getSwitchIP(ctx context.Context, entryName string) (string, error) {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	kube, err := kubeutil.NewClient(ctx, kubeconfig, wiringapi.SchemeBuilder)
	if err != nil {
		return "", fmt.Errorf("creating kube client: %w", err)
	}

	sw := &wiringapi.Switch{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: entryName, Namespace: kmetav1.NamespaceDefault}, sw); err != nil {
		return "", fmt.Errorf("getting switch object: %w", err) //nolint:goerr113
	}

	if sw.Spec.IP == "" {
		return "", fmt.Errorf("switch IP not found: %s", entryName) //nolint:goerr113
	}

	swIP, err := netip.ParsePrefix(sw.Spec.IP)
	if err != nil {
		return "", fmt.Errorf("parsing switch IP: %w", err)
	}

	return swIP.Addr().String(), nil
}

func (c *Config) getNodeIP(ctx context.Context, name string) (string, error) {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	kube, err := kubeutil.NewClient(ctx, kubeconfig, fabapi.SchemeBuilder)
	if err != nil {
		return "", fmt.Errorf("creating kube client: %w", err)
	}

	node := &fabapi.FabNode{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: comp.FabNamespace}, node); err != nil {
		return "", fmt.Errorf("getting node object: %w", err) //nolint:goerr113
	}

	if node.Spec.Management.IP == "" {
		return "", fmt.Errorf("node mgmt IP not found: %s", name) //nolint:goerr113
	}

	nodeIP, err := node.Spec.Management.IP.Parse()
	if err != nil {
		return "", fmt.Errorf("parsing node mgmt IP: %w", err)
	}

	return nodeIP.Addr().String(), nil
}

func (c *Config) CollectVLABDebug(ctx context.Context, vlab *VLAB, opts VLABRunOpts) {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)

	if _, err := os.Stat(kubeconfig); errors.Is(err, fs.ErrNotExist) {
		for _, vm := range vlab.VMs {
			if vm.Type == VMTypeControl {
				ssh, err := c.SSHVM(ctx, vlab, vm)
				if err != nil {
					slog.Warn("Failed to setup ssh to copy kubeconfig", "vm", vm.Name)

					break
				}

				if err := ssh.DownloadPath(k3s.KubeConfigPath, kubeconfig); err != nil {
					slog.Warn("Failed to download kubeconfig", "vm", vm.Name)
				}

				break
			}
		}
	}

	if dump, err := support.Collect(ctx, "vlab", kubeconfig); err != nil {
		slog.Warn("Failed to collect support dump", "err", err)
	} else {
		if data, err := support.Marshal(dump); err != nil {
			slog.Warn("Failed to marshal support dump", "err", err)
		} else {
			if err := os.WriteFile(filepath.Join(c.WorkDir, "vlab.hhs"), data, 0o644); err != nil { //nolint:gosec
				slog.Warn("Failed to write support dump", "err", err)
			}
		}
	}

	if opts.CollectShowTech {
		if err := c.VLABShowTech(ctx, vlab); err != nil {
			slog.Warn("Failed to collect show-tech diagnostics", "err", err)
		}
	}
}
