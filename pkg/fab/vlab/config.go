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

package vlab

import (
	"strings"

	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/pkg/wiring"
)

type Config struct {
	VMs      VMsConfig               `json:"vms,omitempty"`
	Switches map[string]SwitchConfig `json:"switches,omitempty"`
	Links    map[string]LinkConfig   `json:"links,omitempty"`
}

type VMsConfig struct {
	Control VMConfig `json:"control,omitempty"`
	Server  VMConfig `json:"server,omitempty"`
	Switch  VMConfig `json:"switch,omitempty"`
}

type VMConfig struct {
	CPU  int `json:"cpu,omitempty"`  // in cores
	RAM  int `json:"ram,omitempty"`  // in MB
	Disk int `json:"disk,omitempty"` // in GB
}

func (cfg VMConfig) DefaultsFrom(def VMConfig) VMConfig {
	if cfg.CPU == 0 {
		cfg.CPU = def.CPU
	}
	if cfg.RAM == 0 {
		cfg.RAM = def.RAM
	}
	if cfg.Disk == 0 {
		cfg.Disk = def.Disk
	}

	return cfg
}

func (cfg VMConfig) OverrideBy(def VMConfig) VMConfig {
	if def.CPU != 0 {
		cfg.CPU = def.CPU
	}
	if def.RAM != 0 {
		cfg.RAM = def.RAM
	}
	if def.Disk != 0 {
		cfg.Disk = def.Disk
	}

	return cfg
}

type SwitchConfig struct {
	Type   ConfigSwitchType `json:"type,omitempty"`
	Serial string           `json:"serial,omitempty"`
}

type ConfigSwitchType string

const (
	ConfigSwitchTypeVS ConfigSwitchType = "vs"
	ConfigSwitchTypeHW ConfigSwitchType = "hw"
)

type LinkConfig struct {
	PCIAddress string `json:"pci,omitempty"`
	// MAC            string `json:"mac,omitempty"`
}

const (
	HHFabCfgPrefix             = ".hhfab.fabric.githedgehog.com"
	HHFabCfgType               = "type" + HHFabCfgPrefix
	HHFabCfgSerial             = "serial" + HHFabCfgPrefix
	HHFabCfgLinkPrefix         = "link" + HHFabCfgPrefix + "/"
	HHFabCfgPCIPrefix          = "pci@"
	HHFabCfgSerialSchemeSSH    = "ssh://"
	HHFabCfgSerialSchemeTelnet = "telnet://"
)

func readConfigFromWiring(data *wiring.Data) (*Config, error) {
	if data == nil {
		return nil, errors.Errorf("no wiring data")
	}

	cfg := &Config{
		Switches: map[string]SwitchConfig{},
		Links:    map[string]LinkConfig{},
	}

	for _, sw := range data.Switch.All() {
		swCfg := SwitchConfig{
			Type: ConfigSwitchTypeVS,
		}

		for key, value := range sw.Annotations {
			if key == HHFabCfgType {
				if value == string(ConfigSwitchTypeHW) {
					swCfg.Type = ConfigSwitchTypeHW
				} else if value != string(ConfigSwitchTypeVS) {
					return nil, errors.Errorf("unknown switch type %s for switch %s", value, sw.Name)
				}
			} else if key == HHFabCfgSerial {
				if !strings.HasPrefix(value, HHFabCfgSerialSchemeSSH) && !strings.HasPrefix(value, HHFabCfgSerialSchemeTelnet) {
					return nil, errors.Errorf("unknown serial scheme %s for switch %s", value, sw.Name)
				}
				swCfg.Serial = value
			} else if strings.HasPrefix(key, HHFabCfgLinkPrefix) {
				port := key[len(HHFabCfgLinkPrefix):]
				if port != "M1" && !strings.HasPrefix(port, "E1/") {
					return nil, errors.Errorf("unknown link type for switch %s port %s (only M1 and E1/X supported)", sw.Name, port)
				}
				if !strings.HasPrefix(value, HHFabCfgPCIPrefix) {
					return nil, errors.Errorf("unknown link PCI address %s for switch %s port %s", value, sw.Name, port)
				}

				cfg.Links[sw.Name+"/"+port] = LinkConfig{
					PCIAddress: value[len(HHFabCfgPCIPrefix):],
				}
			}
		}

		cfg.Switches[sw.Name] = swCfg
	}

	return cfg, nil
}
