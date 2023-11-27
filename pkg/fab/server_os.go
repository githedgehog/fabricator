package fab

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

//go:embed server_os_butane.tmpl.yaml
var serverButaneTemplate string

type ServerOS struct {
	PasswordHash string `json:"passwordHash,omitempty"`
}

var _ cnc.Component = (*ServerOS)(nil)

func (cfg *ServerOS) Name() string {
	return "server-os"
}

func (cfg *ServerOS) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *ServerOS) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Category:    cfg.Name() + FLAG_CATEGORY_CONFIG_BASE_SUFFIX,
			Name:        "server-password-hash",
			Usage:       "Password hash for the server nodes user 'core' (use 'openssl passwd -5' and pass with '' or escape to avoid shell $ substitution)",
			EnvVars:     []string{"HHFAB_SERVER_PASSWORD_HASH"},
			Destination: &cfg.PasswordHash,
		},
	}
}

func (cfg *ServerOS) Hydrate(preset cnc.Preset) error {
	return nil
}

func (cfg *ServerOS) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, data *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	username := FLATCAR_CONTROL_USER
	authorizedKeys := BaseConfig(get).AuthorizedKeys

	if cfg.PasswordHash == "" && BaseConfig(get).Dev {
		cfg.PasswordHash = DEV_PASSWORD
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

		run(BundleServerOS, STAGE, "ignition-"+server.Name,
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

	return nil
}
