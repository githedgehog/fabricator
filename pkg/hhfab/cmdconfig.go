// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

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
	"oras.land/oras-go/v2/registry/remote/credentials"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	RegistryConfigFile = ".registry.yaml"
	FabConfigFile      = "fab.yaml"
	IncludeDir         = "include"
	ResultDir          = "result"
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
	Nodes    []fabapi.Node
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

	cacheDirPath := filepath.Join(cacheDir)
	if err := os.MkdirAll(cacheDirPath, 0o700); err != nil {
		return fmt.Errorf("creating cache dir %q: %w", cacheDirPath, err)
	}

	return nil
}

type InitConfig struct {
	WorkDir            string
	CacheDir           string
	Repo               string
	Prefix             string
	ImportConfig       string
	Force              bool
	Wiring             []string
	ImportHostUpstream bool
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
		if c.Force {
			slog.Debug("Overwriting existing config", "config", FabConfigFile)
		} else {
			slog.Warn("Delete manually or re-run with -f/--force to overwrite", "config", FabConfigFile)

			return fmt.Errorf("config %q: %w", FabConfigFile, ErrExist)
		}
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

		l := apiutil.NewFabLoader()
		if err := l.LoadAdd(ctx, fabCfgData); err != nil {
			return fmt.Errorf("loading config to import %q: loading: %w", c.ImportConfig, err)
		}

		if _, _, _, err := fab.GetFabAndNodes(ctx, l.GetClient(), true); err != nil {
			return fmt.Errorf("loading config to import %q: getting fabricator and controls nodes: %w", c.ImportConfig, err)
		}

		slog.Info("Imported config", "source", c.ImportConfig)
	} else {
		if c.ImportHostUpstream {
			username, password, err := getLocalDockerCredsFor(ctx, c.Repo)
			if err != nil {
				return fmt.Errorf("getting creds from docker config: %w", err)
			}

			c.InitConfigInput.RegUpstream = &fabapi.ControlConfigRegistryUpstream{
				Repo:     c.Repo,
				Prefix:   c.Prefix,
				Username: username,
				Password: password,
			}
		}

		fabCfgData, err = fab.InitConfig(ctx, c.InitConfigInput)
		if err != nil {
			return fmt.Errorf("generating fab config: %w", err)
		}

		slog.Info("Generated initial config")
	}

	if err := os.WriteFile(filepath.Join(c.WorkDir, FabConfigFile), fabCfgData, 0o600); err != nil {
		return fmt.Errorf("writing fab config: %w", err)
	}

	if err := os.RemoveAll(filepath.Join(c.WorkDir, IncludeDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing include dir: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(c.WorkDir, IncludeDir), 0o700); err != nil {
		return fmt.Errorf("creating include dir: %w", err)
	}

	if err := os.RemoveAll(filepath.Join(c.WorkDir, ResultDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing result dir: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(c.WorkDir, ResultDir), 0o700); err != nil {
		return fmt.Errorf("creating result dir: %w", err)
	}

	if err := os.RemoveAll(filepath.Join(c.WorkDir, VLABDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing VLAB dir: %w", err)
	}

	if err := importWiring(c); err != nil {
		return err
	}

	slog.Info("Adjust configs (incl. credentials, modes, subnets, etc.)", "file", FabConfigFile)
	slog.Info("Include wiring files (.yaml) or adjust imported ones", "dir", IncludeDir)

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

func Versions(ctx context.Context, workDir, cacheDir string, hMode HydrateMode) error {
	configPath := filepath.Join(workDir, FabConfigFile)
	if _, err := os.Stat(configPath); err != nil && errors.Is(err, os.ErrNotExist) {
		slog.Info("No configuration found", "file", FabConfigFile, "action", "Showing release versions")
		freshFab := fabapi.Fabricator{}
		if err := freshFab.CalculateVersions(fab.Versions); err != nil {
			return fmt.Errorf("calculating default versions: %w", err)
		}
		data, err := yaml.Marshal(freshFab.Status.Versions)
		if err != nil {
			return fmt.Errorf("marshalling versions: %w", err)
		}
		fmt.Println(string(data))

		return nil
	}

	cfg, err := load(ctx, workDir, cacheDir, true, hMode)
	if err != nil {
		return err
	}

	var overrides map[string]map[string]string
	oData, err := yaml.Marshal(cfg.Fab.Spec.Overrides.Versions)
	if err == nil {
		if err := yaml.Unmarshal(oData, &overrides); err != nil {
			slog.Warn("Failed to unmarshal overrides", "error", err)
		}
	} else {
		slog.Warn("Failed to marshal overrides", "error", err)
	}

	freshFab := fabapi.Fabricator{}
	if err := freshFab.CalculateVersions(fab.Versions); err != nil {
		return fmt.Errorf("calculating default versions: %w", err)
	}
	releaseData, err := yaml.Marshal(freshFab.Status.Versions)
	if err != nil {
		return fmt.Errorf("marshalling release versions: %w", err)
	}
	var release map[string]interface{}
	if err := yaml.Unmarshal(releaseData, &release); err != nil {
		return fmt.Errorf("unmarshalling release versions: %w", err)
	}

	if len(overrides) == 0 {
		slog.Info("Printing versions of all components")
		fmt.Println(string(releaseData))

		return nil
	}

	slog.Info("Printing versions of all components (overridden←→release)")
	merged := make(map[string]interface{})
	for category, value := range release {
		if inner, ok := value.(map[string]interface{}); ok {
			newInner := make(map[string]interface{})
			if catOvr, ok := overrides[category]; ok {
				for comp, verValue := range inner {
					verStr, ok := verValue.(string)
					if !ok {
						newInner[comp] = verValue

						continue
					}
					if ovrStr, exists := catOvr[comp]; exists {
						newInner[comp] = fmt.Sprintf("%s←→%s", ovrStr, verStr)
					} else {
						newInner[comp] = verStr
					}
				}
			} else {
				newInner = inner
			}
			merged[category] = newInner
		} else {
			merged[category] = value
		}
	}
	mergedData, err := yaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshalling merged versions: %w", err)
	}
	fmt.Println(string(mergedData))

	return nil
}

func load(ctx context.Context, workDir, cacheDir string, wiringAndHydration bool, mode HydrateMode) (*Config, error) {
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

	f, controls, nodes, err := fab.GetFabAndNodes(ctx, l.GetClient(), true)
	if err != nil {
		return nil, fmt.Errorf("getting fabricator and controls nodes: %w", err)
	}

	cfg := &Config{
		WorkDir:        workDir,
		CacheDir:       cacheDir,
		RegistryConfig: *regConf,
		Fab:            f,
		Controls:       controls,
		Nodes:          nodes,
	}

	if wiringAndHydration {
		if err := cfg.loadHydrateValidate(ctx, mode); err != nil {
			return nil, fmt.Errorf("loading wiring and hydrating: %w", err)
		}

		for _, control := range cfg.Controls {
			if err := control.Validate(ctx, &f.Spec.Config, false); err != nil {
				return nil, fmt.Errorf("validating control node %q: %w", control.Name, err)
			}
		}

		for _, node := range cfg.Nodes {
			if err := node.Validate(ctx, &f.Spec.Config, false); err != nil {
				return nil, fmt.Errorf("validating node %q: %w", node.Name, err)
			}
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

func getLocalDockerCredsFor(ctx context.Context, repo string) (string, string, error) {
	storeOpts := credentials.StoreOptions{}
	credStore, err := credentials.NewStoreFromDocker(storeOpts)
	if err != nil {
		return "", "", fmt.Errorf("loading docker config: %w", err)
	}

	creds, err := credStore.Get(ctx, repo)
	if err != nil {
		return "", "", fmt.Errorf("getting creds for %q: %w", repo, err)
	}

	return creds.Username, creds.Password, nil
}
