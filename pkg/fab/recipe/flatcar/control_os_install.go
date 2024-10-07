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

	"go.githedgehog.com/fabric/pkg/util/logutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

const (
	MountDir = "/mnt/rootdir"
)

func DoControlOSInstall(ctx context.Context, workDir string) error {
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

		fabPath := filepath.Join(workDir, dirEntry.Name(), recipe.FabName)
		if _, err := os.Stat(fabPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return fmt.Errorf("stat %q: %w", fabPath, err)
		}

		slog.Info("Loading Fabricator config", "path", fabPath)
		installDir := filepath.Join(workDir, dirEntry.Name())

		l := apiutil.NewFabLoader()
		fabData, err := os.ReadFile(filepath.Join(installDir, recipe.FabName))
		if err != nil {
			return fmt.Errorf("reading fabricator config: %w", err)
		}
		if err := l.LoadAdd(ctx, fabData); err != nil {
			return fmt.Errorf("loading fabricator config: %w", err)
		}

		f, controls, err := fab.GetFabAndControls(ctx, l.GetClient(), false)
		if err != nil {
			return fmt.Errorf("getting fabricator and controls: %w", err)
		}
		if len(controls) != 1 {
			return fmt.Errorf("expected exactly one control node, got %d", len(controls)) //nolint:goerr113
		}

		return (&ControlOSInstal{
			WorkDir:    workDir,
			InstallDir: installDir,
			Fab:        f,
			Control:    controls[0],
		}).Run(ctx)
	}

	return nil
}

type ControlOSInstal struct {
	WorkDir    string
	InstallDir string
	Fab        fabapi.Fabricator
	Control    fabapi.ControlNode
}

func (i *ControlOSInstal) Run(ctx context.Context) error {
	ignition := filepath.Join(i.WorkDir, recipe.ControlUSBIgnition)
	dev := i.Control.Spec.Bootstrap.Disk
	img := filepath.Join(i.WorkDir, "flatcar_production_image.bin.bz2") // TODO const

	if err := i.execCmd(ctx, true, "lsblk", dev); err != nil {
		return fmt.Errorf("checking disk %q: %w", dev, err)
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

	slog.Info("Installing Flatcar", "dev", dev)
	if err := i.execCmd(ctx, true, "/tmp/flatcar-install", "-i", ignition, "-d", dev, "-f", img); err != nil {
		return fmt.Errorf("installing flatcar: %w", err)
	}

	if err := i.execCmd(ctx, true, "partprobe", dev); err != nil {
		return fmt.Errorf("partprobing: %w", err)
	}

	slog.Info("Expanding On-disk root parition", "dev", dev)
	// have to delete existing partition
	if err := i.execCmd(ctx, true, "sgdisk", "--delete=9 ", dev); err != nil {
		return fmt.Errorf("Deleting partition 9 from existing block device: %w", err)
	}

	// 4857856 is the start sector start of the too small root parition
	// not expected to change often, disk_layout is set by flatcar
	// The typecode listed here is a UUID that flatcar uses - https://github.com/flatcar/init/blob/flatcar-master/scripts/extend-filesystems#L15
	// Called COREOS_RESIZE, we are doing a small expand, then letting the installer exapand to the full disk size.
	if err := i.execCmd(ctx, true, "sgdisk", "--new=9:4857856:+9G", "--typecode=9:3884dd41-8582-4404-b9a8-e9b84f2df50e", dev); err != nil {
		return fmt.Errorf("Creating partition 9 on existing block device: %w", err)
	}

	// The partition resize didn't wipe out the exisiting filesystem so we don't
	// need to remake it, just expand the one that is on disk already. In our
	// case we just moving the end of it, not the start
	if err := i.execCmd(ctx, true, "resize2fs", dev); err != nil {
		return fmt.Errorf("Resizing filesystem on partition 9 on existing block device: %w", err)
	}

	if err := os.MkdirAll(MountDir, 0o755); err != nil {
		return fmt.Errorf("creating mount dir: %w", err)
	}

	// 9 is the partition number for the root partition
	if err := i.execCmd(ctx, true, "mount", "-t", "auto", dev+"9", MountDir); err != nil {
		return fmt.Errorf("mounting root: %w", err)
	}

	target := filepath.Join(MountDir, recipe.ControlOSTarget)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("creating target dir: %w", err)
	}

	slog.Info("Uploading control installer to installed Flatcar")
	if err := i.execCmd(ctx, true, "rsync", "-azP", i.InstallDir, target); err != nil {
		return fmt.Errorf("rsyncing control-install: %w", err)
	}

	if err := i.execCmd(ctx, true, "umount", MountDir); err != nil {
		return fmt.Errorf("unmounting root: %w", err)
	}

	slog.Info("Rebooting to installed Flatcar, USB drive can be removed")
	if err := i.execCmd(ctx, true, "shutdown", "-r", "now", "Flatcar installed, rebooting to installed system"); err != nil {
		return fmt.Errorf("rebooting: %w", err)
	}

	return nil
}

func (i *ControlOSInstal) execCmd(ctx context.Context, sudo bool, cmdName string, args ...string) error { //nolint:unparam
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
