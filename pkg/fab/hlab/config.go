package hlab

import "github.com/pkg/errors"

type Config struct {
	Env         string              `json:"env,omitempty"`
	MgmtNetwork string              `json:"mgmtNetwork,omitempty"`
	Disk        DiskConfig          `json:"disk,omitempty"`
	VMs         map[string]VMConfig `json:"vms,omitempty"`
}

type DiskConfig struct {
	ImageID      string `json:"imageID,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
}

type VMConfig struct {
	Description string       `json:"description,omitempty"`
	CPU         int          `json:"cpu,omitempty"`
	Memory      string       `json:"memory,omitempty"`
	Disk        string       `json:"disk,omitempty"`
	MgmtNetwork bool         `json:"mgmtNetwork,omitempty"`
	HostDevices []HostDevice `json:"hostDevices,omitempty"`
}

type HostDevice struct {
	Name       string `json:"name,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
}

func (c *Config) Validate() error {
	if c.Env == "" {
		return errors.Errorf("env is not specified")
	}

	if c.MgmtNetwork == "" {
		return errors.Errorf("mgmtNetwork is not specified")
	}

	if c.Disk.ImageID == "" {
		return errors.Errorf("disk.imageID is not specified")
	}
	if c.Disk.StorageClass == "" {
		return errors.Errorf("disk.storageClass is not specified")
	}

	if len(c.VMs) == 0 {
		return errors.Errorf("no VMs specified")
	}

	for name, vm := range c.VMs {
		if vm.Description == "" {
			return errors.Errorf("vm[%s].description is not specified", name)
		}
		if vm.CPU == 0 {
			return errors.Errorf("vm[%s].cpu is not specified", name)
		}
		if vm.Memory == "" {
			return errors.Errorf("vm[%s].memory is not specified", name)
		}
		if vm.Disk == "" {
			return errors.Errorf("vm[%s].disk is not specified", name)
		}

		if len(vm.HostDevices) == 0 {
			return errors.Errorf("vm[%s].hostDevices is not specified", name)
		}
		for j, hd := range vm.HostDevices {
			if hd.Name == "" {
				return errors.Errorf("vm[%s].hostDevices[%d].name is not specified", name, j)
			}
			if hd.DeviceName == "" {
				return errors.Errorf("vm[%s].hostDevices[%d].deviceName is not specified", name, j)
			}
		}
	}

	return nil
}
