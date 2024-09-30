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

// Package iso will build an efi bootable live image of the flatcar linux distro.
package iso

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.githedgehog.com/fabricator/pkg/fab"

	diskfs "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/fat32"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

// Copies a file from the local directory to the newly created filesystem, does not rename files.
func copyFile(dstPath string, srcPath string, destination filesystem.FileSystem) error {
	slog.Debug("CopyFile Entry", "DstPath", dstPath, "SrcPath", srcPath, "Destination Filesystem", destination.Label())
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("CopyFile Error Opening Source Path %s: %w", srcPath, err)
	}
	defer src.Close()

	//  "/" is needed to place files in the root dir, diskfs says so
	if dstPath == "/" {
		dstPath = filepath.Join("/", filepath.Base(srcPath))
	}
	dest, err := destination.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_SYNC)
	if err != nil {
		return fmt.Errorf("CopyFile Error Opening Destination Path %s: %w", dstPath, err)
	}
	defer dest.Close()

	_, err = io.Copy(dest, src)
	if err != nil {
		return fmt.Errorf("Writing file using go-diskfs: %w", err)
	}
	return err
}

// Copies an existing directory structure into the new filesystem.
func copyTree(workdir, localDirName string, destination filesystem.FileSystem) error {
	slog.Debug("CopyTree", "LocalDirName", localDirName, "WorkDir", workdir, "Destination", destination.Label())
	tree := filepath.Join(workdir, localDirName)
	err := filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
		// knock out the workdir
		relPath, err := filepath.Rel(workdir, path)
		if err != nil {
			return fmt.Errorf("Error in filepath.Rel, WorkDir %s, Path: %s: %w", workdir, path, err)
		}

		if info.IsDir() {
			err = destination.Mkdir(filepath.Join("/", relPath))
			if err != nil {
				return fmt.Errorf("Error Mkdir -  RelPath %s: %w", relPath, err)
			}
		}
		if !info.IsDir() {
			dstPath := filepath.Join("/", relPath)
			err = copyFile(dstPath, path, destination)
			if err != nil {
				return fmt.Errorf("Error copyFile inside of copyTree Path %s: %w", path, err)
			}

		}

		return err
	})
	if err != nil {
		return fmt.Errorf("Walkpath error %w", err)
	}
	return err
}

func createEfi(diskImg, workdir string) error {
	var (
		espSize             int64 = 500 * 1024 * 1024                              // 500 MiB
		oemSize             int64 = (6 * 1024 * 1024 * 1024) + (500 * 1024 * 1024) // 10.5 GiB
		dataSize                  = espSize + oemSize                              // 1 GiB + 500MiB
		blkSize             int64 = 512
		diskSize                  = dataSize + 2*16896 + (1024 * 1024) // GPT partition is 33 LBA in size, there are two of them. gdisk said I was missing a MiB so I added it.
		espPartitionStart   int64 = 2048
		espPartitionSectors       = espSize / blkSize                             // 1024000 sectors
		espPartitionEnd           = espPartitionSectors + (espPartitionStart - 1) // 1026047
		oemPartitionStart         = espPartitionEnd + 1                           // 1026048
		oemPartitionSectors       = oemSize / blkSize                             // 2097152 sectors
		oemPartitionEnd           = oemPartitionSectors + (oemPartitionStart - 1) // 3123199
	)

	// create a disk image
	disk, err := diskfs.Create(diskImg, diskSize, diskfs.Raw, diskfs.SectorSizeDefault)
	if err != nil {
		return fmt.Errorf("Unable to create disk image:%w", err)
	}
	// create a partition table
	table := new(gpt.Table)
	table.ProtectiveMBR = true

	table.Partitions = []*gpt.Partition{
		{Start: uint64(espPartitionStart), End: uint64(espPartitionEnd), Type: gpt.EFISystemPartition, Size: uint64(espSize), Name: "HHA"},
		{Start: uint64(oemPartitionStart), End: uint64(oemPartitionEnd), Type: gpt.LinuxFilesystem, Size: uint64(oemSize), Name: "HHB"},
	}

	// apply the partition table
	// will also call initTable under the covers
	err = disk.Partition(table)
	if err != nil {
		return fmt.Errorf("Unable to apply Partition table to disk: %w", err)
	}
	// Check the right stuff is on disk
	t, err := disk.GetPartitionTable()
	if err != nil {
		return fmt.Errorf("Partition table error: %w", err)
	}

	err = t.Verify(disk.File, uint64(diskSize))
	if err != nil {
		return fmt.Errorf("Partition table on disk failed verification: %w", err)
	}

	espSpec := diskpkg.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"}
	espFs, err := disk.CreateFilesystem(espSpec)
	if err != nil {
		return fmt.Errorf("Error creating %s filesystem: %w", espSpec.VolumeLabel, err)
	}
	espFs.(*fat32.FileSystem).SetLazy(true)

	// NEED OEM as the disk label things don't work otherwise
	backpackSpec := diskpkg.FilesystemSpec{Partition: 2, FSType: filesystem.TypeFat32, VolumeLabel: "OEM"}
	slog.Debug("espSpec", "Values", espSpec)
	slog.Debug("backpackSpec", "Values", backpackSpec)

	backpackFs, err := disk.CreateFilesystem(backpackSpec)
	if err != nil {
		return fmt.Errorf("Error creating %s filesystem: %w", backpackSpec.VolumeLabel, err)
	}
	backpackFs.(*fat32.FileSystem).SetLazy(true)

	err = copyTree(workdir, "/EFI", espFs)
	if err != nil {
		return fmt.Errorf("Error copying tree: %w", err)
	}
	err = copyTree(workdir, "/boot", espFs)
	if err != nil {
		return fmt.Errorf("Error copying tree: %w", err)
	}

	err = copyFile("/", workdir+"/flatcar_production_pxe_image.cpio.gz", espFs)
	if err != nil {
		return fmt.Errorf("Error copying flatcar_production_pxe_image.cpio.gz: %w", err)
	}
	err = copyFile("/", workdir+"/oem.cpio.gz", espFs)
	if err != nil {
		return fmt.Errorf("Error copying oem.cpio.gz: %w", err)
	}
	err = copyFile("/", workdir+"/flatcar_production_pxe.vmlinuz", espFs)
	if err != nil {
		return fmt.Errorf("Error copying flatcar_production_pxe.vmlinuz: %w", err)
	}

	err = copyFile("/", workdir+"/flatcar-install.yaml", backpackFs)
	if err != nil {
		return fmt.Errorf("Error copying flatcar-install.yaml: %w", err)
	}
	err = copyFile("/", workdir+"/flatcar_production_image.bin.bz2", backpackFs)
	if err != nil {
		return fmt.Errorf("Error copying flatcar_production_image.bin.bz2: %w", err)
	}

	basePath := filepath.Dir(workdir)                     //.hhfab
	ignitionPath := filepath.Join(basePath, "control-os") //.hhfab/control-os/
	slog.Debug("path names", "basePath", basePath, "ignitionPath", ignitionPath)
	err = copyFile("/", ignitionPath+"/ignition.json", backpackFs)
	if err != nil {
		return fmt.Errorf("Error copying ignition.json: %w", err)
	}
	err = copyTree(basePath, "/control-install", backpackFs)
	if err != nil {
		return fmt.Errorf("Error copying control-install: %w", err)
	}

	if err := espFs.(*fat32.FileSystem).Commit(); err != nil {
		return fmt.Errorf("commiting espFs: %w", err)
	}

	if err := backpackFs.(*fat32.FileSystem).Commit(); err != nil {
		return fmt.Errorf("commiting backpackFs: %w", err)
	}

	return err
}

// Build builds the Control Node ISO only, the components needed for this are downloaded as a bundle in a previous step.
func Build(_ context.Context, basedir string) error {
	start := time.Now()

	installer := filepath.Join(basedir, fab.BundleControlInstall.Name)
	target := filepath.Join(basedir, "flatcar-live.img")
	workdir := filepath.Join(basedir, fab.BundleControlISO.Name)

	slog.Info("Building Control Node ISO", "target", target, "workdir", workdir, "installer", installer)
	err := createEfi(target, workdir)
	slog.Info("ISO building done", "took", time.Since(start))

	return err
}
