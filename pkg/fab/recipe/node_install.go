// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/f8r"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	coreapi "k8s.io/api/core/v1"
)

const (
	dropVXLANService  = "hh-drop-vxlan.service"
	dropVXLANUnitPath = "/etc/systemd/system/" + dropVXLANService
)

type NodeInstallUpgrade struct {
	WorkDir string
	Yes     bool
	Fab     fabapi.Fabricator
	Node    fabapi.FabNode
}

func (c *NodeInstallUpgrade) Run(ctx context.Context, upgrade bool) error {
	mode := "install"
	if upgrade {
		mode = "upgrade"
	}
	if upgrade && c.Node.Spec.Management.Interface != "" {
		if err := enforceNICNames(ctx, c.WorkDir, c.Node.Spec.Management.Interface, ""); err != nil {
			return fmt.Errorf("NIC names: %w", err)
		}
	}
	slog.Info("Running node "+mode, "name", c.Node.Name, "roles", c.Node.Spec.Roles)
	if err := checkIfaceAddresses(fabapi.MgmtNICName,
		string(c.Node.Spec.Management.IP),
	); err != nil {
		return fmt.Errorf("checking management addresses: %w", err)
	}

	// TODO dedup
	slog.Info("Wait for registry on control node(s)")

	regURL, err := comp.RegistryURL(c.Fab)
	if err != nil {
		return fmt.Errorf("getting registry URL: %w", err)
	}

	if err := waitURL(ctx, "https://"+regURL+"/v2/_catalog", ""); err != nil {
		return fmt.Errorf("waiting for zot endpoint: %w", err)
	}

	controlVIP, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}

	if err := setupTimesync(ctx, controlVIP.Addr().String()); err != nil {
		return fmt.Errorf("setting up timesync: %w", err)
	}

	if err := c.joinK8s(ctx); err != nil {
		return fmt.Errorf("joining k8s cluster: %w", err)
	}

	if err := installToolbox(ctx); err != nil {
		return fmt.Errorf("installing toolbox: %w", err)
	}

	if slices.Contains(c.Node.Spec.Roles, fabapi.NodeRoleGateway) {
		// TODO remove after dataplane takes care of it
		if err := c.prepForDataplane(ctx); err != nil {
			return fmt.Errorf("preparing node for dataplane: %w", err)
		}

		if err := c.enableLLDPOnAllEther(ctx); err != nil {
			return fmt.Errorf("enabling LLDP on all ether interfaces: %w", err)
		}
	}

	if upgrade {
		if err := upgradeFlatcar(ctx, string(flatcar.Version(c.Fab)), c.Yes); err != nil {
			return fmt.Errorf("upgrading Flatcar: %w", err)
		}
	}

	slog.Info("Node " + mode + " completed")

	return nil
}

// TODO dedup with contol node's k3s install
func (c *NodeInstallUpgrade) joinK8s(ctx context.Context) error {
	slog.Info("Joining k8s cluster")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := copyFile(k3s.BinName, filepath.Join(k3s.BinDir, k3s.BinName), 0o755); err != nil {
		return fmt.Errorf("copying k3s bin: %w", err)
	}

	if err := os.MkdirAll(k3s.ImagesDir, 0o755); err != nil {
		return fmt.Errorf("creating k3s images dir %q: %w", k3s.ImagesDir, err)
	}

	if err := copyFile(k3s.AirgapName, filepath.Join(k3s.ImagesDir, k3s.AirgapName), 0o644); err != nil {
		return fmt.Errorf("copying k3s airgap: %w", err)
	}

	nodeConfigRef, err := comp.ImageURL(c.Fab, f8r.NodeConfigRef)
	if err != nil {
		return fmt.Errorf("getting node config image URL: %w", err)
	}

	if err := artificer.InstallOCIArchive(ctx, ".", f8r.NodeConfigRef,
		c.Fab.Status.Versions.Fabricator.NodeConfig,
		filepath.Join(k3s.ImagesDir, f8r.NodeConfigAirgapName),
		nodeConfigRef+":"+string(c.Fab.Status.Versions.Fabricator.NodeConfig),
	); err != nil {
		// error is hardcoded in the lib and so we can't match it
		if strings.Contains(err.Error(), "docker-archive doesn't support modifying existing images") {
			slog.Warn("Node config airgap image already loaded, skipping")
		} else {
			return fmt.Errorf("installing node config airgap image: %w", err)
		}
	}

	if err := os.MkdirAll(k3s.ConfigDir, 0o755); err != nil {
		return fmt.Errorf("creating k3s config dir %q: %w", k3s.ConfigPath, err)
	}

	k3sCfg, err := k3s.AgentConfig(c.Fab, c.Node)
	if err != nil {
		return fmt.Errorf("k3s config: %w", err)
	}
	if err := os.WriteFile(k3s.ConfigPath, []byte(k3sCfg), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing file %q: %w", k3s.ConfigPath, err)
	}

	k3sInstall := "./" + k3s.InstallName
	if err := os.Chmod(k3sInstall, 0o755); err != nil {
		return fmt.Errorf("chmod k3s install: %w", err)
	}

	controlVIP, err := c.Fab.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return fmt.Errorf("parsing control VIP: %w", err)
	}

	args := []string{}
	for _, role := range c.Node.Spec.Roles {
		args = append(args,
			"--node-label", fabapi.RoleLabelKey(role)+"="+fabapi.RoleLabelValue,
			"--node-taint", fabapi.RoleTaintKey(role)+":"+string(coreapi.TaintEffectNoExecute),
		)
	}

	slog.Debug("Running k3s install")
	cmd := exec.CommandContext(ctx, k3sInstall, args...)
	cmd.Env = append(os.Environ(),
		"INSTALL_K3S_SKIP_DOWNLOAD=true",
		"INSTALL_K3S_BIN_DIR=/opt/bin",
		fmt.Sprintf("K3S_URL=https://%s:%d", controlVIP.Addr().String(), k3s.APIPort),
		"K3S_TOKEN="+c.Fab.Spec.Config.Control.JoinToken,
	)
	cmd.Dir = c.WorkDir
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "k3s: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "k3s: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running k3s install: %w", err)
	}

	return nil
}

func (c *NodeInstallUpgrade) prepForDataplane(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	slog.Debug("Dropping VXLAN traffic (4789/udp)")

	if err := os.WriteFile(dropVXLANUnitPath, //nolint:gosec
		[]byte(`
[Unit]
Description=Drop inbound VXLAN (4789/udp)
Wants=network-pre.target
Before=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'iptables -C INPUT -p udp --dport 4789 -j DROP 2>/dev/null || iptables -I INPUT 1 -p udp --dport 4789 -j DROP'
ExecStop=/bin/sh -c 'iptables -D INPUT -p udp --dport 4789 -j DROP || true'

[Install]
WantedBy=multi-user.target
`), 0o644); err != nil {
		return fmt.Errorf("writing drop vxlan unit: %w", err)
	}

	cmd := exec.CommandContext(ctx, "systemctl", "daemon-reload")
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error on systemctl daemon-reload: %w", err)
	}

	cmd = exec.CommandContext(ctx, "systemctl", "enable", "--now", dropVXLANService)
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "systemctl: ")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error on systemctl enable --now %s: %w", dropVXLANService, err)
	}

	return nil
}

func (c *NodeInstallUpgrade) enableLLDPOnAllEther(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	slog.Debug("Enabling LLDP on all ether interfaces")

	if err := os.WriteFile("/etc/systemd/network/90-lldp.network", //nolint:gosec
		[]byte(`# Enable LLDP on all ether interfaces
[Match]
Name=*
Type=ether
Driver=!veth
Kind=!*

[Network]
KeepConfiguration=yes
DHCP=no
IPv6AcceptRA=no
IPv6SendRA=no
LinkLocalAddressing=no
LLDP=yes
EmitLLDP=yes
`), 0o644); err != nil {
		return fmt.Errorf("writing 90-lldp.network: %w", err)
	}

	cmd := exec.CommandContext(ctx, "networkctl", "reload")
	cmd.Dir = c.WorkDir
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "networkctl: ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "networkctl: ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running networkctl reload: %w", err)
	}

	return nil
}

func installToolbox(ctx context.Context) error {
	if ubuntu, err := isUbuntu(); err == nil && ubuntu {
		// TODO do something on ubuntu
		slog.Warn("Skipping toolbox on ubuntu")

		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	slog.Info("Installing toolbox")

	var lastErr error
	for attempt := range 24 {
		if attempt > 0 {
			time.Sleep(5 * time.Second)
		}
		// using system ctr as we need to load it for the toolbox, not for k8s
		cmd := exec.CommandContext(ctx, "ctr", "image", "import", flatcar.ToolboxArchiveBin) //nolint:gosec
		cmd.Stdout = logutil.NewSink(ctx, slog.Debug, "ctr-import: ")
		cmd.Stderr = logutil.NewSink(ctx, slog.Debug, "ctr-import: ")
		lastErr = cmd.Run()
		if lastErr == nil {
			break
		}

		slog.Debug("Failed to install toolbox", "attempt", attempt, "err", lastErr)
	}
	if lastErr != nil {
		return fmt.Errorf("ctr image import: %w", lastErr)
	}

	if err := os.WriteFile("/etc/default/toolbox", []byte(flatcar.ToolboxConfig), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing toolbox config: %w", err)
	}

	return nil
}

// enforceNICNames this function is meant to move customers to standard NIC names, it expects the NIC names
// to be passed in from the fab.yaml.
func enforceNICNames(ctx context.Context, workDir string, mgmtNIC string, extNIC string) error {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()
	isCtrlr := true
	if extNIC == "" {
		isCtrlr = false
	}
	reloadServices := false

	mgmtNICPaths := []string{
		"/etc/rancher/k3s/config.yaml",
		"/etc/systemd/network/20-mgmt.network",
	}
	for _, path := range mgmtNICPaths {
		changed, err := replaceInFile(path, mgmtNIC, fabapi.MgmtNICName)
		if err != nil {
			return fmt.Errorf("error replacing mgmt nic name in %q: %w", path, err)
		}
		if changed {
			slog.Debug("Renamed NIC", "old", mgmtNIC, "new", fabapi.MgmtNICName, "file", path)
			reloadServices = true
		}
	}
	newMgmtLinkPath := "/etc/systemd/network/08-mgmt.link"
	mgmtLinkFile := fmt.Sprintf("[Match]\nOriginalName=%s\n[Link]\nName=%s\nDescription=Communicate with Fabric switches", mgmtNIC, fabapi.MgmtNICName)

	if err := os.WriteFile(newMgmtLinkPath, []byte(mgmtLinkFile), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writefile error at %q: %w", newMgmtLinkPath, err)
	}

	if isCtrlr {
		extNICPaths := []string{
			"/etc/hh/nftables.conf",
			"/etc/systemd/network/30-ext.network",
		}
		for _, path := range extNICPaths {
			changed, err := replaceInFile(path, extNIC, fabapi.ExtNICName)
			if err != nil {
				return fmt.Errorf("error replacing ext nic name in %q: %w", path, err)
			}
			if changed {
				slog.Debug("Renamed NIC", "old", extNIC, "new", fabapi.ExtNICName, "file", path)
				reloadServices = true
			}
		}
		newExtLinkPath := "/etc/systemd/network/09-ext.link"
		extLinkFile := fmt.Sprintf("[Match]\nOriginalName=%s\n[Link]\nName=%s\nDescription=Used to communicate outside the Fabric", extNIC, fabapi.ExtNICName)

		if err := os.WriteFile(newExtLinkPath, []byte(extLinkFile), 0o644); err != nil { //nolint:gosec
			return fmt.Errorf("writefile error at %q: %w", newExtLinkPath, err)
		}
	}
	if reloadServices {
		run := func(name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = workDir
			sink := logutil.NewSink(ctx, slog.Debug, name+": ")
			cmd.Stdout, cmd.Stderr = sink, sink
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("running %s %s: %w", name, strings.Join(args, " "), err)
			}

			return nil
		}
		var reloadCmds [][]string
		if isCtrlr {
			reloadCmds = [][]string{
				{"udevadm", "control", "--reload"},
				{"udevadm", "trigger", "--settle", "--subsystem-match=net", "--action=add"},
				{"networkctl", "reload"},
				{"systemctl", "reload", "hh-nftables.service"},
			}
		} else {
			reloadCmds = [][]string{
				{"udevadm", "control", "--reload"},
				{"udevadm", "trigger", "--settle", "--subsystem-match=net", "--action=add"},
				{"networkctl", "reload"},
			}
		}

		for _, cmd := range reloadCmds {
			if err := run(cmd[0], cmd[1:]...); err != nil {
				return err
			}
		}
	}

	return nil
}

// replaceInFile replaces the every occurrence of existing with replacement in
// the file at path, preserving permissions (not owner), return false,nil when no match is found.
//
// path must not be a symlink.
func replaceInFile(path string, existing string, replacement string) (bool, error) {
	fileData, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading file %q: %w", path, err)
	}

	// idempotency check
	newFileData := bytes.ReplaceAll(fileData, []byte(existing), []byte(replacement))
	if bytes.Equal(fileData, newFileData) {
		return false, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat file %q: %w", path, err)
	}

	if err := os.WriteFile(path, newFileData, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("writing new data into %q: %w", path, err)
	}

	return true, nil
}
