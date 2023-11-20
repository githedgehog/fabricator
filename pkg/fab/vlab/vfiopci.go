package vlab

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

func isDeviceBoundToVFIO(dev string) bool {
	vfioDevicePath := filepath.Join("/sys/bus/pci/drivers/vfio-pci", dev)

	_, err := os.Stat(vfioDevicePath)

	return err == nil
}

func bindDeviceToVFIO(dev string) error {
	vfioDevicePath := filepath.Join("/sys/bus/pci/drivers/vfio-pci", dev)
	devicePath := filepath.Join("/sys/bus/pci/devices", dev)

	vendorID, err := os.ReadFile(filepath.Join(devicePath, "vendor"))
	if err != nil {
		return errors.Wrapf(err, "error reading vendor id for %s", dev)
	}
	deviceID, err := os.ReadFile(filepath.Join(devicePath, "device"))
	if err != nil {
		return errors.Wrapf(err, "error reading device id for %s", dev)
	}

	if _, err := os.Stat(vfioDevicePath); err == nil {
		return nil
	}

	// unbind from current driver
	if _, err := os.Stat(filepath.Join(devicePath, "driver")); err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrapf(err, "error checking for driver for %s", dev)
		}
	} else {
		if file, err := os.OpenFile(filepath.Join(devicePath, "driver", "unbind"), os.O_WRONLY, 0o200); err != nil {
			return errors.Wrapf(err, "error opening file to unbind driver for %s", dev)
		} else {
			defer file.Close()

			if _, err := file.WriteString(dev); err != nil {
				return errors.Wrapf(err, "error writing to file to unbind driver for %s", dev)
			}
		}
	}

	if file, err := os.OpenFile("/sys/bus/pci/drivers/vfio-pci/new_id", os.O_WRONLY, 0o200); err != nil {
		return errors.Wrapf(err, "error opening new_id file to bind to vfio-pci for %s", dev)
	} else {
		defer file.Close()

		if _, err := file.WriteString(string(vendorID) + " " + string(deviceID)); err != nil {
			if !os.IsExist(err) {
				return errors.Wrapf(err, "error writing to new_id file to bind to vfio-pci for %s", dev)
			}
		}
	}

	if file, err := os.OpenFile("/sys/bus/pci/drivers/vfio-pci/bind", os.O_WRONLY, 0o200); err != nil {
		return errors.Wrapf(err, "error opening bind file to bind to vfio-pci for %s", dev)
	} else {
		defer file.Close()

		if _, err := file.WriteString(dev); err != nil {
			return errors.Wrapf(err, "error writing to bind file to bind to vfio-pci for %s", dev)
		}
	}

	if _, err := os.Stat(vfioDevicePath); err != nil {
		return errors.Wrapf(err, "%s is still not bound to vfio-pci", dev)
	}

	return nil
}

// func unbindDeviceFromVFIO(dev string) error {
// 	if !isDeviceBoundToVFIO(dev) {
// 		return nil
// 	}

// 	if file, err := os.OpenFile(filepath.Join("/sys/bus/pci/devices", dev, "remove"), os.O_WRONLY, 0o200); err != nil {
// 		return errors.Wrapf(err, "error opening remove file to unbind %s", dev)
// 	} else {
// 		defer file.Close()

// 		if _, err := file.WriteString("1"); err != nil {
// 			return errors.Wrapf(err, "error writing to remove file to unbind from vfio-pci for %s", dev)
// 		}
// 	}

// 	if file, err := os.OpenFile("/sys/bus/pci/rescan", os.O_WRONLY, 0o200); err != nil {
// 		return errors.Wrapf(err, "error opening rescan file to rescan %s", dev)
// 	} else {
// 		defer file.Close()

// 		if _, err := file.WriteString("1"); err != nil {
// 			return errors.Wrapf(err, "error writing to rescan file to rescan %s", dev)
// 		}
// 	}

// 	return nil
// }
