package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	RegistryConfigFile = ".registry.yaml"
	FabConfigFile      = "fab.yaml"
	IncludeDir         = "include"
	ResultDir          = "result"
	CacheDirSuffix     = "v1"
	DefaultRepo        = "ghcr.io"
	DefaultPrefix      = "githedgehog"
	YAMLExt            = ".yaml"
)

var (
	ErrExist    = fmt.Errorf("already exists")
	ErrNotExist = fmt.Errorf("does not exist")
	ErrNotDir   = fmt.Errorf("not a directory")
)

type Config struct {
	WorkDir  string
	CacheDir string
	RegistryConfig
	Fab      fabapi.Fabricator
	Controls []fabapi.ControlNode
	Wiring   client.Reader
}

type RegistryConfig struct {
	Repo   string `json:"repo,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

func checkWorkCacheDir(workDir, cacheDir string) error {
	stat, err := os.Stat(workDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("work dir %q: %w", workDir, ErrNotExist)
		}

		return fmt.Errorf("checking work dir: %w", err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("work dir %q: %w", workDir, ErrNotDir)
	}

	cacheDirPath := filepath.Join(cacheDir, CacheDirSuffix)
	if err := os.MkdirAll(cacheDirPath, 0o700); err != nil {
		return fmt.Errorf("creating cache dir %q: %w", cacheDirPath, err)
	}

	return nil
}

type InitConfig struct {
	WorkDir      string
	CacheDir     string
	Repo         string
	Prefix       string
	ImportConfig string
	Wiring       []string
	fab.InitConfigInput
}

func Init(ctx context.Context, c InitConfig) error {
	if err := checkWorkCacheDir(c.WorkDir, c.CacheDir); err != nil {
		return err
	}

	_, err := os.Stat(filepath.Join(c.WorkDir, FabConfigFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking config %q: %w", FabConfigFile, err)
	}
	if err == nil {
		return fmt.Errorf("config %q: %w", FabConfigFile, ErrExist)
	}

	regConf := RegistryConfig{
		Repo:   c.Repo,
		Prefix: c.Prefix,
	}

	if c.Repo != DefaultRepo || c.Prefix != DefaultPrefix {
		slog.Info("Using custom registry config", "repo", c.Repo, "prefix", c.Prefix)
	}

	regConfData, err := yaml.Marshal(regConf)
	if err != nil {
		return fmt.Errorf("marshalling registry config: %w", err)
	}

	if err := os.WriteFile(filepath.Join(c.WorkDir, RegistryConfigFile), regConfData, 0o600); err != nil {
		return fmt.Errorf("writing registry config: %w", err)
	}

	var fabCfgData []byte
	if c.ImportConfig != "" {
		fabCfgData, err = os.ReadFile(c.ImportConfig)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("import config %q: %w", c.ImportConfig, ErrNotExist)
			}

			return fmt.Errorf("reading config %q to import: %w", c.ImportConfig, err)
		}

		if _, err := apiutil.NewFabLoader().Load(fabCfgData); err != nil {
			return fmt.Errorf("importing config %q: loading: %w", c.ImportConfig, err)
		}

		slog.Info("Imported config", "source", c.ImportConfig)
	} else {
		fabCfgData, err = fab.InitConfig(ctx, c.InitConfigInput)
		if err != nil {
			return fmt.Errorf("generating fab config: %w", err)
		}

		slog.Info("Generated initial config")
	}

	if err := os.WriteFile(filepath.Join(c.WorkDir, FabConfigFile), fabCfgData, 0o600); err != nil {
		return fmt.Errorf("writing fab config: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(c.WorkDir, IncludeDir), 0o700); err != nil {
		return fmt.Errorf("creating include dir: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(c.WorkDir, ResultDir), 0o700); err != nil {
		return fmt.Errorf("creating result dir: %w", err)
	}

	if err := importWiring(c); err != nil {
		return err
	}

	// TODO print info about initialized config, created files, next steps, etc.

	return nil
}

func importWiring(c InitConfig) error {
	for _, wiringFile := range c.Wiring {
		name := ""
		source := ""
		if strings.Contains(wiringFile, ":") {
			parts := strings.Split(wiringFile, ":")
			if len(parts) != 2 {
				return fmt.Errorf("importing %q: should be '<path>' or '<path>:<import-name>'", wiringFile) //nolint:goerr113
			}

			source = parts[0]
			name = parts[1]
			if !strings.HasSuffix(name, YAMLExt) {
				name += YAMLExt
			}
		} else {
			source = wiringFile
			name = filepath.Base(wiringFile)
		}

		if !strings.HasSuffix(source, YAMLExt) {
			return fmt.Errorf("importing %q: should have .yaml extension", source) //nolint:goerr113
		}

		data, err := os.ReadFile(source)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("importing %q: %w", source, ErrNotExist)
			}

			return fmt.Errorf("importing %q: reading: %w", source, err)
		}

		target := filepath.Join(c.WorkDir, IncludeDir, name)
		relName := filepath.Join(IncludeDir, name)
		_, err = os.Stat(target)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("importing %q: checking target %q: %w", source, relName, err)
		}
		if err == nil {
			return fmt.Errorf("importing %q: target %q: %w", source, relName, ErrExist)
		}

		if _, err := apiutil.NewWiringLoader().Load(data); err != nil {
			return fmt.Errorf("importing %q: loading: %w", source, err)
		}

		if err := os.WriteFile(target, data, 0o600); err != nil {
			return fmt.Errorf("importing %q: target %q: writing: %w", source, relName, err)
		}

		slog.Info("Imported wiring file", "name", relName, "source", source)
	}

	return nil
}

func Validate(ctx context.Context, workDir, cacheDir string, hMode HydrateMode) error {
	_, err := load(ctx, workDir, cacheDir, true, hMode)
	if err != nil {
		return err
	}

	slog.Info("Fabricator config and wiring are valid")

	return nil
}

func load(ctx context.Context, workDir, cacheDir string, wiring bool, mode HydrateMode) (*Config, error) {
	if err := checkWorkCacheDir(workDir, cacheDir); err != nil {
		return nil, err
	}

	_, err := os.Stat(filepath.Join(workDir, FabConfigFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config %q: %w", FabConfigFile, ErrNotExist)
		}

		return nil, fmt.Errorf("checking config %q: %w", FabConfigFile, err)
	}

	regConf, err := loadRegConf(workDir)
	if err != nil {
		return nil, err
	}

	l := apiutil.NewFabLoader()
	fabCfg, err := os.ReadFile(filepath.Join(workDir, FabConfigFile))
	if err != nil {
		return nil, fmt.Errorf("reading fab config: %w", err)
	}

	if err := l.LoadAdd(ctx, fabCfg); err != nil {
		return nil, fmt.Errorf("loading fab config: %w", err)
	}

	f, controls, err := fab.GetFabAndControls(ctx, l.GetClient(), true)
	if err != nil {
		return nil, fmt.Errorf("getting fabricator and controls nodes: %w", err)
	}

	cfg := &Config{
		WorkDir:        workDir,
		CacheDir:       cacheDir,
		RegistryConfig: *regConf,
		Fab:            f,
		Controls:       controls,
	}

	if wiring {
		if err := cfg.loadHydrateValidate(ctx, mode); err != nil {
			return nil, fmt.Errorf("loading wiring: %w", err)
		}
	}

	return cfg, nil
}

func loadRegConf(workDir string) (*RegistryConfig, error) {
	regConf := &RegistryConfig{
		Repo:   DefaultRepo,
		Prefix: DefaultPrefix,
	}

	regConfData, err := os.ReadFile(filepath.Join(workDir, RegistryConfigFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading registry config: %w", err)
	}

	if err == nil {
		if err := yaml.UnmarshalStrict(regConfData, regConf); err != nil {
			return nil, fmt.Errorf("unmarshalling registry config: %w", err)
		}
	}

	return regConf, nil
}
