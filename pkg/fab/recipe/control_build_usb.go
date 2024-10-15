// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/fat32"
	"github.com/diskfs/go-diskfs/partition/gpt"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/embed/flatcaroem"
)

const (
	ControlUSBRootRef  = "fabricator/control-usb-root"
	ControlUSBIgnition = "ignition.json"
	ControlOSTarget    = "/opt/hedgehog/install"
)

var (
	espSize             uint64 = 500 * 1024 * 1024
	oemSize             uint64 = (6 * 1024 * 1024 * 1024) + (500 * 1024 * 1024)
	dataSize                   = espSize + oemSize
	blkSize                    = diskfs.SectorSize512
	diskSize                   = int64(dataSize + 2*16896 + (1024 * 1024))
	espPartitionStart   uint64 = 2048
	espPartitionSectors        = espSize / uint64(blkSize)
	espPartitionEnd            = espPartitionSectors + (espPartitionStart - 1)
	oemPartitionStart          = espPartitionEnd + 1
	oemPartitionSectors        = oemSize / uint64(blkSize)
	oemPartitionEnd            = oemPartitionSectors + (oemPartitionStart - 1)
)

func (b *ControlInstallBuilder) buildUSBImage(ctx context.Context) error {
	if b.Control.Spec.Bootstrap.Disk == "" {
		return fmt.Errorf("no disk specified for control %q", b.Control.Name) //nolint:goerr113
	}
	if b.Control.Spec.Management.IP == "" {
		return fmt.Errorf("no management IP specified for control %q", b.Control.Name) //nolint:goerr113
	}
	if b.Control.Spec.Management.Interface == "" {
		return fmt.Errorf("no management interface specified for control %q", b.Control.Name) //nolint:goerr113
	}
	if b.Control.Spec.External.IP == "" {
		return fmt.Errorf("no external IP specified for control %q", b.Control.Name) //nolint:goerr113
	}
	if b.Control.Spec.External.Interface == "" {
		return fmt.Errorf("no external interface specified for control %q", b.Control.Name) //nolint:goerr113
	}

	slog.Info("Building installer USB image, may take up to 5-10 minutes", "control", b.Control.Name)

	workdir := filepath.Join(b.WorkDir, b.Control.Name+InstallUSBImageWorkdirSuffix)

	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return fmt.Errorf("creating workdir %q: %w", workdir, err)
	}

	// TODO(Frostman) use ORAS files directly from cache without copying to workdir
	if err := b.Downloader.FromORAS(ctx, workdir, ControlUSBRootRef, b.Fab.Status.Versions.Fabricator.ControlUSBRoot, []artificer.ORASFile{
		{Name: "boot"},
		{Name: "EFI"},
		{Name: "flatcar_production_image.bin.bz2"},
		{Name: "flatcar_production_pxe_image.cpio.gz"},
		{Name: "flatcar_production_pxe.vmlinuz"},
	}); err != nil {
		return fmt.Errorf("downloading ISO root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "oem.cpio.gz"), flatcaroem.Bytes(), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing oem cpio: %w", err)
	}

	diskImgPath := filepath.Join(b.WorkDir, b.Control.Name+InstallUSBImageSuffix)
	diskImg, err := diskfs.Create(diskImgPath, diskSize, diskfs.Raw, blkSize)
	if err != nil {
		return fmt.Errorf("creating disk image: %w", err)
	}

	table := new(gpt.Table)
	table.ProtectiveMBR = true

	table.Partitions = []*gpt.Partition{
		{
			Name:  "HHA",
			Type:  gpt.EFISystemPartition,
			Size:  espSize,
			Start: espPartitionStart,
			End:   espPartitionEnd,
		},
		{
			Name:  "HHB",
			Type:  gpt.LinuxFilesystem,
			Size:  oemSize,
			Start: oemPartitionStart,
			End:   oemPartitionEnd,
		},
	}

	if err := diskImg.Partition(table); err != nil {
		return fmt.Errorf("applying partition table to disk: %w", err)
	}

	partTable, err := diskImg.GetPartitionTable()
	if err != nil {
		return fmt.Errorf("getting partition table: %w", err)
	}

	if err := partTable.Verify(diskImg.File, uint64(diskSize)); err != nil { //nolint:gosec
		return fmt.Errorf("verifying partition table: %w", err)
	}

	espSpec := disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"}
	espFS, err := diskImg.CreateFilesystem(espSpec)
	if err != nil {
		return fmt.Errorf("creating filesystem %s: %w", espSpec.VolumeLabel, err)
	}
	espFS.(*fat32.FileSystem).SetLazy(true)

	// NEED OEM as the disk label things don't work otherwise
	backpackSpec := disk.FilesystemSpec{Partition: 2, FSType: filesystem.TypeFat32, VolumeLabel: "OEM"}
	backpackFS, err := diskImg.CreateFilesystem(backpackSpec)
	if err != nil {
		return fmt.Errorf("creating filesystem %s: %w", backpackSpec.VolumeLabel, err)
	}
	backpackFS.(*fat32.FileSystem).SetLazy(true)

	slog.Info("Copying /EFI to installer USB image", "fs", espFS.Label(), "control", b.Control.Name)
	if err := diskFSCopyTree(workdir, "/EFI", espFS); err != nil {
		return fmt.Errorf("copying EFI dir: %w", err)
	}

	slog.Info("Copying /boot to installer USB image", "fs", espFS.Label(), "control", b.Control.Name)
	if err := diskFSCopyTree(workdir, "/boot", espFS); err != nil {
		return fmt.Errorf("copying boot dir: %w", err)
	}

	slog.Info("Copying flatcar.cpio.gz to installer USB image", "fs", espFS.Label(), "control", b.Control.Name)
	if err := diskFSCopyFile("/", filepath.Join(workdir, "flatcar_production_pxe_image.cpio.gz"), espFS); err != nil {
		return fmt.Errorf("copying flatcar cpio: %w", err)
	}

	slog.Info("Copying oem.cpio.gz to installer USB image", "fs", espFS.Label(), "control", b.Control.Name)
	if err := diskFSCopyFile("/", filepath.Join(workdir, "oem.cpio.gz"), espFS); err != nil {
		return fmt.Errorf("copying oem cpio: %w", err)
	}

	slog.Info("Copying flatcar.vmlinuz to installer USB image", "fs", espFS.Label(), "control", b.Control.Name)
	if err := diskFSCopyFile("/", filepath.Join(workdir, "flatcar_production_pxe.vmlinuz"), espFS); err != nil {
		return fmt.Errorf("copying flatcar vmlinuz: %w", err)
	}

	slog.Info("Copying flatcar.bin to installer USB image", "fs", backpackFS.Label(), "control", b.Control.Name)
	if err := diskFSCopyFile("/", filepath.Join(workdir, "/flatcar_production_image.bin.bz2"), backpackFS); err != nil {
		return fmt.Errorf("copying flatcar image: %w", err)
	}

	slog.Info("Copying control-install to installer USB image", "fs", backpackFS.Label(), "control", b.Control.Name)
	if err := diskFSCopyTree(b.WorkDir, b.Control.Name+InstallSuffix, backpackFS); err != nil {
		return fmt.Errorf("copying control-install: %w", err)
	}

	targetDir := filepath.Join(ControlOSTarget, b.Control.Name+InstallSuffix)
	ign, err := controlIgnition(b.Fab, b.Control, targetDir)
	if err != nil {
		return fmt.Errorf("creating ignition: %w", err)
	}
	ignFile, err := backpackFS.OpenFile(filepath.Join("/", ControlUSBIgnition), os.O_CREATE|os.O_RDWR|os.O_SYNC)
	if err != nil {
		return fmt.Errorf("creating ignition file: %w", err)
	}
	if _, err := ignFile.Write(ign); err != nil {
		return fmt.Errorf("writing ignition: %w", err)
	}

	if err := espFS.(*fat32.FileSystem).Commit(); err != nil {
		return fmt.Errorf("commiting esp FS: %w", err)
	}
	if err := backpackFS.(*fat32.FileSystem).Commit(); err != nil {
		return fmt.Errorf("commiting backpack FS: %w", err)
	}

	slog.Info("Installer USB image completed", "control", b.Control.Name, "path", diskImgPath)

	return nil
}

func diskFSCopyTree(workdir, localDirName string, destination filesystem.FileSystem) error {
	tree := filepath.Join(workdir, localDirName)
	if err := filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walking %q: %w", path, err)
		}

		relPath, err := filepath.Rel(workdir, path)
		if err != nil {
			return fmt.Errorf("getting rel path: base %q targ %q: %w", workdir, path, err)
		}

		if info.IsDir() {
			if err := destination.Mkdir(filepath.Join("/", relPath)); err != nil {
				return fmt.Errorf("mkdir %q: %w", relPath, err)
			}
		} else {
			dstPath := filepath.Join("/", relPath)
			if err := diskFSCopyFile(dstPath, path, destination); err != nil {
				return fmt.Errorf("copying file %q to %q: %w", localDirName, dstPath, err)
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("filepath walking %q: %w", tree, err)
	}

	return nil
}

func diskFSCopyFile(dstPath string, srcPath string, destination filesystem.FileSystem) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening source %q: %w", srcPath, err)
	}
	defer src.Close()

	//  "/" is needed to place files in the root dir, diskfs says so
	if dstPath == "/" {
		dstPath = filepath.Join("/", filepath.Base(srcPath))
	}
	dest, err := destination.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_SYNC)
	if err != nil {
		return fmt.Errorf("opening dest %q: %w", dstPath, err)
	}
	defer dest.Close()

	if _, err := io.Copy(dest, src); err != nil {
		return fmt.Errorf("copying: %w", err)
	}

	return nil
}
