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

package fab

import (
	"fmt"

	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

type VLAB struct {
	cnc.NoValidationComponent

	ONIERef       cnc.Ref `json:"onieRef,omitempty"`
	FlatcarRef    cnc.Ref `json:"flatcarRef,omitempty"`
	EEPROMEditRef cnc.Ref `json:"eepromEditRef,omitempty"`
}

var _ cnc.Component = (*VLAB)(nil)

func (cfg *VLAB) Name() string {
	return "vlab"
}

func (cfg *VLAB) IsEnabled(preset cnc.Preset) bool {
	return preset == PresetVLAB
}

func (cfg *VLAB) Flags() []cli.Flag {
	return nil
}

func (cfg *VLAB) Hydrate(_ cnc.Preset, _ meta.FabricMode) error {
	cfg.ONIERef = cfg.ONIERef.Fallback(RefVLABONIE)
	cfg.FlatcarRef = cfg.FlatcarRef.Fallback(RefVLABFlarcar)
	cfg.EEPROMEditRef = cfg.EEPROMEditRef.Fallback(RefVLABEEPROMEdit)

	// TODO
	return nil
}

func (cfg *VLAB) Build(_ string, _ cnc.Preset, _ meta.FabricMode, get cnc.GetComponent, data *wiring.Data, run cnc.AddBuildOp, _ cnc.AddRunOp) error {
	cfg.ONIERef = cfg.ONIERef.Fallback(BaseConfig(get).Source)
	cfg.FlatcarRef = cfg.FlatcarRef.Fallback(BaseConfig(get).Source)
	cfg.EEPROMEditRef = cfg.EEPROMEditRef.Fallback(BaseConfig(get).Source)

	run(BundleVlabFiles, Stage, "onie-files",
		&cnc.FilesORAS{
			Ref:    cfg.ONIERef,
			Unpack: []string{"onie-kvm_x86_64.qcow2.xz"},
			Files: []cnc.File{
				{Name: "onie-kvm_x86_64.qcow2"},
				{Name: "onie_efi_code.fd"},
				{Name: "onie_efi_vars.fd"},
			},
		})

	run(BundleVlabFiles, Stage, "flatcar",
		&cnc.FilesORAS{
			Ref: cfg.FlatcarRef,
			Files: []cnc.File{
				{Name: "flatcar.img"},
				{Name: "flatcar_efi_code.fd"},
				{Name: "flatcar_efi_vars.fd"},
			},
		})

	run(BundleVlabFiles, Stage, "onie-qcow2-eeprom-edit",
		&cnc.FilesORAS{
			Ref: cfg.EEPROMEditRef, // TODO automatically don't cache latest?
			Files: []cnc.File{
				{Name: "onie-qcow2-eeprom-edit", Mode: 0o755},
			},
		})

	username := FlatcarControlUser

	for _, server := range data.Server.All() {
		if server.IsControl() {
			continue
		}
		run(BundleServerOS, Stage, "ignition-"+server.Name,
			&cnc.FileGenerate{
				File: cnc.File{
					Name: fmt.Sprintf("%s.ignition.json", server.Name),
				},
				Content: cnc.IgnitionFromButaneTemplate(serverButaneTemplate,
					"cfg", cfg,
					"username", username,
					"hostname", server.Name,
					"authorizedKeys", BaseConfig(get).AuthorizedKeys,
				),
			})
	}

	return nil
}
