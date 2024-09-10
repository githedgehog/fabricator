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
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pkg/errors"
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
		slog.Error("lsblk error", "Stderr", stderr.String())
	}
	if err = json.Unmarshal(stdout, disks); err != nil {
		slog.Error("Unmarshal from lsblk", err.Error())
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
			return false, errors.Wrapf(err, "error checking config file %s", configFilePath)
		}
	}

	configData, err := os.ReadFile(configFilePath)
	if err != nil {
		return false, errors.Wrapf(err, "error reading config file %s", configFilePath)
	}

	if err := yaml.Unmarshal(configData, config); err != nil {
		return false, errors.Wrapf(err, "error unmarshalling config file %s", configFilePath)
	}
	slog.Info("ConfigCheck", "config", config)
	if config.PasswordHash == "" && len(config.AuthorizedKeys) == 0 {
		// we need a way to login to the installed systemd
		populatePassword(config)

	}
	if config.BlockDevicePath == "" {
		err = populateBlockDevice(config)
		if err != nil {
			return false, errors.Wrapf(err, "Error choosing block device")
		}

	}

	return true, nil

}

func populatePassword(config *Config) {
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
func launchFlatcarInstaller(config Config) error {
	// TODO this is where we will exec the flatcar installer with the values from the ignition.json file
	//TODO read the config file to find install device

	slog.Info("Running Install", "BlockDevice", config.BlockDevicePath)
	installCmd := exec.Command("sudo /usr/bin/flatcar-install", "-i", "/mnt/hedgehog/ignition.json", "-d", config.BlockDevicePath, "-f", "/mnt/hedgehog/flatcar_production_image.bin.bz2")
	var stderr bytes.Buffer
	installCmd.Stderr = &stderr

	_, err := installCmd.Output()
	if err != nil {
		slog.Error("flatcar install error", "Stderr", stderr.String())
	}
	return nil
}

// mountUnbootedFlatcar is reponsible for booting the block device at a known location
func mountUnbootedFlatcar(config Config) error {
	//TODO sudo partprobe config.BlockDevicePath
	// attempt to expand image to

	slog.Info("MountUnbootedFlatcar", "BlockDevice", config.BlockDevicePath)
	return nil

}

// copyControlInstallFiles will take the control-os
func copyControlInstallFiles() error {
	// TODO after the installer is done, we need to copy the first boot files onto the newely installed but not-yet-booted flatcar system
	// this is most likely going to be in /opt/hedgehog
	// Need to sudo mount the config.BlockDevicePath to a temp location
	// Need to rsync? cp -r ? , something all of the control-os dir to the base system
	slog.Info("CopyInstall Files", "Destination", "/mnt/root/hedgehog", "Src", "/mnt/hedgehot/control-os")
	return nil
}

func rebootSystem() {
	slog.Info("Rebooting Live Image")
}

func PreInstallCheck(_ context.Context, basedir string, dryRun bool) error {
	slog.Debug("Using", "basedir", basedir, "dryRun", dryRun)

	configFile := filepath.Join(basedir, ConfigFile)
	config := Config{}

	proceed, err := checkConfigFile(configFile, &config)
	if err != nil {
		slog.Info("Check config failed", "Error", err.Error())
		return err
	}
	if dryRun {
		slog.Info("Config", "config", config)
		return nil
	}
	if proceed != true {
		//TODO Prompt user for config file changes
	}

	slog.Info("Config Before ignition", "config", config)
	// TODO Write missing ssh-keys or password to ignition
	// this might not be true, since we need
	launchFlatcarInstaller(config)
	mountUnbootedFlatcar(config)
	copyControlInstallFiles()
	rebootSystem()

	return nil
}
