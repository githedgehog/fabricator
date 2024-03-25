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
	"log/slog"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	"golang.org/x/exp/slices"
)

type Base struct {
	cnc.NoValidationComponent

	Source          cnc.Ref  `json:"source,omitempty"`
	Target          cnc.Ref  `json:"target,omitempty"`
	TargetInCluster cnc.Ref  `json:"targetInCluster,omitempty"`
	AuthorizedKeys  []string `json:"authorizedKeys,omitempty"`
	Dev             bool     `json:"dev,omitempty"`

	authorizedKeysFlag cli.StringSlice
}

var _ cnc.Component = (*Base)(nil)

func (cfg *Base) Name() string {
	return "base"
}

func (cfg *Base) IsEnabled(_ cnc.Preset) bool {
	return true
}

func (cfg *Base) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "base-repo",
			Usage:       "Base repo",
			EnvVars:     []string{"HHFAB_BASE_REPO"},
			Destination: &cfg.Source.Repo,
		},
		&cli.StringSliceFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "authorized-key",
			Usage:       "SSH public keys to add to the control node, first will be used for SONiC users",
			EnvVars:     []string{"HHFAB_AUTHORIZED_KEY"},
			Destination: &cfg.authorizedKeysFlag,
		},
		&cli.BoolFlag{
			Category:    cfg.Name() + CategoryConfigBaseSuffix,
			Name:        "dev",
			Usage:       "Enable development mode (dev users & ssh keys)",
			EnvVars:     []string{"HHFAB_DEV"},
			Destination: &cfg.Dev,
		},
	}
}

func (cfg *Base) Hydrate(preset cnc.Preset, _ meta.FabricMode) error {
	cfg.Source = cfg.Source.Fallback(RefSource)
	cfg.Target = cfg.Target.Fallback(RefTarget)
	cfg.TargetInCluster = cfg.TargetInCluster.Fallback(RefTargetInCluster)

	for _, val := range cfg.authorizedKeysFlag.Value() {
		if val == "" {
			continue
		}
		if !slices.Contains(cfg.AuthorizedKeys, val) {
			cfg.AuthorizedKeys = append(cfg.AuthorizedKeys, val)
		}
	}
	if cfg.Dev && !slices.Contains(cfg.AuthorizedKeys, DevSSHKey) {
		cfg.AuthorizedKeys = append(cfg.AuthorizedKeys, DevSSHKey)
	}

	if preset == PresetVLAB {
		cfg.Dev = true
	}

	return nil
}

func (cfg *Base) Build(basedir string, preset cnc.Preset, _ meta.FabricMode, _ cnc.GetComponent, _ *wiring.Data, _ cnc.AddBuildOp, _ cnc.AddRunOp) error {
	if cfg.Dev {
		slog.Warn("Attention! Development mode enabled - this is not secure! Default users and keys will be created.")
	}

	if preset == PresetVLAB {
		key, err := cnc.ReadOrGenerateSSHKey(basedir, DefaultVLABSSHKey, "vlab")
		if err != nil {
			return errors.Wrapf(err, "error reading or generating vlab ssh key")
		}

		cfg.AuthorizedKeys = append([]string{key}, cfg.AuthorizedKeys...)
	}

	return nil
}

func BaseConfig(get cnc.GetComponent) *Base {
	return get((&Base{}).Name()).(*Base)
}
