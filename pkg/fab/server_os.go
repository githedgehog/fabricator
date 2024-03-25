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
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed server_os_butane.tmpl.yaml
var serverButaneTemplate string

//go:embed server_os_hhnet
var hhnetTemplate string

type ServerOS struct {
	cnc.NoValidationComponent

	PasswordHash string  `json:"passwordHash,omitempty"`
	ToolboxRef   cnc.Ref `json:"toolboxRef,omitempty"`
}

var _ cnc.Component = (*ServerOS)(nil)

func (cfg *ServerOS) Name() string {
	return "server-os"
}

func (cfg *ServerOS) IsEnabled(preset cnc.Preset) bool {
	return preset == PresetVLAB
}

func (cfg *ServerOS) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "server-password-hash",
			Usage:       "Password hash for the server nodes user 'core' (use 'openssl passwd -5' and pass with '' or escape to avoid shell $ substitution)",
			EnvVars:     []string{"HHFAB_SERVER_PASSWORD_HASH"},
			Destination: &cfg.PasswordHash,
		},
	}
}

func (cfg *ServerOS) Hydrate(_ cnc.Preset, _ meta.FabricMode) error {
	cfg.ToolboxRef = cfg.ToolboxRef.Fallback(RefToolbox)

	return nil
}

func (cfg *ServerOS) Build(_ string, _ cnc.Preset, _ meta.FabricMode, get cnc.GetComponent, data *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	cfg.ToolboxRef = cfg.ToolboxRef.Fallback(BaseConfig(get).Source)

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
					"passwordHash", cfg.PasswordHash,
				),
			})
	}

	for _, bundle := range []cnc.Bundle{BundleControlInstall, BundleServerInstall} {
		run(bundle, StageInstall0Prep, "toolbox",
			&cnc.FilesORAS{
				Ref: cfg.ToolboxRef,
				Files: []cnc.File{
					{
						Name:          "toolbox.tar",
						InstallTarget: "/opt/hedgehog",
						InstallMode:   0o644,
					},
				},
			})

		install(bundle, StageInstall0Prep, "toolbox-load",
			&cnc.ExecCommand{
				Name: "ctr",
				Args: []string{"image", "import", "/opt/hedgehog/toolbox.tar"},
			})
	}

	run(BundleServerInstall, StageInstall0Prep, "hhnet",
		&cnc.FileGenerate{
			File: cnc.File{
				Name:          "hhnet",
				InstallTarget: "/opt/bin",
				InstallMode:   0o755,
			},
			Content: cnc.FromValue(hhnetTemplate),
		})

	return nil
}
