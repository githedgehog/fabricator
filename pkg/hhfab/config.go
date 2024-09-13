package hhfab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

const (
	CurrentContextFile = ".current"
	CredentialsFile    = "credentials.yaml"
	ContextConfigFile  = "context.yaml"

	DefaultContext = "default"

	DefaultRepo   = "ghcr.io"
	DefaultPrefix = "githedgehog"
)

var ErrContextNotExist = fmt.Errorf("does not exist, create it first using 'hhfab create'")

// Runtime configuration
type Config struct {
	CacheDir string
	BaseDir  string

	CredentialsFile string
	Credentials     RegistryCredentialsStore

	IsContext  bool
	Context    string
	ContextDir string
	ContextConfig
}

type ContextConfig struct {
	Dev      bool           `json:"dev,omitempty"`  // TODO autoset some props for dev
	VLAB     bool           `json:"vlab,omitempty"` // TODO prep for VLAB deployment - validate wiring and etc? default ifaces/disks
	Registry RegistryConfig `json:"registry,omitempty"`
}

type RegistryConfig struct {
	Repo   string `json:"repo,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

func Load(baseDir, cacheDir string, isContext bool, context string) (*Config, error) {
	cfg := &Config{
		BaseDir:  baseDir,
		CacheDir: cacheDir,
	}

	if err := os.MkdirAll(cfg.BaseDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating base dir: %w", err)
	}

	if err := os.MkdirAll(cfg.CacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	cfg.CredentialsFile = filepath.Join(cfg.BaseDir, CredentialsFile)
	cfg.Credentials = RegistryCredentialsStore{}
	if err := cfg.Credentials.Load(cfg.CredentialsFile); err != nil {
		return nil, fmt.Errorf("loading credentials: %w", err)
	}

	if isContext {
		cfg.IsContext = true
		cfg.Context = context
		cfg.ContextDir = filepath.Join(cfg.BaseDir, context)

		contextFile := filepath.Join(cfg.ContextDir, ContextConfigFile)

		if _, err := os.Stat(contextFile); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("context %q: %w", cfg.Context, ErrContextNotExist)
			}

			return nil, fmt.Errorf("checking context: %w", err)
		}

		ctxCfgData, err := os.ReadFile(contextFile)
		if err != nil {
			return nil, fmt.Errorf("reading context: %w", err)
		}

		ctxCfg := ContextConfig{}
		if err := yaml.UnmarshalStrict(ctxCfgData, &ctxCfg); err != nil {
			return nil, fmt.Errorf("unmarshalling context: %w", err)
		}

		cfg.ContextConfig = ctxCfg
	}

	return cfg, nil
}
