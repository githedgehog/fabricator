// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package flatcar

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/lo"
	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

const (
	MountDir = "/mnt/rootdir"
)

func DoOSInstall(ctx context.Context, workDir string) error {
	dirEntries, err := os.ReadDir(workDir)
	if err != nil {
		return fmt.Errorf("reading workdir %q: %w", workDir, err)
	}

	for _, dirEntry := range dirEntries {
		if !dirEntry.IsDir() {
			continue
		}
		if !strings.HasSuffix(dirEntry.Name(), recipe.InstallSuffix) {
			continue
		}

		installDir := filepath.Join(workDir, dirEntry.Name())

		slog.Debug("Found install dir", "path", installDir)

		fabPath := filepath.Join(installDir, recipe.FabName)
		if _, err := os.Stat(fabPath); err != nil {
			if os.IsNotExist(err) {
				slog.Debug("Fabricator config not found", "path", fabPath)

				continue
			}

			return fmt.Errorf("stat fab config %q: %w", fabPath, err)
		}

		configPath := filepath.Join(installDir, recipe.ConfigName)
		if _, err := os.Stat(configPath); err != nil {
			if os.IsNotExist(err) {
				slog.Debug("Recipe config not found", "path", configPath)

				continue
			}

			return fmt.Errorf("stat recipe config %q: %w", configPath, err)
		}

		slog.Info("Loading recipe config", "path", configPath)

		cfg, err := recipe.LoadConfig(installDir)
		if err != nil {
			return fmt.Errorf("loading recipe config: %w", err)
		}

		slog.Info("Loading Fabricator config", "path", fabPath)

		l := apiutil.NewFabLoader()
		fabData, err := os.ReadFile(fabPath)
		if err != nil {
			return fmt.Errorf("reading fabricator config: %w", err)
		}
		if err := l.LoadAdd(ctx, fabData); err != nil {
			return fmt.Errorf("loading fabricator config: %w", err)
		}

		_, controls, nodes, err := fab.GetFabAndNodes(ctx, l.GetClient(), fab.GetFabAndNodesOpts{
			AllowNotHydrated: true,
			AllowNoControls:  true,
		})
		if err != nil {
			return fmt.Errorf("getting fabricator, controls and nodes: %w", err)
		}
		if len(controls) > 1 {
			return fmt.Errorf("only one control node supported, got %d", len(controls)+len(nodes)) //nolint:goerr113
		}

		found := false
		targetDisk := ""
		role := ""

		if cfg.Type == recipe.TypeControl {
			for _, control := range controls {
				if control.Name != cfg.Name {
					continue
				}

				found = true
				targetDisk = control.Spec.Bootstrap.Disk
				role = "control"
			}
		}

		if cfg.Type == recipe.TypeNode {
			for _, node := range nodes {
				if node.Name != cfg.Name {
					continue
				}

				found = true
				targetDisk = node.Spec.Bootstrap.Disk
				if len(node.Spec.Roles) > 0 {
					role = strings.Join(lo.Map(node.Spec.Roles, func(role fabapi.NodeRole, _ int) string {
						return string(role)
					}), ",")
				}
			}
		}

		if !found {
			return fmt.Errorf("no expected %s/%s node or control node found in fabricator config", cfg.Type, cfg.Name) //nolint:goerr113
		}

		slog.Info("Installing", "name", cfg.Name, "type", cfg.Type, "role", role, "disk", targetDisk)

		return (&OSInstal{
			WorkDir:    workDir,
			InstallDir: installDir,
			TargetDisk: targetDisk,
		}).Run(ctx)
	}

	return fmt.Errorf("no install dirs found in %q", workDir) //nolint:goerr113
}

type OSInstal struct {
	WorkDir    string
	TargetDisk string
	InstallDir string
}

func (i *OSInstal) Run(ctx context.Context) error {
	if i.TargetDisk == "" {
		return fmt.Errorf("no target disk found in fabricator config") //nolint:goerr113
	}

	ignition := filepath.Join(i.WorkDir, recipe.IgnitionFile)
	img := filepath.Join(i.WorkDir, "flatcar_production_image.bin.bz2") // TODO const

	if err := i.execCmd(ctx, true, "lsblk", i.TargetDisk); err != nil {
		return fmt.Errorf("checking disk %q: %w", i.TargetDisk, err)
	}

	// TODO find a better way to avoid flatcar-install hanging
	// most probably it's because of the background job trying to write to fifo in blocking way and it's already removed
	// and/or we somehow waiting for job to finish but there is noone to read from fifo
	{
		flatcarInstall, err := os.ReadFile("/usr/bin/flatcar-install")
		if err != nil {
			return fmt.Errorf("reading flatcar-install: %w", err)
		}
		lineToRemove := "(exec 2>/dev/null ; echo \"done\" > \"${WORKDIR}/disk_modified\") &"
		if !strings.Contains(string(flatcarInstall), lineToRemove) {
			return fmt.Errorf("line to be removed not found in flatcar-install: %q", lineToRemove) //nolint:goerr113
		}
		flatcarInstall = []byte(strings.ReplaceAll(string(flatcarInstall), lineToRemove, ""))
		if err := os.WriteFile("/tmp/flatcar-install", flatcarInstall, 0o755); err != nil { //nolint:gosec
			return fmt.Errorf("writing patched flatcar-install: %w", err)
		}
	}

	slog.Info("Installing Flatcar", "dev", i.TargetDisk)
	if err := i.execCmd(ctx, true, "/tmp/flatcar-install", "-i", ignition, "-d", i.TargetDisk, "-f", img); err != nil {
		return fmt.Errorf("installing flatcar: %w", err)
	}

	if err := i.execCmd(ctx, true, "partprobe", i.TargetDisk); err != nil {
		return fmt.Errorf("partprobing: %w", err)
	}

	slog.Info("Expanding On-disk root parition", "dev", i.TargetDisk)
	// have to delete existing partition
	if err := i.execCmd(ctx, true, "sgdisk", "--delete=9", i.TargetDisk); err != nil {
		return fmt.Errorf("deleting partition 9 from existing block device: %w", err)
	}

	if err := i.execCmd(ctx, true, "partprobe", i.TargetDisk); err != nil {
		return fmt.Errorf("partprobing: %w", err)
	}

	// 4857856 is the start sector start of the too small root parition
	// not expected to change often, disk_layout is set by flatcar
	// The typecode listed here is a UUID that flatcar uses - https://github.com/flatcar/init/blob/flatcar-master/scripts/extend-filesystems#L15
	// Called COREOS_RESIZE, we are doing a small expand, then letting the installer exapand to the full disk size.
	if err := i.execCmd(ctx, true, "sgdisk", "--new=9:4857856:+9G", "--typecode=9:3884dd41-8582-4404-b9a8-e9b84f2df50e", i.TargetDisk); err != nil {
		return fmt.Errorf("creating partition 9 on existing block device: %w", err)
	}

	if err := i.execCmd(ctx, true, "partprobe", i.TargetDisk); err != nil {
		return fmt.Errorf("partprobing: %w", err)
	}

	// The partition resize didn't wipe out the exisiting filesystem so we don't
	// need to remake it, just expand the one that is on disk already. In our
	// case we just moving the end of it, not the start
	// 9 is the partition number for the root partition
	partition := "9"
	if strings.Contains(i.TargetDisk, "nvme") {
		partition = "p9"
	}
	// "-f" in this case is force the check, even if the file system seems clean
	if err := i.execCmd(ctx, true, "e2fsck", "-f", "-p", i.TargetDisk+partition); err != nil {
		return fmt.Errorf("e2fsck filesystem on partition 9 on existing block device: %w", err)
	}
	if err := i.execCmd(ctx, true, "resize2fs", i.TargetDisk+partition); err != nil {
		return fmt.Errorf("resizing filesystem on partition 9 on existing block device: %w", err)
	}

	if err := i.execCmd(ctx, true, "partprobe", i.TargetDisk); err != nil {
		return fmt.Errorf("partprobing: %w", err)
	}

	if err := os.MkdirAll(MountDir, 0o755); err != nil {
		return fmt.Errorf("creating mount dir: %w", err)
	}

	if err := i.execCmd(ctx, true, "mount", "-t", "auto", i.TargetDisk+partition, MountDir); err != nil {
		return fmt.Errorf("mounting root: %w", err)
	}

	target := filepath.Join(MountDir, recipe.OSTargetInstallDir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("creating target dir: %w", err)
	}

	slog.Info("Uploading installer to installed Flatcar")
	if err := i.execCmd(ctx, true, "rsync", "-azP", i.InstallDir, target); err != nil {
		return fmt.Errorf("rsyncing installer: %w", err)
	}

	if err := i.execCmd(ctx, true, "umount", MountDir); err != nil {
		return fmt.Errorf("unmounting root: %w", err)
	}

	slog.Info("Rebooting to installed Flatcar in 5 seconds, USB drive can be removed")

	time.Sleep(5 * time.Second)

	if err := i.execCmd(ctx, true, "shutdown", "-r", "now", "Flatcar installed, rebooting to installed system"); err != nil {
		return fmt.Errorf("rebooting: %w", err)
	}

	return nil
}

func (i *OSInstal) execCmd(ctx context.Context, sudo bool, cmdName string, args ...string) error { //nolint:unparam
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmdToRun := cmdName
	argsSummary := strings.Join(args, " ")
	if sudo {
		cmdToRun = "sudo"
		args = append([]string{cmdName}, args...)
	}

	slog.Debug("Running command", "cmd", cmdName+" "+argsSummary)
	cmd := exec.CommandContext(ctx, cmdToRun, args...)
	cmd.Dir = i.WorkDir
	cmd.Stdout = logutil.NewSink(ctx, slog.Debug, cmdName+": ")
	cmd.Stderr = logutil.NewSink(ctx, slog.Debug, cmdName+": ")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %q: %w", cmdName, err)
	}

	return nil
}
