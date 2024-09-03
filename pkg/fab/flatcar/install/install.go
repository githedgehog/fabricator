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
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/manifoldco/promptui"
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
		log.Fatal("lsblk error: ", stderr.String())
	}
	if err = json.Unmarshal(stdout, disks); err != nil {
		log.Fatal("Unmarshal:", err)
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

func checkConfigFile(filePath string) {
}
func PreInstallCheck(_ context.Context, basedir string, dryRun bool) error {
	slog.Debug("Using", "basedir", basedir, "dryRun", dryRun)

	configFile := filepath.Join(basedir, ConfigFile)
	config := Config{}

	if _, err := os.Stat(configFile); err != nil {
		if os.IsNotExist(err) {
			slog.Warn("Config file not found", "file", configFile)
		} else {
			return errors.Wrapf(err, "error checking config file %s", configFile)
		}
	}

	configData, err := os.ReadFile(configFile)
	if err != nil {
		return errors.Wrapf(err, "error reading config file %s", configFile)
	}

	if err := yaml.Unmarshal(configData, &config); err != nil {
		return errors.Wrapf(err, "error unmarshalling config file %s", configFile)
	}

	slog.Info("Config", "config", config)

	// TODO implement flatcar installer
	// - read config from "basedir" (using sigs.k8s.io/yaml) if file is present
	// - if values missing prompt user for missing values
	// - if values not missing display values and start countdown
	//
	index := -1
	disks := getDisks()

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

	index, result, err := prompt.Run()

	if err != nil {
		fmt.Printf("Prompt failed %v\n", err)
		return err
	}

	fmt.Printf("You chose %s aka %s", disks.Devices[index].Description, result)

	return err
}
