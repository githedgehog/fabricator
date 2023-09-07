package fab

import (
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

type Base struct {
	Source          cnc.Ref `json:"source,omitempty"`
	Target          cnc.Ref `json:"target,omitempty"`
	TargetInCluster cnc.Ref `json:"targetInCluster,omitempty"`
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
			Name: "base-repo",
			// Usage:       "Base repo",
			EnvVars:     []string{"HHFAB_BASE_REPO"},
			Destination: &cfg.Source.Repo,
		},
	}
}

func (cfg *Base) Hydrate(preset cnc.Preset) error {
	cfg.Source = cfg.Source.Fallback(REF_SOURCE)
	cfg.Target = cfg.Target.Fallback(REF_TARGET)
	cfg.TargetInCluster = cfg.TargetInCluster.Fallback(REF_TARGET_INCLUSTER)

	return nil
}

func (cfg *Base) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, wiring *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	return nil
}

func BaseConfig(get cnc.GetComponent) *Base {
	return get((&Base{}).Name()).(*Base)
}
