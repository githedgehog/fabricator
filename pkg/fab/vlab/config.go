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
	HHFAB_CFG_PREFIX               = ".hhfab.fabric.githedgehog.com"
	HHFAB_CFG_TYPE                 = "type" + HHFAB_CFG_PREFIX
	HHFAB_CFG_SERIAL               = "serial" + HHFAB_CFG_PREFIX
	HHFAB_CFG_LINK_PREFIX          = "link" + HHFAB_CFG_PREFIX + "/"
	HHFAB_CFG_PCI_PREFIX           = "pci@"
	HHFAB_CFG_SERIAL_SCHEME_SSH    = "ssh://"
	HHFAB_CFG_SERIAL_SCHEME_TELNET = "telnet://"
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
			if key == HHFAB_CFG_TYPE {
				if value == string(ConfigSwitchTypeHW) {
					swCfg.Type = ConfigSwitchTypeHW
				} else if value != string(ConfigSwitchTypeVS) {
					return nil, errors.Errorf("unknown switch type %s for switch %s", value, sw.Name)
				}
			} else if key == HHFAB_CFG_SERIAL {
				if !strings.HasPrefix(value, HHFAB_CFG_SERIAL_SCHEME_SSH) && !strings.HasPrefix(value, HHFAB_CFG_SERIAL_SCHEME_TELNET) {
					return nil, errors.Errorf("unknown serial scheme %s for switch %s", value, sw.Name)
				}
				swCfg.Serial = value
			} else if strings.HasPrefix(key, HHFAB_CFG_LINK_PREFIX) {
				port := key[len(HHFAB_CFG_LINK_PREFIX):]
				if !strings.HasPrefix(port, "Management0") && !strings.HasPrefix(port, "Ethernet") {
					return nil, errors.Errorf("unknown link type for switch %s port %s (only Management0 and EthernetX supported)", sw.Name, port)
				}
				if !strings.HasPrefix(value, HHFAB_CFG_PCI_PREFIX) {
					return nil, errors.Errorf("unknown link PCI address %s for switch %s port %s", value, sw.Name, port)
				}

				cfg.Links[sw.Name+"/"+port] = LinkConfig{
					PCIAddress: value[len(HHFAB_CFG_PCI_PREFIX):],
				}
			}
		}

		cfg.Switches[sw.Name] = swCfg
	}

	return cfg, nil
}
