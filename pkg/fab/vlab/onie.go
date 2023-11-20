package vlab

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// TODO pass real ONIE version
const onieEepromConfigTmpl = `
tlvs:
  product_name: Hedgehog ONIE kvm_x86_64 Virtual Machine
  part_number: hh-onie-kvm_x86_64-{{ .name }}
  serial_number: {{ .serial }}
  mac_base:
    - 0x0c
    - 0x20
    - 0x12
    - 0xfe
    - 0x{{ .macPart }}
    - 0x00
  manufacture_date: {{ .now }}
  device_version: 1
  label_revision: null
  platform_name: x86_64-kvm_x86_64-r0
  onie_version: master-01091853-dirty
  num_macs: {{ .ifaces }}
  manufacturer: Caprica Systems
  country_code: US
  vendor: Hedgehog
  diag_version: null
  service_tag: null
  vendor_extension: null
`

func (vm *VM) OnieEepromConfig() (string, error) {
	if vm.Type != VMTypeSwitchVS {
		return "", errors.Errorf("only virtual switches have ONIE EEPROM config")
	}

	return executeTemplate(onieEepromConfigTmpl, map[string]any{
		"name":    vm.Name,
		"serial":  uuid.New().String(),
		"macPart": fmt.Sprintf("%02d", vm.ID),
		"now":     time.Now().Format(time.DateTime),
		"ifaces":  len(vm.Interfaces),
	})
}

func executeTemplate(tmplText string, data any) (string, error) {
	tmplText = strings.TrimPrefix(tmplText, "\n")
	tmplText = strings.TrimSpace(tmplText)

	tmpl, err := template.New("tmpl").Parse(tmplText)
	if err != nil {
		return "", errors.Wrapf(err, "error parsing template")
	}

	buf := bytes.NewBuffer(nil)
	err = tmpl.Execute(buf, data)
	if err != nil {
		return "", errors.Wrapf(err, "error executing template")
	}

	return buf.String(), nil
}
