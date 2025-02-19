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
	"sync/atomic"
	"time"

	"github.com/appleboy/easyssh-proxy"
	"github.com/manifoldco/promptui"
	"github.com/samber/lo"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
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
			args = append(SSHQuietFlags,
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
			args = append(SSHQuietFlags,
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
	ErrConsole = errors.New("Connection to console failed")
	ErrLogin   = errors.New("Login to switch failed")
	ErrInstall = errors.New("OS Install failed")
	ErrHHFab   = errors.New("hhfab vlab serial failed")
	ErrUnknown = errors.New("Unknown expect error")
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
		return fmt.Errorf("%s: Unknown error (exit code: %d)", switchName, exitCode) //nolint:goerr113
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
	if err := c.Wiring.List(ctx, &switches); err != nil {
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

type ShowTechScript struct {
	Scripts map[VMType][]byte
}

func DefaultShowTechScript() ShowTechScript {
	return ShowTechScript{
		Scripts: map[VMType][]byte{
			VMTypeServer:   serverScript,
			VMTypeControl:  controlScript,
			VMTypeSwitch:   switchScript,
			VMTypeGateway:  serverScript, // TODO add gateway script
			VMTypeExternal: serverScript, // TODO add external script
		},
	}
}

func (c *Config) VLABShowTech(ctx context.Context, vlab *VLAB) error {
	entries, err := c.getVLABEntries(ctx, vlab)
	if err != nil {
		return fmt.Errorf("retrieving VM and switch entries: %w", err)
	}

	scriptConfig := DefaultShowTechScript()

	outputDir := filepath.Join(c.WorkDir, "show-tech-output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(entries))

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

	for name, entry := range entries {
		wg.Add(1)
		go func(name string, entry VLABAccessInfo) {
			defer wg.Done()

			collectionCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			if err := c.collectShowTech(collectionCtx, name, entry, scriptConfig, outputDir); err != nil {
				errChan <- fmt.Errorf("collecting show-tech for entry %s: %w", name, err)
			} else {
				successCount.Add(1)
			}
		}(name, entry)
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
			"total_count", len(entries),
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

			slog.Info("Calling PDU API", "switch", sw.Name, "psu", psuName, "pduIP", pduIP, "outletID", outletID, "action", opts.Action)
			if err := pdu.ControlOutlet(ctx, pduIP, opts.PDUUsername, opts.PDUPassword, outletID, opts.Action); err != nil {
				return fmt.Errorf("failed to power %s switch %s %s: %w", opts.Action, sw.Name, psuName, err)
			}
		}
	}

	return nil
}

func (c *Config) collectShowTech(ctx context.Context, entryName string, entry VLABAccessInfo, scriptConfig ShowTechScript, outputDir string) error {
	// Determine the script for the VM type
	vmType := getVMType(entry)
	script, ok := scriptConfig.Scripts[vmType]
	if !ok {
		slog.Debug("No show-tech script available for", "entry", entryName, "type", vmType)

		return nil // Skip entries with no defined script
	}

	remoteScriptPath := "/tmp/show-tech.sh"
	remoteOutputPath := "/tmp/show-tech.log"

	ssh, err := c.createSSHConfig(ctx, entryName, entry)
	if err != nil {
		return fmt.Errorf("creating SSH config for %s: %w", entryName, err)
	}

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

	err = ssh.Scp(tmpfile.Name(), remoteScriptPath)
	if err != nil {
		return fmt.Errorf("uploading script to %s: %w", entryName, err)
	}

	chmodCmd := fmt.Sprintf("chmod +x %s && %s", remoteScriptPath, remoteScriptPath)
	stdout, stderr, done, err := ssh.Run(chmodCmd, 150*time.Second)
	if err != nil {
		return fmt.Errorf("executing show-tech on %s: %w", entryName, err) //nolint:goerr113
	}

	if !done {
		return fmt.Errorf("show-tech execution timed out on %s: stdout: %s, stderr: %s", entryName, stdout, stderr) //nolint:goerr113
	}

	if stderr != "" {
		slog.Debug("show-tech execution produced stderr", "entry", entryName, "stderr", stderr)
	}

	localFilePath := filepath.Join(outputDir, entryName+"-show-tech.log")
	localFile, err := os.Create(localFilePath)
	if err != nil {
		slog.Error("Failed to create local file", "entry", entryName, "error", err)

		return fmt.Errorf("failed to create local file for %s: %w", entryName, err)
	}
	defer localFile.Close()

	// Run cat command to fetch remote file contents (no scp download available in easy-ssh)
	stdoutChan, stderrChan, doneChan, errChan, err := ssh.Stream(fmt.Sprintf("cat %s", remoteOutputPath), 60*time.Second)
	if err != nil {
		slog.Error("Failed to connect", "entry", entryName, "error", err)

		return fmt.Errorf("failed to connect to %s: %w", entryName, err)
	}

	// Read remote file contents and write to local file
	isTimeout := true
loop:
	for {
		select {
		case isTimeout = <-doneChan:

			break loop
		case line := <-stdoutChan:
			_, writeErr := localFile.WriteString(line + "\n")
			if writeErr != nil {
				slog.Error("Failed to write to local file", "entry", entryName, "error", writeErr)

				return fmt.Errorf("failed to write to local file for %s: %w", entryName, writeErr)
			}
		case errLine := <-stderrChan:
			if errLine != "" {
				fmt.Fprintf(os.Stderr, "%s: %s\n", entryName, errLine)
			}
		case err = <-errChan:
			if err != nil {
				slog.Error("Error while reading from remote file", "entry", entryName, "error", err)

				break loop
			}

			break loop
		}
	}

	if !isTimeout {
		slog.Error("Timeout occurred while fetching file", "entry", entryName)

		return fmt.Errorf("timeout occurred while fetching file from %s", entryName) //nolint:goerr113
	}

	slog.Debug("Show tech collected successfully", "entry", entryName, "output", localFilePath)

	return nil
}

func (c *Config) createSSHConfig(ctx context.Context, entryName string, entry VLABAccessInfo) (*easyssh.MakeConfig, error) {
	sshKeyPath := filepath.Join(VLABDir, VLABSSHKeyFile)

	if _, err := os.Stat(sshKeyPath); err != nil {
		return nil, fmt.Errorf("SSH key not found at %s: %w", sshKeyPath, err)
	}

	if entry.SSHPort > 0 {
		return &easyssh.MakeConfig{
			User:    "core",
			Server:  "127.0.0.1",
			Port:    fmt.Sprintf("%d", entry.SSHPort),
			KeyPath: sshKeyPath,
			Timeout: 60 * time.Second,
		}, nil
	}

	if entry.IsSwitch {
		swIP, err := c.getSwitchIP(ctx, entryName)
		if err != nil {
			return nil, fmt.Errorf("getting switch IP: %w", err)
		}

		controlPort := getSSHPort(0)
		if controlPort == 0 {
			return nil, fmt.Errorf("invalid control node port (0) for %s", entryName) //nolint:goerr113
		}

		return &easyssh.MakeConfig{
			User:    "admin",
			Server:  swIP,
			Port:    "22",
			KeyPath: sshKeyPath,
			Timeout: 60 * time.Second,
			Proxy: easyssh.DefaultConfig{
				User:    "core",
				Server:  "127.0.0.1",
				Port:    fmt.Sprintf("%d", controlPort),
				KeyPath: sshKeyPath,
				Timeout: 60 * time.Second,
			},
		}, nil
	}

	return nil, fmt.Errorf("unsupported entry type for %s", entryName) //nolint:goerr113
}

func (c *Config) getSwitchIP(ctx context.Context, entryName string) (string, error) {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	kube, err := kubeutil.NewClient(ctx, kubeconfig, wiringapi.SchemeBuilder)
	if err != nil {
		return "", fmt.Errorf("creating kube client: %w", err)
	}

	sw := &wiringapi.Switch{}
	if err := kube.Get(ctx, client.ObjectKey{Name: entryName, Namespace: metav1.NamespaceDefault}, sw); err != nil {
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

	node := &fabapi.Node{}
	if err := kube.Get(ctx, client.ObjectKey{Name: name, Namespace: comp.FabNamespace}, node); err != nil {
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

func getVMType(entry VLABAccessInfo) VMType {
	switch {
	case entry.IsSwitch:
		return VMTypeSwitch
	case entry.IsControl:
		return VMTypeControl
	case entry.IsServer:
		return VMTypeServer
	case entry.IsExternal:
		return VMTypeExternal
	default:
		return ""
	}
}
