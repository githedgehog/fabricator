package fab

import (
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	"golang.org/x/exp/slices"
)

type Base struct {
	Source              cnc.Ref  `json:"source,omitempty"`
	Target              cnc.Ref  `json:"target,omitempty"`
	TargetInCluster     cnc.Ref  `json:"targetInCluster,omitempty"`
	ExtraAuthorizedKeys []string `json:"extraAuthorizedKeys,omitempty"`
	Dev                 bool

	extraAuthorizedKeysFlag cli.StringSlice
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
			Name:        "base-repo",
			Usage:       "Base repo",
			EnvVars:     []string{"HHFAB_BASE_REPO"},
			Destination: &cfg.Source.Repo,
		},
		&cli.StringSliceFlag{
			Name:        "extra-authorized-key",
			Usage:       "Extra SSH public keys to add to the control node",
			EnvVars:     []string{"HHFAB_EXTRA_AUTHORIZED_KEY"},
			Destination: &cfg.extraAuthorizedKeysFlag,
		},
		&cli.BoolFlag{
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

	for _, val := range cfg.extraAuthorizedKeysFlag.Value() {
		if val == "" {
			continue
		}
		if !slices.Contains(cfg.ExtraAuthorizedKeys, val) {
			cfg.ExtraAuthorizedKeys = append(cfg.ExtraAuthorizedKeys, val)
		}
	}

	if preset == PRESET_VLAB {
		cfg.Dev = true
	}

	return nil
}

func (cfg *Base) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	return nil
}

func BaseConfig(get cnc.GetComponent) *Base {
	return get((&Base{}).Name()).(*Base)
}
