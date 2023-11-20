package vlab

import (
	"os"

	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"
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

func readConfig(path string) (*Config, error) {
	cfg := &Config{}

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "error reading config file %s", path)
	}

	err = yaml.Unmarshal(data, cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "error parsing config file %s", path)
	}

	return cfg, nil
}
