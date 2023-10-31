package hlab

import (
	"bytes"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"
)

const (
	HLAB_LABEL = "hlab.githedgehog.com/env"
)

//go:embed vm.tmpl.yaml
var vmTmpl string

type Service struct {
	svcCfg *ServiceConfig
	cfg    *Config
}

type ServiceConfig struct {
	DryRun            bool
	Basedir           string
	ControlIgnition   string
	ServerIgnitionDir string
	FilesDir          string
	Config            string
	Kubeconfig        string
}

func Load(svcCfg *ServiceConfig) (*Service, error) {
	if svcCfg.ControlIgnition == "" {
		return nil, errors.Errorf("control ignition file is not specified")
	}
	if svcCfg.ServerIgnitionDir == "" {
		return nil, errors.Errorf("server ignition dir is not specified")
	}
	if svcCfg.FilesDir == "" {
		return nil, errors.Errorf("files dir is not specified")
	}
	if svcCfg.Config == "" {
		return nil, errors.Errorf("config file is not specified")
	}
	if svcCfg.Kubeconfig == "" {
		return nil, errors.Errorf("kubeconfig file is not specified")
	}

	data, err := os.ReadFile(svcCfg.Config)
	if err != nil {
		return nil, errors.Wrapf(err, "error reading config file %s", svcCfg.Config)
	}

	cfg := &Config{}
	err = yaml.Unmarshal(data, cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "error parsing config file %s", svcCfg.Config)
	}

	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrapf(err, "error validating config file %s", svcCfg.Config)
	}

	return &Service{
		svcCfg: svcCfg,
		cfg:    cfg,
	}, nil
}

func (s *Service) CreateVMs(cleanupExisting bool) error {
	start := time.Now()

	slog.Info("Creating VMs", "env", s.cfg.Env, "config", s.svcCfg.Config, "kubeconfig", s.svcCfg.Kubeconfig)

	if cleanupExisting {
		if err := s.CleanupVMs(); err != nil {
			return errors.Wrap(err, "error cleaning up existing VMs")
		}
	}

	tmpl, err := template.New("vm").Funcs(sprig.FuncMap()).Parse(vmTmpl)
	if err != nil {
		return errors.Wrapf(err, "error parsing vm template")
	}

	err = os.RemoveAll(s.svcCfg.FilesDir)
	if err != nil {
		return errors.Wrapf(err, "error removing files dir %s", s.svcCfg.FilesDir)
	}
	err = os.Mkdir(s.svcCfg.FilesDir, 0o755)
	if err != nil && !os.IsExist(err) {
		return errors.Wrapf(err, "error creating files dir %s", s.svcCfg.FilesDir)
	}

	for name, vm := range s.cfg.VMs {
		slog.Info("Generating VM", "name", name, "descr", vm.Description)

		ignitionPath := filepath.Join(s.svcCfg.ServerIgnitionDir, name+".ignition.json")
		if name == "control-1" { // TODO support custom control names and more than 1 control
			ignitionPath = s.svcCfg.ControlIgnition
		}
		ignition, err := os.ReadFile(ignitionPath)
		if err != nil {
			return errors.Wrapf(err, "error reading ignition file %s", ignitionPath)
		}

		name = fmt.Sprintf("%s-%s", s.cfg.Env, name)

		buf := &bytes.Buffer{}
		err = tmpl.Execute(buf, map[string]interface{}{
			"env":               s.cfg.Env,
			"name":              name,
			"description":       vm.Description,
			"imageID":           s.cfg.Disk.ImageID,
			"storageClass":      s.cfg.Disk.StorageClass,
			"cpu":               vm.CPU,
			"memory":            vm.Memory,
			"disk":              vm.Disk,
			"mgmtNetworkEnable": vm.MgmtNetwork,
			"mgmtNetworkName":   s.cfg.MgmtNetwork,
			"hostDevices":       vm.HostDevices,
			"ignition":          string(ignition),
		})
		if err != nil {
			return errors.Wrapf(err, "error executing vm template")
		}

		vmFile := filepath.Join(s.svcCfg.FilesDir, name+".yaml")
		err = os.WriteFile(vmFile, buf.Bytes(), 0o644)
		if err != nil {
			return errors.Wrapf(err, "error writing vm file")
		}

		slog.Debug("VM file generated", "name", name, "path", vmFile)
	}

	if s.svcCfg.DryRun {
		slog.Info("Dry run, skipping, generated VMs located in", "dir", s.svcCfg.FilesDir)
		return nil
	}

	slog.Info("Creating VMs and their dependencies")

	if err := s.kubectl("apply", "-f", s.svcCfg.FilesDir); err != nil {
		return errors.Wrapf(err, "error creating VMs")
	}

	slog.Info("Waiting for VMs to be ready")

	if err := s.kubectl("wait", "--for=condition=AgentConnected", "vm", "-l", HLAB_LABEL+"="+s.cfg.Env, "--timeout=180s"); err != nil {
		return errors.Wrapf(err, "error waiting for VMs to be ready")
	}

	time.Sleep(5 * time.Second)

	vmsQuery := []string{"get"}
	for name := range s.cfg.VMs {
		vmsQuery = append(vmsQuery, fmt.Sprintf("vmi/%s-%s", s.cfg.Env, name))
	}
	if err := s.kubectl(vmsQuery...); err != nil {
		return errors.Wrapf(err, "error getting VMs")
	}

	slog.Info("Done", "took", time.Since(start))

	return nil
}

func (s *Service) CleanupVMs() error {
	slog.Info("Cleaning up existing VMs and their dependencies")

	return errors.Wrapf(s.kubectl("delete", "vm,pvc,secret", "-l", HLAB_LABEL+"="+s.cfg.Env, "--wait=true", "--timeout=90s"), "error cleaning up VMs")
}

func (s *Service) kubectl(args ...string) error {
	argsStr := strings.Join(args, " ")
	slog.Debug("Running", "cmd", "kubectl", "args", argsStr)

	if s.svcCfg.DryRun {
		slog.Info("Dry run, skipping")
		return nil
	}

	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	cmd.Env = append(os.Environ(), "KUBECONFIG="+s.svcCfg.Kubeconfig)

	return errors.Wrapf(cmd.Run(), "error running kubectl %s", argsStr)
}
