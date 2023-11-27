package fab

import (
	"log/slog"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	"golang.org/x/exp/slices"
)

type Base struct {
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

func (cfg *Base) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *Base) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + FLAG_CATEGORY_CONFIG_BASE_SUFFIX,
			Name:        "base-repo",
			Usage:       "Base repo",
			EnvVars:     []string{"HHFAB_BASE_REPO"},
			Destination: &cfg.Source.Repo,
		},
		&cli.StringSliceFlag{
			Category:    cfg.Name() + FLAG_CATEGORY_CONFIG_BASE_SUFFIX,
			Name:        "authorized-key",
			Usage:       "SSH public keys to add to the control node, first will be used for SONiC users",
			EnvVars:     []string{"HHFAB_AUTHORIZED_KEY"},
			Destination: &cfg.authorizedKeysFlag,
		},
		&cli.BoolFlag{
			Category:    cfg.Name() + FLAG_CATEGORY_CONFIG_BASE_SUFFIX,
			Name:        "dev",
			Usage:       "Enable development mode (dev users & ssh keys)",
			EnvVars:     []string{"HHFAB_DEV"},
			Destination: &cfg.Dev,
		},
	}
}

func (cfg *Base) Hydrate(preset cnc.Preset) error {
	cfg.Source = cfg.Source.Fallback(REF_SOURCE)
	cfg.Target = cfg.Target.Fallback(REF_TARGET)
	cfg.TargetInCluster = cfg.TargetInCluster.Fallback(REF_TARGET_INCLUSTER)

	for _, val := range cfg.authorizedKeysFlag.Value() {
		if val == "" {
			continue
		}
		if !slices.Contains(cfg.AuthorizedKeys, val) {
			cfg.AuthorizedKeys = append(cfg.AuthorizedKeys, val)
		}
	}
	if cfg.Dev && !slices.Contains(cfg.AuthorizedKeys, DEV_SSH_KEY) {
		cfg.AuthorizedKeys = append(cfg.AuthorizedKeys, DEV_SSH_KEY)
	}

	if preset == PRESET_VLAB {
		cfg.Dev = true
	}

	return nil
}

func (cfg *Base) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	if cfg.Dev {
		slog.Warn("Attention! Development mode enabled - this is not secure! Default users and keys will be created.")
	}

	if preset == PRESET_VLAB {
		key, err := cnc.ReadOrGenerateSSHKey(basedir, DEFAULT_VLAB_SSH_KEY, "vlab")
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
