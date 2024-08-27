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

package iso

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	diskfs "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
	"go.githedgehog.com/fabricator/pkg/fab"
)

// Copies a file from the local directory to the newly created filesystem, does not rename files.
func copyFile(dstPath string, srcPath string, destination filesystem.FileSystem, buf []byte) error {
	src, err := os.Open(srcPath)
	if err != nil {
		log.Print(err)
	}
	defer src.Close()

	// needed to place files in the root dir
	if dstPath == "/" {
		dstPath = "/" + srcPath
	}
	dest, err := destination.OpenFile(dstPath, os.O_CREATE|os.O_RDWR)
	if err != nil {
		log.Print(err)
	}
	defer dest.Close()

	//Beautiful Copy
	_, err = io.CopyBuffer(dest, src, buf)
	return err
}

// Copies an existing directory structure into the new filesystem.
func copyTree(localDirName string, destination filesystem.FileSystem) error {
	err := filepath.Walk(localDirName, func(path string, info os.FileInfo, err error) error {

		// This is a folder, so make it in the new filesystem
		if info.IsDir() {
			err = destination.Mkdir("/" + path)
			if err != nil {
				log.Fatal("Error with:", path, err)
				return err
			}
		}
		// This a file, make it in the new filesystem
		if !info.IsDir() {
			buf := make([]byte, 1024*1024)
			err = copyFile("/"+path, path, destination, buf)
			if err != nil {
				log.Fatal("Error with:", path, err)
				return err
			}

		}

		return err
	}) //anon filepath walk function
	return err

}

func createEfi(diskImg string) {

	var (
		espSize             int64 = 500 * 1024 * 1024      // 500 MiB
		oemSize             int64 = 1 * 1024 * 1024 * 1024 // 1 GiB
		dataSize            int64 = espSize + oemSize      // 1 GiB + 500MiB
		blkSize             int64 = 512
		diskSize            int64 = dataSize + 2*16896 + (1024 * 1024) //GPT partition is 33 LBA in size, there are two of them. gdisk said I was missing a MiB so I added it.
		espPartitionStart   int64 = 2048
		espPartitionSectors int64 = espSize / blkSize                             // 1024000 sectors
		espPartitionEnd     int64 = espPartitionSectors + (espPartitionStart - 1) // 1026047
		oemPartitionStart   int64 = espPartitionEnd + 1                           // 1026048
		oemPartitionSectors int64 = oemSize / blkSize                             // 2097152 sectors
		oemPartitionEnd     int64 = oemPartitionSectors + (oemPartitionStart - 1) // 3123199
	)

	// create a disk image
	disk, err := diskfs.Create(diskImg, diskSize, diskfs.Raw, diskfs.SectorSizeDefault)
	if err != nil {
		log.Print(err)
	}
	// create a partition table
	table := new(gpt.Table)
	table.ProtectiveMBR = true

	table.Partitions = []*gpt.Partition{
		&gpt.Partition{Start: uint64(espPartitionStart), End: uint64(espPartitionEnd), Type: gpt.EFISystemPartition, Size: uint64(espSize), Name: "ESP"},
		&gpt.Partition{Start: uint64(oemPartitionStart), End: uint64(oemPartitionEnd), Type: gpt.LinuxFilesystem, Size: uint64(oemSize), Name: "BACKPACK"},
	}

	// apply the partition table
	// will also call initTable under the covers
	err = disk.Partition(table)
	if err != nil {
		log.Print(err)
	}
	// Check the right stuff is on disk
	t, err := disk.GetPartitionTable()
	if err != nil {
		log.Print(err)
	}
	fmt.Printf("Table: %+v\n", t)

	err = t.Verify(disk.File, uint64(diskSize))
	if err != nil {
		log.Print(err)
	}

	espSpec := diskpkg.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"}
	espFs, err := disk.CreateFilesystem(espSpec)
	if err != nil {
		log.Print(err)
	}

	// NEED OEM as the disk label things don't work otherwise
	backpackSpec := diskpkg.FilesystemSpec{Partition: 2, FSType: filesystem.TypeFat32, VolumeLabel: "OEM"}

	backpackFs, err := disk.CreateFilesystem(backpackSpec)
	if err != nil {
		log.Print(err)
	}

	copyTree("EFI", espFs)
	copyTree("boot", espFs)

	buf := make([]byte, 256*1024*1024)
	copyFile("/", "./flatcar_production_pxe_image.cpio.gz", espFs, buf)

	copyFile("/", "./oem.cpio.gz", espFs, buf)
	copyFile("/", "./flatcar_production_pxe.vmlinuz", espFs, buf)
	fmt.Println("Copying flatcar_image.bin.bz2")
	copyFile("/", "./flatcar_production_image.bin.bz2", backpackFs, buf)
}

// Build builds the Control Node ISO only, based on the pre-built control-instal bundle, not generic
func Build(_ context.Context, basedir string) error {
	start := time.Now()

	installer := filepath.Join(basedir, fab.BundleControlInstall.Name)
	target := filepath.Join(basedir, "control-node.iso")
	workdir := filepath.Join(basedir, fab.BundleControlISO.Name)

	slog.Info("Building Control Node ISO", "target", target, "workdir", workdir, "installer", installer)

	// TODO implement ISO building
	// - use "workdir" as working directory where all needed files will be downloaded and configs built (see fab/ctrl_os.go)
	// - include all files from "installer" path and later run "hhfab-recipe ..." on that files on first boot
	// - use "target" as final ISO file

	createEfi(target)
	slog.Info("ISO building done", "took", time.Since(start))

	return nil
}
