// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"sigs.k8s.io/yaml"
)

const (
	ConfigName = "recipe.yaml"
)

type Type string

const (
	TypeControl Type = "control"
	TypeNode    Type = "node"
)

var AllTypes = []Type{TypeControl, TypeNode}

type Config struct {
	Type Type   `json:"type"`
	Name string `json:"name"`
}

func LoadConfig(dir string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, ConfigName))
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := &Config{}
	if err := yaml.UnmarshalStrict(data, cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

func (cfg *Config) Validate() error {
	if !slices.Contains(AllTypes, cfg.Type) {
		return fmt.Errorf("invalid type %q", cfg.Type) //nolint:goerr113
	}

	if cfg.Name == "" {
		return fmt.Errorf("name is required") //nolint:goerr113
	}

	return nil
}

func (cfg *Config) Save(dir string) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validating config: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, ConfigName), data, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
