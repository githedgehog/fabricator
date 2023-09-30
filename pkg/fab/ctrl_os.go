package fab

import (
	_ "embed"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
)

const (
	FLATCAR_CONTROL_USER = "core"
	CONTROL_OS_IGNITION  = "ignition.json"
	DEFAULT_VLAB_SSH_KEY = "ssh-key"
)

//go:embed ctrl_os_butane.tmpl.yaml
var controlButaneTemplate string

type ControlOS struct{}

var _ cnc.Component = (*ControlOS)(nil)

func (cfg *ControlOS) Name() string {
	return "os"
}

func (cfg *ControlOS) IsEnabled(preset cnc.Preset) bool {
	return true
}

func (cfg *ControlOS) Flags() []cli.Flag {
	return nil
}

func (cfg *ControlOS) Hydrate(preset cnc.Preset) error {
	// TODO add ignition template to the config?
	return nil
}

func (cfg *ControlOS) Build(basedir string, preset cnc.Preset, get cnc.GetComponent, data *wiring.Data, run cnc.AddBuildOp, install cnc.AddRunOp) error {
	hostname, err := getControlNodeName(data)
	if err != nil {
		return err
	}
	username := FLATCAR_CONTROL_USER
	authorizedKeys := BaseConfig(get).AuthorizedKeys

	if len(authorizedKeys) == 0 {
		return errors.New("no authorized keys found for control node, you'll not be able to login")
	}

	controlVIP := CONTROL_VIP + CONTROL_VIP_MASK

	ports, err := buildControlPorts(data)
	if err != nil {
		return err
	}

	run(BundleControlOS, STAGE, "ignition-control",
		&cnc.FileGenerate{
			File: cnc.File{
				Name: CONTROL_OS_IGNITION,
			},
			Content: cnc.IgnitionFromButaneTemplate(controlButaneTemplate,
				"cfg", cfg,
				"username", username,
				"hostname", hostname,
				"authorizedKeys", authorizedKeys,
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
