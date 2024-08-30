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
	_ "embed"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	"go.githedgehog.com/fabricator/pkg/fab/flatcar/install"
)

const (
	FlatcarControlUser = "core"
	ControlOSIgnition  = "ignition.json"
	DefaultVLABSSHKey  = "ssh-key"
)

//go:embed ctrl_os_butane.tmpl.yaml
var controlButaneTemplate string

type ControlOS struct {
	cnc.NoValidationComponent

	PasswordHash string `json:"passwordHash,omitempty"`

	// keep this name in sync
	LiveImageRoot cnc.Ref `json:"liveImageRef,omitempty"` // TODO replace with needed files for control iso gen
	OEMCpio       cnc.Ref `json:"OEMCpio,omitempty"`      // TODO replace with needed files for control iso gen
}

var _ cnc.Component = (*ControlOS)(nil)

func (cfg *ControlOS) Name() string {
	return "control-os"
}

func (cfg *ControlOS) IsEnabled(_ cnc.Preset) bool {
	return true
}

func (cfg *ControlOS) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "control-password-hash",
			Usage:       "Password hash for the control node user 'core' (use 'openssl passwd -5' and pass with '' or escape to avoid shell $ substitution)",
			EnvVars:     []string{"HHFAB_CONTROL_PASSWORD_HASH"},
			Destination: &cfg.PasswordHash,
		},
	}
}

func (cfg *ControlOS) Hydrate(_ cnc.Preset, _ meta.FabricMode) error {
	// TODO add ignition template to the config?

	cfg.LiveImageRoot = cfg.LiveImageRoot.Fallback(RefLiveImageTree)
	cfg.OEMCpio = cfg.OEMCpio.Fallback(RefOEMCpio)

	return nil
}

func (cfg *ControlOS) Build(_ string, _ cnc.Preset, _ meta.FabricMode, get cnc.GetComponent, data *wiring.Data, run cnc.AddBuildOp, _ cnc.AddRunOp) error {
	hostname, err := getControlNodeName(data)
	if err != nil {
		return err
	}
	username := FlatcarControlUser
	authorizedKeys := BaseConfig(get).AuthorizedKeys

	if cfg.PasswordHash == "" && BaseConfig(get).Dev {
		cfg.PasswordHash = DevPassword
	}

	if cfg.PasswordHash != "" && !strings.HasPrefix(cfg.PasswordHash, "$5$") {
		return errors.Errorf("control node password hash is expected to be a hash, not a password, use 'openssl passwd -5' to generate one")
	}

	if len(authorizedKeys) == 0 && cfg.PasswordHash == "" {
		return errors.Errorf("no authorized keys or password found for control node, you'll not be able to login")
	}

	controlVIP := ControlVIP + ControlVIPMask

	run(BundleControlOS, Stage, "ignition-control",
		&cnc.FileGenerate{
			File: cnc.File{
				Name: ControlOSIgnition,
			},
			Content: cnc.IgnitionFromButaneTemplate(controlButaneTemplate,
				"cfg", cfg,
				"username", username,
				"hostname", hostname,
				"authorizedKeys", authorizedKeys,
				"ports", buildControlPorts(data),
				"controlVIP", controlVIP,
				"passwordHash", cfg.PasswordHash,
			),
		})

	// TODO replace with needed files and actions
	// the rest of the method is just an example for the control iso gen
	// Needed files:
	// the live image tree that I pushed to ghcr
	//   Ignition file it needs to have an ignition file, with the binary that asks questions or starts the installer
	//   That ignition file needs to be packaged as a cpio.gz archive

	//cfg.FlatcarRef = cfg.FlatcarRef.Fallback(BaseConfig(get).Source)
	run(BundleControlISO, Stage, "Live-Image-Root",
		//&cnc.SyncOCI{
		&cnc.FilesORAS{
			Ref: cfg.LiveImageRoot,
			Files: []cnc.File{
				{Name: "iso_root"},
			},
		})
	run(BundleControlISO, Stage, "CPIO-OEM",
		//&cnc.SyncOCI{
		&cnc.FilesORAS{
			Ref: cfg.OEMCpio,
			Files: []cnc.File{
				{Name: "oem.cpio.gz"},
			},
		})

	//run(BundleControlISO, Stage, "flatcar",
	//&cnc.SyncOCI{
	//	Ref: cfg.FlatcarRef,
	//	Files: []cnc.File{
	//		{Name: "flatcar.img"},
	//		{Name: "flatcar_efi_code.fd"},
	//		{Name: "flatcar_efi_vars.fd"},
	//	},
	//	})
	run(BundleControlISO, Stage, "flatcar-install-config",
		&cnc.FileGenerate{
			File: cnc.File{
				Name: install.ConfigFile,
			},
			Content: cnc.YAMLFrom(install.Config{
				Hostname:       hostname,
				Username:       username,
				PasswordHash:   cfg.PasswordHash,
				AuthorizedKeys: authorizedKeys,
			}),
		})

	return nil
}

func getControlNodeName(data *wiring.Data) (string, error) {
	for _, server := range data.Server.All() {
		if server.Spec.Type == wiringapi.ServerTypeControl {
			return server.Name, nil
		}
	}

	return "", errors.New("no control node found")
}

type renderPort struct {
	ID         string
	PortName   string
	SwitchName string
	IP         string
	MAC        string
}

func buildControlPorts(data *wiring.Data) []renderPort {
	res := []renderPort{}

	for idx, conn := range data.Connection.All() {
		if conn.Spec.Management == nil {
			continue
		}

		switchName := conn.Spec.Management.Link.Switch.DeviceName()
		portName := conn.Spec.Management.Link.Server.LocalPortName()
		mac := conn.Spec.Management.Link.Server.MAC

		port := renderPort{
			ID:         fmt.Sprintf("1%02d", idx),
			SwitchName: switchName,
			PortName:   portName,
			IP:         conn.Spec.Management.Link.Server.IP,
			MAC:        mac,
		}

		res = append(res, port)
	}

	return res
}
