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
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.githedgehog.com/fabricator/pkg/fab"

	diskfs "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

// Copies a file from the local directory to the newly created filesystem, does not rename files.
func copyFile(dstPath string, srcPath string, destination filesystem.FileSystem) error {
	//slog.Debug("CopyFile Entry", "DstPath", dstPath, "SrcPath", srcPath, "Destination Filesystem", destination.Label())
	//src, err := os.Open(srcPath)
	// here goes all the memory...
	src, err := os.ReadFile(srcPath)
	if err != nil {
		slog.Error("CopyFile Error Opening Source Path", "SourcePath", srcPath, "Error:", err.Error())
	}

	//  "/" is needed to place files in the root dir, diskfs says so
	if dstPath == "/" {
		dstPath = filepath.Join("/", filepath.Base(srcPath))
	}
	//slog.Debug("Fabricator - Checking dir Exists", "DestPath", filepath.Dir(dstPath))
	//fileInfos, err := destination.ReadDir(filepath.Dir(dstPath))
	if err != nil {
		slog.Error("CopyFile Error checking DestPath", "DestPath", dstPath, "Error:", err)
	}
	//slog.Debug("Fabricator - Files in dir, before opening new file", "DestPath", filepath.Dir(dstPath), "Files", fileInfos)
	dest, err := destination.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_SYNC)
	if err != nil {
		slog.Error("CopyFile Error Opening Destination Path ", "DestPath", dstPath, "Error:", err)
	}
	//slog.Debug("Fabricator - copyFile - destination file handle,next step is write", "dest", dest)

	//_, err = io.Copy(dest, src)
	_, err = dest.Write(src)
	if err != nil {
		slog.Error("Writing file using go-diskfs", "Error", err)
	}
	//slog.Debug("Fabricator - write:", "Bytes Written", n, "Source File Size", len(src))
	//src.Close()
	dest.Close()
	//fileInfos, err = destination.ReadDir(filepath.Dir(dstPath))
	//if err != nil {
	//	slog.Error("CopyFile Error checking DestPath after write", "DestPath", dstPath, "Error:", err)
	//}
	//slog.Debug("Files in dir after write", "DestPath", filepath.Dir(dstPath), "len(fileinfos)", len(fileInfos), "Files", fileInfos)
	//slog.Debug("CopyFile Exit", "DstPath", dstPath, "SrcPath", srcPath, "Destination Filesystem", destination.Label())
	return err
}

// Copies an existing directory structure into the new filesystem.
func copyTree(workdir, localDirName string, destination filesystem.FileSystem) error {
	//slog.Debug("CopyTree", "LocalDirName", localDirName, "WorkDir", workdir, "Destination", destination.Label())
	tree := filepath.Join(workdir, localDirName)
	err := filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
		//slog.Debug("Entry into Filepath Walk", "Path", path, "os.FileInfo", info.Name())

		// knock out the workdir
		relPath, err := filepath.Rel(workdir, path)
		if err != nil {
			slog.Error("Error in filepath.Rel", "WorkDir", workdir, "Path", path, "Error", err.Error())
			return err
		}

		//slog.Debug("Filepath Walk", "relPath", relPath)
		if info.IsDir() {
			err = destination.Mkdir(filepath.Join("/", relPath))
			//slog.Debug("MkDir", "Destination Filesystem", destination.Label(), "Filepath", filepath.Join("/", relPath))
			if err != nil {
				slog.Error("MkDir Error", "RelPath", relPath, "Error", err.Error())
				return err
			}
		}
		if !info.IsDir() {
			dstPath := filepath.Join("/", relPath)
			err = copyFile(dstPath, path, destination)
			if err != nil {
				slog.Error("copyFile inside of copyTree returned an error", "Path", path, "Error", err.Error())
				return err
			}

		}

		return err
	})
	if err != nil {
		slog.Error("Walkpath error", "Error", err.Error())
	}
	return err

}

func createEfi(diskImg, workdir string) error {

	var (
		espSize             int64 = 500 * 1024 * 1024                              // 500 MiB
		oemSize             int64 = (6 * 1024 * 1024 * 1024) + (500 * 1024 * 1024) // 10.5 GiB
		dataSize            int64 = espSize + oemSize                              // 1 GiB + 500MiB
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
	diskLogan, err := diskfs.Create(diskImg, diskSize, diskfs.Raw, diskfs.SectorSizeDefault)
	if err != nil {
		slog.Error("Unable to create disk image: ", err)
		return err
	}
	// create a partition table
	table := new(gpt.Table)
	table.ProtectiveMBR = true

	table.Partitions = []*gpt.Partition{
		&gpt.Partition{Start: uint64(espPartitionStart), End: uint64(espPartitionEnd), Type: gpt.EFISystemPartition, Size: uint64(espSize), Name: "HHA"},
		&gpt.Partition{Start: uint64(oemPartitionStart), End: uint64(oemPartitionEnd), Type: gpt.LinuxFilesystem, Size: uint64(oemSize), Name: "HHB"},
	}

	// apply the partition table
	// will also call initTable under the covers
	err = diskLogan.Partition(table)
	if err != nil {
		slog.Error("Unable to apply Partition table to disk: ", err.Error())
		return err
	}
	// Check the right stuff is on disk
	t, err := diskLogan.GetPartitionTable()
	if err != nil {
		slog.Error("Partition table error", err.Error())
		return err
	}

	err = t.Verify(diskLogan.File, uint64(diskSize))
	if err != nil {
		slog.Error("Partition table on disk failed verification", "Error", err.Error())
		return err
	}

	espSpec := diskpkg.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"}
	espFs, err := diskLogan.CreateFilesystem(espSpec)
	if err != nil {
		slog.Error("Error creating %s filesystem", "disk", espSpec.VolumeLabel, "Error", err.Error())
		return err
	}
	//slog.Info("espFS", "Values", espFs)

	// NEED OEM as the disk label things don't work otherwise
	backpackSpec := diskpkg.FilesystemSpec{Partition: 2, FSType: filesystem.TypeFat32, VolumeLabel: "OEM"}
	slog.Debug("espSpec", "Values", espSpec)
	slog.Debug("backpackSpec", "Values", backpackSpec)

	backpackFs, err := diskLogan.CreateFilesystem(backpackSpec)
	if err != nil {
		slog.Error("Error creating %s filesystem", "disk", backpackSpec.VolumeLabel, "Error", err.Error())
		return err
	}

	err = copyTree(workdir, "/EFI", espFs)
	if err != nil {
		slog.Error("Error copying tree", "Error", err.Error())
		return err
	}
	err = copyTree(workdir, "/boot", espFs)
	if err != nil {
		slog.Error("Error copying tree", "Error", err.Error())
		return err
	}

	err = copyFile("/", workdir+"/flatcar_production_pxe_image.cpio.gz", espFs)
	err = copyFile("/", workdir+"/oem.cpio.gz", espFs)
	err = copyFile("/", workdir+"/flatcar_production_pxe.vmlinuz", espFs)

	err = copyFile("/", workdir+"/flatcar-install.yaml", backpackFs)
	err = copyFile("/", workdir+"/flatcar_production_image.bin.bz2", backpackFs)

	basePath := filepath.Dir(workdir)                     //.hhfab
	ignitionPath := filepath.Join(basePath, "control-os") //.hhfab/control-os/
	slog.Debug("path names", "basePath", basePath, "ignitionPath", ignitionPath)
	err = copyFile("/", ignitionPath+"/ignition.json", backpackFs)
	err = copyTree(basePath, "/control-install", backpackFs)
	//err = copyTree(basePath, "/control-install-2", backpackFs)
	return err
}

// Build builds the Control Node ISO only, the components needed for this are downloaded as a bundle in a previous step.
func Build(_ context.Context, basedir string) error {

	start := time.Now()

	installer := filepath.Join(basedir, fab.BundleControlInstall.Name)
	target := filepath.Join(basedir, "flatcar-live.img")
	workdir := filepath.Join(basedir, fab.BundleControlISO.Name)

	slog.Info("Building Control Node ISO", "target", target, "workdir", workdir, "installer", installer)
	//f, err := os.Create("CpuProfile")
	//pprof.StartCPUProfile(f)
	err := createEfi(target, workdir)
	//pprof.StopCPUProfile()
	slog.Info("ISO building done", "took", time.Since(start))

	return err
}
