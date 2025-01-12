// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	_ "embed"
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

	"github.com/appleboy/easyssh-proxy"
	"github.com/manifoldco/promptui"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
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
	RemoteSerial string // ssh to get serial
}

func (c *Config) getVLABEntries(ctx context.Context, vlab *VLAB) (map[string]VLABAccessInfo, error) {
	entries := map[string]VLABAccessInfo{}

	// Gather VM entries
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
			IsServer:   vm.Type == VMTypeServer,
			IsControl:  vm.Type == VMTypeControl,
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

// ShowTechScript represents scripts for different VM types
type ShowTechScript struct {
	Scripts map[VMType][]byte
}

// DefaultShowTechScript initializes scripts for VM types
func DefaultShowTechScript() ShowTechScript {
	return ShowTechScript{
		Scripts: map[VMType][]byte{
			VMTypeServer:  serverScript,
			VMTypeControl: controlScript,
			VMTypeSwitch:  switchScript,
		},
	}
}

func (c *Config) VLABShowTech(ctx context.Context, vlab *VLAB) error {
	// Get VM and Switch entries from VLAB
	entries, err := c.getVLABEntries(ctx, vlab)
	if err != nil {
		return fmt.Errorf("retrieving VM and switch entries: %w", err)
	}

	// Initialize the ShowTechScript
	scriptConfig := DefaultShowTechScript()

	// Create the output directory
	outputDir := filepath.Join(c.WorkDir, "show-tech-output")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Channel for errors and WaitGroup for concurrency
	var wg sync.WaitGroup
	errChan := make(chan error, len(entries))

	// Iterate over entries
	for name, entry := range entries {
		wg.Add(1)
		go func(name string, entry VLABAccessInfo) {
			defer wg.Done()

			// Pass entry, scriptConfig, and outputDir to collectShowTech
			if err := c.collectShowTech(ctx, name, entry, scriptConfig, outputDir); err != nil {
				errChan <- fmt.Errorf("collecting show-tech for entry %s: %w", name, err)
			}
		}(name, entry)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errChan)

	// Collect and handle errors
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return fmt.Errorf("show-tech encountered errors: %v", errors) //nolint:goerr113
	}
	slog.Info("Show tech files saved in", "folder", outputDir)

	return nil
}

func (c *Config) collectShowTech(ctx context.Context, entryName string, entry VLABAccessInfo, scriptConfig ShowTechScript, outputDir string) error {
	// Determine the script for the VM type
	vmType := getVMType(entry)
	script, ok := scriptConfig.Scripts[vmType]
	if !ok {
		return nil // Skip entries with no defined script
	}

	// Remote paths
	remoteScriptPath := "/tmp/show-tech.sh"
	remoteOutputPath := "/tmp/show-tech.log"

	// Execute remote commands and file transfers using easyssh-proxy //nolint:goerr113
	ssh, err := c.createSSHConfig(ctx, entryName, entry)
	if err != nil {
		return fmt.Errorf("creating SSH config for %s: %w", entryName, err)
	}

	// Create temporary file for the script
	tmpfile, err := os.CreateTemp("", "script-*")
	if err != nil {
		return fmt.Errorf("creating temporary script file: %w", err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	// Write script content to temporary file
	if _, err := tmpfile.Write(script); err != nil {
		return fmt.Errorf("writing script to temporary file: %w", err)
	}

	if err := tmpfile.Sync(); err != nil {
		return fmt.Errorf("syncing temporary script file: %w", err)
	}

	// Upload the script from temporary file
	err = ssh.Scp(tmpfile.Name(), remoteScriptPath)
	if err != nil {
		return fmt.Errorf("uploading script to %s: %w", entryName, err)
	}

	// Make script executable and run it
	chmodCmd := fmt.Sprintf("chmod +x %s && %s", remoteScriptPath, remoteScriptPath)
	stdout, stderr, done, err := ssh.Run(chmodCmd, 60*time.Second)
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
		fmt.Printf("Failed to create local file for %s: %v\n", entryName, err)

		return fmt.Errorf("failed to create local file for %s: %w", entryName, err)
	}
	defer localFile.Close()

	// Run cat command to fetch remote file contents
	stdoutChan, stderrChan, doneChan, errChan, err := ssh.Stream(fmt.Sprintf("cat %s", remoteOutputPath), 60*time.Second)
	if err != nil {
		fmt.Printf("Failed to connect to %s: %v\n", entryName, err)

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
				fmt.Printf("Failed to write to local file for %s: %v\n", entryName, writeErr)

				return fmt.Errorf("failed to write to local file for %s: %w", entryName, writeErr)
			}
		case errLine := <-stderrChan:
			if errLine != "" {
				fmt.Fprintf(os.Stderr, "%s: %s\n", entryName, errLine)
			}
		case err = <-errChan:
			if err != nil {
				fmt.Printf("Error while reading from %s: %v\n", entryName, err)

				break loop
			}

			break loop
		}
	}

	// Handle timeout or errors
	if !isTimeout {
		fmt.Printf("Timeout occurred while fetching file from %s\n", entryName)

		return fmt.Errorf("timeout occurred while fetching file from %s", entryName) //nolint:goerr113
	}

	slog.Debug("Show tech collected successfully", "entry", entryName, "output", localFilePath)

	return nil
}

// Helper to create an SSH client for the given entry
func (c *Config) createSSHConfig(ctx context.Context, entryName string, entry VLABAccessInfo) (*easyssh.MakeConfig, error) {
	sshKeyPath := filepath.Join(VLABDir, VLABSSHKeyFile)

	// Verify SSH key exists
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

		// Get control node port with validation
		controlPort := getSSHPort(0)
		if controlPort == 0 {
			return nil, fmt.Errorf("invalid control node port (0) for %s", entryName) //nolint:goerr113
		}

		// Create SSH config with proxy through control node
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

// Helper to get switch IP using Kubernetes client
func (c *Config) getSwitchIP(ctx context.Context, entryName string) (string, error) {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	kube, err := kubeutil.NewClientWithCache(ctx, kubeconfig, wiringapi.SchemeBuilder)
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

// Helper to determine VM type based on entry
func getVMType(entry VLABAccessInfo) VMType {
	switch {
	case entry.IsSwitch:
		return VMTypeSwitch
	case entry.IsControl:
		return VMTypeControl
	case entry.IsServer:
		return VMTypeServer
	default:
		return ""
	}
}
