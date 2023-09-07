package fab

import (
	_ "embed"
	"fmt"

	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed vlab_server_butane.tmpl.yaml
var serverButaneTemplate string

type VLAB struct {
	ONIERef                   cnc.Ref  `json:"onieRef,omitempty"`
	FlatcarRef                cnc.Ref  `json:"flatcarRef,omitempty"`
	EEPROMEditRef             cnc.Ref  `json:"eepromEditRef,omitempty"`
	ExtraServerAuthorizedKeys []string `json:"extraServerAuthorizedKeys,omitempty"`
}

var _ cnc.Component = (*VLAB)(nil)

func (cfg *VLAB) Name() string {
	return "vlab"
}

func (cfg *VLAB) IsEnabled(preset cnc.Preset) bool {
	return preset == PRESET_VLAB
}

func (cfg *VLAB) Flags() []cli.Flag {
	return nil
}

func (cfg *VLAB) Hydrate(preset cnc.Preset) error {
	cfg.ONIERef = cfg.ONIERef.Fallback(REF_VLAB_ONIE)
	cfg.FlatcarRef = cfg.FlatcarRef.Fallback(REF_VLAB_FLATCAR)
	cfg.EEPROMEditRef = cfg.EEPROMEditRef.Fallback(REF_VLAB_EEPROM_EDIT)

	// TODO
	return nil
}

func (cfg *VLAB) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, data *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.ONIERef = cfg.ONIERef.Fallback(BaseConfig(get).Source)
	cfg.FlatcarRef = cfg.FlatcarRef.Fallback(BaseConfig(get).Source)
	cfg.EEPROMEditRef = cfg.EEPROMEditRef.Fallback(BaseConfig(get).Source)

	run(BundleVlabFiles, STAGE, "onie-files",
		&cnc.FilesORAS{
			Ref:    cfg.ONIERef,
			Unpack: []string{"onie-kvm_x86_64.qcow2.xz"},
			Files: []cnc.File{
				{Name: "onie-kvm_x86_64.qcow2"},
				{Name: "onie_efi_code.fd"},
				{Name: "onie_efi_vars.fd"},
			},
		})

	run(BundleVlabFiles, STAGE, "flatcar",
		&cnc.FilesORAS{
			Ref:    cfg.FlatcarRef,
			Unpack: []string{"flatcar.img.bz2"},
			Files: []cnc.File{
				{Name: "flatcar.img"},
				{Name: "flatcar_efi_code.fd"},
				{Name: "flatcar_efi_vars.fd"},
			},
		})

	run(BundleVlabFiles, STAGE, "onie-qcow2-eeprom-edit",
		&cnc.FilesORAS{
			Ref: cfg.EEPROMEditRef, // TODO automatically don't cache latest?
			Files: []cnc.File{
				{Name: "onie-qcow2-eeprom-edit", Mode: 0o755},
			},
		})

	username := FLATCAR_CONTROL_USER
	key, error := cnc.ReadOrGenerateSSHKey(basedir, DEFAULT_SSH_KEY, fmt.Sprintf("%s@%s", username, "server")) // TODO server?
	if error != nil {
		return error
	}

	authorizedKets := append(cfg.ExtraServerAuthorizedKeys, key)

	for _, server := range data.Server.All() {
		if server.IsControl() {
			continue
		}
		run(BundleServerOS, STAGE, "ignition-"+server.Name,
			&cnc.FileGenerate{
				File: cnc.File{
					Name: fmt.Sprintf("%s.ignition.json", server.Name),
				},
				Content: cnc.IgnitionFromButaneTemplate(serverButaneTemplate,
					"cfg", cfg,
					"username", username,
					"hostname", server.Name,
					"authorizedKeys", authorizedKets,
				),
			})
	}

	return nil
}
