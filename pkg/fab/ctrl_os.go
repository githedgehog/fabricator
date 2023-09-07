package fab

import (
	_ "embed"
	"fmt"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
	"golang.org/x/exp/slices"
)

const (
	FLATCAR_CONTROL_USER = "core"
	CONTROL_OS_IGNITION  = "ignition.json"
	DEFAULT_SSH_KEY      = "ssh-key"
)

//go:embed ctrl_os_butane.tmpl.yaml
var controlButaneTemplate string

type ControlOS struct {
	ExtraAuthorizedKeys     []string `json:"extraAuthorizedKeys,omitempty"`
	extraAuthorizedKeysFlag cli.StringSlice
}

var _ cnc.Component = (*ControlOS)(nil)

func (cfg *ControlOS) Name() string {
	return "os"
}

func (cfg *ControlOS) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *ControlOS) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringSliceFlag{
			Name:        "extra-authorized-key",
			Usage:       "Extra SSH public keys to add to the control node",
			EnvVars:     []string{"HHFAB_EXTRA_AUTHORIZED_KEY"},
			Destination: &cfg.extraAuthorizedKeysFlag,
		},
	}
}

func (cfg *ControlOS) Hydrate(preset cnc.Preset) error {
	// TODO add ignition template to the config?
	for _, val := range cfg.extraAuthorizedKeysFlag.Value() {
		if val == "" {
			continue
		}
		if !slices.Contains(cfg.ExtraAuthorizedKeys, val) {
			cfg.ExtraAuthorizedKeys = append(cfg.ExtraAuthorizedKeys, val)
		}
	}

	return nil
}

func (cfg *ControlOS) Build(basedir string, preset cnc.Preset, _ cnc.GetComponent, data *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	hostname, err := getControlNodeName(data)
	if err != nil {
		return err
	}

	username := FLATCAR_CONTROL_USER

	key, error := cnc.ReadOrGenerateSSHKey(basedir, DEFAULT_SSH_KEY, fmt.Sprintf("%s@%s", username, hostname))
	if error != nil {
		return error
	}

	authorizedKets := append(cfg.ExtraAuthorizedKeys, key)

	ports, err := buildControlPorts(data)
	if err != nil {
		return err
	}

	controlVIP := CONTROL_VIP + CONTROL_VIP_MASK

	run(BundleControlOS, STAGE, "ignition-control",
		&cnc.FileGenerate{
			File: cnc.File{
				Name: CONTROL_OS_IGNITION,
			},
			Content: cnc.IgnitionFromButaneTemplate(controlButaneTemplate,
				"cfg", cfg,
				"username", username,
				"hostname", hostname,
				"authorizedKeys", authorizedKets,
				"ports", ports,
				"controlVIP", controlVIP,
			),
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
	PortName   string
	SwitchName string
	IP         string
}

func buildControlPorts(data *wiring.Data) ([]renderPort, error) {
	res := []renderPort{}

	for _, conn := range data.Connection.All() {
		if conn.Spec.Management == nil {
			continue
		}

		port := renderPort{
			PortName:   conn.Spec.Management.Link.Server.LocalPortName(),
			IP:         conn.Spec.Management.Link.Server.IP,
			SwitchName: conn.Spec.Management.Link.Switch.DeviceName(),
		}

		res = append(res, port)
	}

	return res, nil
}
