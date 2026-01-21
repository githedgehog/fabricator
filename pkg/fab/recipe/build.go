// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/fat32"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
	"github.com/diskfs/go-diskfs/partition/gpt"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/embed/flatcaroem"
	"go.githedgehog.com/fabricator/pkg/embed/recipebin"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/f8r"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k9s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
)

var (
	_ comp.ListOCIArtifacts = PrecacheNodeBuildORAS
	_ comp.ListOCIArtifacts = PrecacheNodeBuildOCI
)

// PrecacheNodeBuildORAS returns a list of ORAS artifacts that are required for building the node installer
func PrecacheNodeBuildORAS(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		FlatcarUSBRootRef:         cfg.Status.Versions.Fabricator.ControlUSBRoot,
		k3s.Ref:                   k3s.Version(cfg),
		flatcar.ToolboxArchiveRef: flatcar.ToolboxVersion(cfg),
		k9s.Ref:                   k9s.Version(cfg),
		zot.AirgapRef:             zot.Version(cfg),
		flatcar.UpdateRef:         flatcar.Version(cfg),
		certmanager.AirgapRef:     certmanager.Version(cfg),
		f8r.BashCompletionRef:     cfg.Status.Versions.Platform.BashCompletion,
		fabric.CtlRef:             cfg.Status.Versions.Fabric.Ctl,
		f8r.CtlRef:                cfg.Status.Versions.Fabricator.Ctl,
	}, nil
}

// PrecacheNodeBuildOCI returns a list of OCI artifacts that are required for building the node installer
func PrecacheNodeBuildOCI(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		f8r.NodeConfigRef: cfg.Status.Versions.Fabricator.NodeConfig,
	}, nil
}

type BuildMode string

const (
	BuildModeManual BuildMode = "manual"
	BuildModeUSB    BuildMode = "usb"
	BuildModeISO    BuildMode = "iso"
)

var BuildModes = []BuildMode{BuildModeManual, BuildModeUSB, BuildModeISO}

const (
	Separator                    = "--"
	InstallSuffix                = "install"
	InstallArchiveSuffix         = InstallSuffix + ".tgz"
	InstallIgnitionSuffix        = InstallSuffix + ".ign"
	InstallUSBImageWorkdirSuffix = InstallSuffix + "-usb.wip"
	InstallUSBImageSuffix        = InstallSuffix + "-usb.img"
	InstallISOImageSuffix        = InstallSuffix + "-usb.iso"
	InstallHashSuffix            = InstallSuffix + ".inhash"
	RecipeBin                    = "hhfab-recipe"
	FlatcarUSBRootRef            = "fabricator/control-usb-root"
	IgnitionFile                 = "ignition.json"
	OSTargetInstallDir           = "/opt/hedgehog/install"
)

type buildInstallOpts struct {
	WorkDir               string
	Name                  string
	Type                  Type
	Mode                  BuildMode
	Hash                  string
	AddPayload            func(ctx context.Context, slog *slog.Logger, installDir string) error
	BuildIgnition         func() ([]byte, error)
	Downloader            *artificer.Downloader
	FlatcarUSBRootVersion meta.Version
}

func buildInstall(ctx context.Context, opts buildInstallOpts) error {
	if !slices.Contains(BuildModes, opts.Mode) {
		return fmt.Errorf("invalid build mode %q", opts.Mode) //nolint:goerr113
	}

	slog := slog.With("name", opts.Name, "type", opts.Type, "mode", opts.Mode)

	fullName := string(opts.Type) + Separator + opts.Name + Separator
	installDir := filepath.Join(opts.WorkDir, fullName+InstallSuffix)
	installArchive := filepath.Join(opts.WorkDir, fullName+InstallArchiveSuffix)
	installIgnition := filepath.Join(opts.WorkDir, fullName+InstallIgnitionSuffix)
	installHashFile := filepath.Join(opts.WorkDir, fullName+InstallHashSuffix)
	installUSBImage := filepath.Join(opts.WorkDir, fullName+InstallUSBImageSuffix)
	installISOImage := filepath.Join(opts.WorkDir, fullName+InstallISOImageSuffix)
	installUSBImageWorkdir := filepath.Join(opts.WorkDir, fullName+InstallUSBImageWorkdirSuffix)

	if existingHash, err := os.ReadFile(installHashFile); err == nil {
		files := []string{installDir, installArchive, installIgnition}
		if opts.Mode == BuildModeUSB {
			files = []string{installDir, installUSBImage}
		}
		if opts.Mode == BuildModeISO {
			files = []string{installDir, installISOImage}
		}
		if string(existingHash) == opts.Hash && isPresent(files...) {
			slog.Info("Using existing installer")

			return nil
		}
	}

	if err := removeIfExists(installHashFile); err != nil {
		return fmt.Errorf("removing hash file: %w", err)
	}

	if err := removeIfExists(installDir); err != nil {
		return fmt.Errorf("removing install dir: %w", err)
	}
	if err := removeIfExists(installArchive); err != nil {
		return fmt.Errorf("removing install archive: %w", err)
	}
	if err := removeIfExists(installIgnition); err != nil {
		return fmt.Errorf("removing install ignition: %w", err)
	}
	if err := removeIfExists(installUSBImageWorkdir); err != nil {
		return fmt.Errorf("removing install usb image workdir: %w", err)
	}
	if err := removeIfExists(installUSBImage); err != nil {
		return fmt.Errorf("removing install usb image: %w", err)
	}
	if err := removeIfExists(installISOImage); err != nil {
		return fmt.Errorf("removing install iso image: %w", err)
	}

	if err := os.MkdirAll(installDir, 0o700); err != nil {
		return fmt.Errorf("creating install dir: %w", err)
	}

	slog.Info("Building installer")

	slog.Info("Adding recipe bin and config to installer")
	recipeBin, err := recipebin.Bytes()
	if err != nil {
		return fmt.Errorf("getting recipe bin: %w", err)
	}
	if err := os.WriteFile(filepath.Join(installDir, RecipeBin), recipeBin, 0o700); err != nil { //nolint:gosec
		return fmt.Errorf("writing recipe bin: %w", err)
	}

	if err := (&Config{Type: opts.Type, Name: opts.Name}).Save(installDir); err != nil {
		return fmt.Errorf("saving recipe config: %w", err)
	}

	if err := opts.AddPayload(ctx, slog, installDir); err != nil {
		return fmt.Errorf("adding payload: %w", err)
	}

	switch opts.Mode {
	case BuildModeManual:
		slog.Debug("Archiving installer", "path", installArchive)
		if err := archiveTarGz(ctx, installDir, installArchive); err != nil {
			return fmt.Errorf("archiving install: %w", err)
		}

		slog.Debug("Creating ignition", "path", installIgnition)
		ign, err := opts.BuildIgnition()
		if err != nil {
			return fmt.Errorf("creating ignition: %w", err)
		}

		if err := os.WriteFile(installIgnition, ign, 0o600); err != nil {
			return fmt.Errorf("writing ignition: %w", err)
		}

		slog.Info("Installer build completed", "ignition", installIgnition, "archive", installArchive)
	case BuildModeUSB, BuildModeISO:
		if err := buildUSBImage(ctx, opts); err != nil {
			return fmt.Errorf("building USB image: %w", err)
		}
	default:
		return fmt.Errorf("unsupported build mode %q", opts.Mode) //nolint:goerr113
	}

	if err := os.WriteFile(installHashFile, []byte(opts.Hash), 0o600); err != nil {
		return fmt.Errorf("writing hash: %w", err)
	}

	return nil
}

const (
	isoLogicalBlockSize diskfs.SectorSize = 2048
	MiB                 uint64            = 1024 * 1024
	GiB                 uint64            = 1024 * 1024 * 1024
	espSize             uint64            = 500 * MiB
	oemSize             uint64            = (9 * GiB)
	dataSize                              = espSize + oemSize
	blkSize                               = diskfs.SectorSize512
	bytesPerBlock                         = 512
	GPTSize                               = 33 * bytesPerBlock
	diskSize                              = int64(dataSize + (2 * GPTSize) + MiB)
	espPartitionStart   uint64            = 2048
	espPartitionSectors                   = espSize / uint64(blkSize)
	espPartitionEnd                       = espPartitionSectors + (espPartitionStart - 1)
	oemPartitionStart                     = espPartitionEnd + 1
	oemPartitionSectors                   = oemSize / uint64(blkSize)
	oemPartitionEnd                       = oemPartitionSectors + (oemPartitionStart - 1)
)

func buildUSBImage(ctx context.Context, opts buildInstallOpts) error {
	slog := slog.With("name", opts.Name, "type", opts.Type, "mode", opts.Mode)

	slog.Info("Building installer image, may take up to 5-10 minutes")

	fullName := string(opts.Type) + Separator + opts.Name + Separator
	installUSBImage := filepath.Join(opts.WorkDir, fullName+InstallUSBImageSuffix)
	installISOImage := filepath.Join(opts.WorkDir, fullName+InstallISOImageSuffix)

	tempDir := filepath.Join(opts.WorkDir, fullName+InstallUSBImageWorkdirSuffix)

	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return fmt.Errorf("creating workdir %q: %w", tempDir, err)
	}

	// TODO(Frostman) use ORAS files directly from cache without copying to workdir
	if err := opts.Downloader.FromORAS(ctx, tempDir, FlatcarUSBRootRef, opts.FlatcarUSBRootVersion, []artificer.ORASFile{
		{Name: "boot"},
		{Name: "EFI"},
		{Name: "images"},
		{Name: "flatcar_production_image.bin.bz2"},
		{Name: "flatcar_production_pxe_image.cpio.gz"},
		{Name: "flatcar_production_pxe.vmlinuz"},
	}); err != nil {
		return fmt.Errorf("downloading ISO root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "oem.cpio.gz"), flatcaroem.Bytes(), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing oem cpio: %w", err)
	}
	var fs1 filesystem.FileSystem
	var fs2 filesystem.FileSystem
	diskImgPath := ""

	switch opts.Mode {
	case BuildModeUSB:
		diskImgPath = installUSBImage
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

		if err := partTable.Verify(diskImg.File, uint64(diskSize)); err != nil {
			return fmt.Errorf("verifying partition table: %w", err)
		}

		espSpec := disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"}
		espFS, err := diskImg.CreateFilesystem(espSpec)
		if err != nil {
			return fmt.Errorf("creating filesystem %s: %w", espSpec.VolumeLabel, err)
		}
		espFS.(*fat32.FileSystem).SetLazy(true)

		backpackSpec := disk.FilesystemSpec{Partition: 2, FSType: filesystem.TypeFat32, VolumeLabel: "HH-MEDIA"}
		backpackFS, err := diskImg.CreateFilesystem(backpackSpec)
		if err != nil {
			return fmt.Errorf("creating filesystem %s: %w", backpackSpec.VolumeLabel, err)
		}
		backpackFS.(*fat32.FileSystem).SetLazy(true)

		fs1 = espFS
		fs2 = backpackFS
	case BuildModeISO:
		diskImgPath = installISOImage
		diskImg, err := diskfs.Create(diskImgPath, diskSize, diskfs.Raw, isoLogicalBlockSize)
		if err != nil {
			return fmt.Errorf("creating disk image: %w", err)
		}

		fspec := disk.FilesystemSpec{
			Partition:   0,
			FSType:      filesystem.TypeISO9660,
			VolumeLabel: "HH-MEDIA",
		}
		isoFS, err := diskImg.CreateFilesystem(fspec)
		if err != nil {
			return fmt.Errorf("creating filesystem: %w", err)
		}
		fs1 = isoFS
		fs2 = isoFS
	case BuildModeManual:
		return fmt.Errorf("manual build mode is not supported for USB images") //nolint:goerr113
	default:
		return fmt.Errorf("unsupported build mode %q", opts.Mode) //nolint:goerr113
	}

	slog.Info("Adding /EFI to image", "fs", fs1.Label())
	if err := diskFSCopyTree(tempDir, "/EFI", fs1); err != nil {
		return fmt.Errorf("adding EFI dir: %w", err)
	}

	slog.Info("Adding /boot to image", "fs", fs1.Label())
	if err := diskFSCopyTree(tempDir, "/boot", fs1); err != nil {
		return fmt.Errorf("adding boot dir: %w", err)
	}

	slog.Info("Adding /images to image", "fs", fs1.Label())
	if err := diskFSCopyTree(tempDir, "/images", fs1); err != nil {
		return fmt.Errorf("adding images dir: %w", err)
	}

	slog.Info("Adding flatcar.cpio.gz to image", "fs", fs1.Label())
	if err := diskFSCopyFile("/", filepath.Join(tempDir, "flatcar_production_pxe_image.cpio.gz"), fs1); err != nil {
		return fmt.Errorf("adding flatcar cpio: %w", err)
	}

	slog.Info("Adding oem.cpio.gz to image", "fs", fs1.Label())
	if err := diskFSCopyFile("/", filepath.Join(tempDir, "oem.cpio.gz"), fs1); err != nil {
		return fmt.Errorf("adding oem cpio: %w", err)
	}

	slog.Info("Adding flatcar.vmlinuz to image", "fs", fs1.Label())
	if err := diskFSCopyFile("/", filepath.Join(tempDir, "flatcar_production_pxe.vmlinuz"), fs1); err != nil {
		return fmt.Errorf("adding flatcar vmlinuz: %w", err)
	}

	slog.Info("Adding flatcar.bin to image", "fs", fs2.Label())
	if err := diskFSCopyFile("/", filepath.Join(tempDir, "/flatcar_production_image.bin.bz2"), fs2); err != nil {
		return fmt.Errorf("adding flatcar image: %w", err)
	}

	slog.Info("Adding install bundle to image", "fs", fs2.Label())
	if err := diskFSCopyTree(opts.WorkDir, fullName+InstallSuffix, fs2); err != nil {
		return fmt.Errorf("adding install: %w", err)
	}

	slog.Info("Adding ignition to image", "fs", fs2.Label())
	ign, err := opts.BuildIgnition()
	if err != nil {
		return fmt.Errorf("building ignition: %w", err)
	}
	ignFile, err := fs2.OpenFile(filepath.Join("/", IgnitionFile), os.O_CREATE|os.O_RDWR|os.O_SYNC)
	if err != nil {
		return fmt.Errorf("creating ignition file: %w", err)
	}
	if _, err := ignFile.Write(ign); err != nil {
		return fmt.Errorf("writing ignition: %w", err)
	}

	slog.Info("Finalizing image")

	if opts.Mode == BuildModeUSB {
		if err := fs1.(*fat32.FileSystem).Commit(); err != nil {
			return fmt.Errorf("committing esp FS: %w", err)
		}
		if err := fs2.(*fat32.FileSystem).Commit(); err != nil {
			return fmt.Errorf("committing backpack FS: %w", err)
		}
	}

	if opts.Mode == BuildModeISO {
		if err := fs1.(*iso9660.FileSystem).Finalize(iso9660.FinalizeOptions{
			VolumeIdentifier: "HH-MEDIA",
			RockRidge:        true,
			ElTorito: &iso9660.ElTorito{
				Entries: []*iso9660.ElToritoEntry{
					{
						Platform:  iso9660.EFI,
						Emulation: iso9660.NoEmulation,
						BootFile:  "images/efi.img",
					},
				},
			},
		}); err != nil {
			return fmt.Errorf("finalizing ISO: %w", err)
		}
	}

	if err := removeIfExists(tempDir); err != nil {
		return fmt.Errorf("removing install usb image workdir: %w", err)
	}

	slog.Info("Installer image completed", "path", diskImgPath)

	return nil
}

func isPresent(files ...string) bool {
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}

	return true
}

func removeIfExists(path string) error {
	_, err := os.Stat(path)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %q: %w", path, err)
	}

	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing %q: %w", path, err)
	}

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
	dest, err := destination.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("opening dest %q: %w", dstPath, err)
	}
	defer dest.Close()

	if _, err := io.Copy(dest, src); err != nil {
		return fmt.Errorf("copying: %w", err)
	}

	if f, ok := dest.(*os.File); ok {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("syncing dest: %w", err)
		}
	}

	return nil
}
