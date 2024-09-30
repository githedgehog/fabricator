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

// Package install is the binary that will run when the core user logs into
// the live flatcar install image.
package install

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// BlockPartition if a BlockDevice has partitions this struct is used to accept that info.
type BlockPartition struct {
	Size       uint64 `json:"size,omitempty"`
	Rotational bool   `json:"rota,omitempty"`
	HotPlug    bool   `json:"hotplug,omitempty"`
	Path       string `json:"path,omitempty"`
	Name       string `json:"name,omitempty"`
	Model      string `json:"model,omitempty"`
	DevType    string `json:"type",omitempty"`
	Transport  string `json:"tran",omitempty"`
}

// BlockDevice is the parsed output of lsblk --output SIZE,ROTA,HOTPLUG,PATH,NAME,MODEL,TYPE,TRAN --json --exclude 1,3,7,11,252 --bytes.
type BlockDevice struct {
	Size        uint64           `json:"size,omitempty"`
	Rotational  bool             `json:"rota,omitempty"`
	HotPlug     bool             `json:"hotplug,omitempty"`
	Path        string           `json:"path,omitempty"`
	Name        string           `json:"name,omitempty"`
	Model       string           `json:"model,omitempty"`
	DevType     string           `json:"type",omitempty"`
	Transport   string           `json:"tran",omitempty"`
	Children    []BlockPartition `json:"children",omitempty"`
	Description string           `json:"-"`
}

// BlockDevices is the top level json array output from lsblk --json.
type BlockDevices struct {
	Devices []*BlockDevice `json:"blockdevices,omitempty"`
}

// getDisks is responsible for the call to lsblk, calls a function to normalize / beautify the values
func getDisks() *BlockDevices {
	disks := &BlockDevices{}
	// The exclude arguments are major block numbers, 252 is a ZRAM swap disk, 11 is a SATA attached CD-ROM
	args := []string{"--bytes", "--json", "--exclude", "1,3,7,11,252", "--output", "SIZE,ROTA,HOTPLUG,PATH,NAME,MODEL,TYPE,TRAN"}
	lsblkCmd := exec.Command("lsblk", args...)
	var stderr bytes.Buffer
	lsblkCmd.Stderr = &stderr

	stdout, err := lsblkCmd.Output()
	if err != nil {
		return fmt.Errorf("lsblk error %s: %w", stderr.String(), err)
	}
	if err = json.Unmarshal(stdout, disks); err != nil {
		return fmt.Errorf("Error from Unmarshal on lsblk output: %w", err)
	}

	slices.SortFunc(disks.Devices, func(a, b *BlockDevice) int {
		return cmp.Compare(b.Size, a.Size)
	})

	return prettyMetaData(disks)

}

func prettyMetaData(disks *BlockDevices) *BlockDevices {
	rota := " SSD "
	for _, b := range disks.Devices {
		// Reset size from bytes to be in GB base 10
		b.Size = b.Size / 1_000_000_000
		if b.Rotational == true {
			rota = " HDD "
		}
		b.Description = strconv.FormatUint(b.Size, 10) + " GB " + strings.ToUpper(b.Transport) + rota
		if b.Model != "" {
			b.Description += b.Model
		}
	}
	return disks
}

// checks that the config file has all the info needed for install. A happy path exit means the install can move forward
func checkConfigFile(configFilePath string, config *Config) (bool, error) {

	if _, err := os.Stat(configFilePath); err != nil {
		if os.IsNotExist(err) {
			slog.Warn("Config file not found", "file", configFilePath)
		} else {
			return false, fmt.Errorf("Error checking config file %s: %w", configFilePath, err)
		}
	}

	configData, err := os.ReadFile(configFilePath)
	if err != nil {
		return false, fmt.Errorf("Error reading config file %s:%w", configFilePath, err)
	}

	if err := yaml.Unmarshal(configData, config); err != nil {
		return false, fmt.Errorf("Error unmarshalling config file %s:%w", configFilePath, err)
	}
	slog.Debug("ConfigCheck", "config", config)
	if config.PasswordHash == "" && len(config.AuthorizedKeys) == 0 {
		// we need a way to login to the installed systemd
		populatePassword(config)

	}
	if config.BlockDevicePath == "" {
		err = populateBlockDevice(config)
		if err != nil {
			return false, fmt.Errorf("Error choosing block device: %w", err)
		}

	}

	return true, nil

}

func populatePassword(_ *Config) {
	slog.Info("Would be calling populate password")

}
func populateBlockDevice(config *Config) error {
	disks := getDisks()

	// User provided a size, go with the first matching disk
	if config.SizeGB != "" {
		for i, d := range disks.Devices {
			userHint, _ := strconv.ParseUint(config.SizeGB, 10, 64)
			if d.Size == userHint {
				slog.Debug("Used user hint to find block device", "userHint", userHint, "Found Disk", disks.Devices[i].Path)
				config.BlockDevicePath = disks.Devices[i].Path
				return nil
			}
		}

	}
	// TODO this is the function where we would print the disk options and ask
	// and ask the user to select the desired disk. It is not simple to automatically
	// take over an interative login shell in systemd on a system with a RO /home dir
	// This might be a way to go, use this as login shell ?https://pkg.go.dev/golang.org/x/term
	/*
			templates := &promptui.SelectTemplates{
				Label:    "{{ .Description }}",
				Active:   "\U0001F994 {{ .Description | cyan }}",
				Inactive: "{{ .Description | cyan }}",
				Selected: "\U0001F994 {{ .Description | red | cyan }}",
			}

			prompt := promptui.Select{
				Label:     "Select Install Disk",
				Items:     disks.Devices,
				Templates: templates,
			}

			// TODO - check if there is a way to pass os.Stdin, or double check where this is listening
			// this might be a problem for running at login.
				//index, _, err := prompt.Run()

				if err != nil {
					fmt.Printf("Prompt failed %v\n", err)
					return err
				}

		slog.Debug("Block Device Choice", "description", disks.Devices[index].Description, "size", disks.Devices[index].Size)
		config.BlockDevicePath = disks.Devices[index].Path
	*/
	return nil

}
func launchFlatcarInstaller(config Config, dryrun bool) error {
	// TODO add plumbing to get dry run bool to flatcar install command

	slog.Debug("Running Install", "BlockDevice", config.BlockDevicePath)
	installCmd := exec.Command("sudo", "flatcar-install", "-i", "/mnt/hedgehog/ignition.json", "-d", config.BlockDevicePath, "-f", "/mnt/hedgehog/flatcar_production_image.bin.bz2")
	var stderr bytes.Buffer
	installCmd.Stderr = &stderr

	if dryrun {
		slog.Info("DryRun Flatcar install", "Command", installCmd.String())
		return nil
	}

	slog.Info("Executing install command", "Commnad", installCmd.String())
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("flatcar install error- stderr %s, command: %s:%w", stderr.String(), installCmd.String(), err)
	}

	return nil
}

// mountUnbootedFlatcar is reponsible for booting the block device at a known location, so later functions can copy to it
func mountUnbootedFlatcar(config Config) error {

	slog.Info("MountUnbootedFlatcar", "BlockDevice", config.BlockDevicePath)
	partProbeCmd := exec.Command("sudo", "partprobe", config.BlockDevicePath)
	if err := partProbeCmd.Run(); err != nil {
		slog.Error("Exec Command exited with error", "Command", partProbeCmd.String(), "Error", err)
	}

	mkdirCmd := exec.Command("sudo", "mkdir", "/mnt/rootdir")
	if err := mkdirCmd.Run(); err != nil {
		slog.Error("Exec Command exited with error", "Command", mkdirCmd.String(), "Error", err)
	}

	// 6 is the partition number for the oem partition
	// 9 is the partition number for the root partition
	mountCmd := exec.Command("sudo", "mount", "-t", "auto", config.BlockDevicePath+"9", "/mnt/rootdir")
	if err := mountCmd.Run(); err != nil {
		slog.Error("Exec Command exited with error", "Command", mountCmd.String(), "Error", err)
	}

	return nil
}

// copyControlInstallFiles will take the control-os
func copyControlInstallFiles() error {

	slog.Info("Rsync Install Files:", "Destination", "/mnt/rootdir/hedgehog", "Src", "/mnt/hedgehog/control-install")
	// use rsync in the live image for reliable copy
	rsyncCmd := exec.Command("sudo", "rsync", "--quiet", "--recursive", "/mnt/hedgehog/control-install", "/mnt/rootdir/hedgehog/")
	if err := rsyncCmd.Run(); err != nil {
		slog.Error("Exec Command exited with error", "Command", rsyncCmd.String(), "Error", err)
	}
	return nil
}

func rebootSystem(dryrun bool) {
	slog.Info("Rebooting Live Image")
	rebootCmd := exec.Command("sudo", "shutdown", "-r", "+1", "Flatcar installed, Rebooting to installed system")
	if dryrun {
		slog.Info("Dryrun reboot", "Command", rebootCmd.String())
		return
	}
	if err := rebootCmd.Run(); err != nil {
		slog.Error("Reboot command failed to run")
	}
}

// PreInstallCheck will ensure the necessary information is present before starting an installation
func PreInstallCheck(_ context.Context, basedir string, dryRun bool) error {
	slog.Debug("Using", "basedir", basedir, "dryRun", dryRun)

	configFile := filepath.Join(basedir, ConfigFile)
	config := Config{}

	proceed, err := checkConfigFile(configFile, &config)
	if err != nil {
		slog.Error("Check config failed", "Error", err.Error())
		return err
	}
	if dryRun {
		slog.Info("DryRun Config", "config", config)
	}
	if proceed != true {
		slog.Error("checkConfigFile returned false", "Config", config)
		//TODO Prompt user for config file changes
	}

	slog.Debug("Config Before ignition", "config", config)
	// TODO Write missing ssh-keys or password to ignition
	launchFlatcarInstaller(config, dryRun)
	mountUnbootedFlatcar(config)
	copyControlInstallFiles()
	rebootSystem(dryRun)

	return nil
}
