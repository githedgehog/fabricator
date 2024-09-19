package hhfab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dario.cat/mergo"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/wiring"
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
	WithDefaults bool
	ImportConfig string
	Wiring       []string
	Dev          bool
	Airgap       bool
}

func Init(c InitConfig) error {
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

	regConfData, err := yaml.Marshal(regConf)
	if err != nil {
		return fmt.Errorf("marshalling registry config: %w", err)
	}

	if err := os.WriteFile(filepath.Join(c.WorkDir, RegistryConfigFile), regConfData, 0o600); err != nil {
		return fmt.Errorf("writing registry config: %w", err)
	}

	fabCfgData := fab.InitConfigText
	if c.Dev || c.WithDefaults || len(c.ImportConfig) > 0 {
		var fabCfg fabapi.FabConfig

		if len(c.ImportConfig) > 0 {
			importCfgData, err := os.ReadFile(c.ImportConfig)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("import config %q: %w", c.ImportConfig, ErrNotExist)
				}

				return fmt.Errorf("reading config %q to import: %w", c.ImportConfig, err)
			}

			fabCfgData = importCfgData

			if err := yaml.UnmarshalStrict(importCfgData, &fabCfg); err != nil {
				return fmt.Errorf("unmarshalling config %q to import: %w", c.ImportConfig, err)
			}
		}

		if c.Dev {
			if err := mergo.Merge(&fabCfg, fab.DevConfig, mergo.WithOverride); err != nil {
				return fmt.Errorf("merging dev config: %w", err)
			}
		}

		if c.WithDefaults {
			if err := mergo.Merge(&fabCfg, fab.DefaultConfig); err != nil {
				return fmt.Errorf("merging dev config: %w", err)
			}
		}

		if c.WithDefaults || c.Dev {
			fabCfgData, err = yaml.Marshal(fabCfg)
			if err != nil {
				return fmt.Errorf("marshalling fab config: %w", err)
			}
		}
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
			if !strings.HasSuffix(name, ".yaml") {
				name += ".yaml"
			}
		} else {
			source = wiringFile
			name = filepath.Base(wiringFile)
		}

		if !strings.HasSuffix(source, ".yaml") {
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
		_, err = os.Stat(target)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("importing %q: checking target %q: %w", source, name, err)
		}
		if err == nil {
			return fmt.Errorf("importing %q: target %q: %w", source, name, ErrExist)
		}

		if _, err := wiring.Load(data); err != nil {
			return fmt.Errorf("importing %q: loading: %w", source, err)
		}

		if err := os.WriteFile(target, data, 0o600); err != nil {
			return fmt.Errorf("importing %q: target %q: writing: %w", source, name, err)
		}
	}

	// TODO print info about initialized config, created files, next steps, etc.

	return nil
}

func Load(workDir, cacheDir string) (*Config, error) {
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

	regConf := RegistryConfig{
		Repo:   DefaultRepo,
		Prefix: DefaultPrefix,
	}

	regConfData, err := os.ReadFile(filepath.Join(workDir, RegistryConfigFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading registry config: %w", err)
	}

	if err := yaml.UnmarshalStrict(regConfData, &regConf); err != nil {
		return nil, fmt.Errorf("unmarshalling registry config: %w", err)
	}

	return &Config{
		WorkDir:        workDir,
		CacheDir:       cacheDir,
		RegistryConfig: regConf,
	}, nil
}
