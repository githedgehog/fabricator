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
	"reflect"
	"sort"
	"strings"

	"encoding/json"

	"github.com/invopop/jsonschema"
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
	cfg, err := load(ctx, workDir, cacheDir, true, hMode)
	if err != nil {
		return err
	}

	slog.Info("Printing versions of all components")

	data, err := yaml.Marshal(cfg.Fab.Status.Versions)
	if err != nil {
		return fmt.Errorf("marshalling versions: %w", err)
	}

	fmt.Println(string(data))

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

func GenerateJSONSchema() (string, error) {
	reflector := &jsonschema.Reflector{
		DoNotReference:            true,
		ExpandedStruct:            true,
		AllowAdditionalProperties: false,
	}

	wiringLoader := apiutil.NewWiringLoader()
	fabLoader := apiutil.NewFabLoader()

	wiringScheme := wiringLoader.GetScheme()
	fabScheme := fabLoader.GetScheme()

	kindSchemas := make(map[string]*jsonschema.Schema)
	kindVersions := make(map[string]map[string]bool)
	allAPIVersions := make(map[string]bool)

	for gvk, typ := range wiringScheme.AllKnownTypes() {
		if !isResourceType(gvk.Kind) {
			continue
		}

		instance := newInstance(typ)
		schema := reflector.Reflect(instance)
		kindSchemas[gvk.Kind] = schema

		if _, ok := kindVersions[gvk.Kind]; !ok {
			kindVersions[gvk.Kind] = make(map[string]bool)
		}

		version := gvk.GroupVersion().String()
		kindVersions[gvk.Kind][version] = true
		allAPIVersions[version] = true
	}

	for gvk, typ := range fabScheme.AllKnownTypes() {
		if !isResourceType(gvk.Kind) {
			continue
		}

		instance := newInstance(typ)
		schema := reflector.Reflect(instance)
		kindSchemas[gvk.Kind] = schema

		if _, ok := kindVersions[gvk.Kind]; !ok {
			kindVersions[gvk.Kind] = make(map[string]bool)
		}

		version := gvk.GroupVersion().String()
		kindVersions[gvk.Kind][version] = true
		allAPIVersions[version] = true
	}

	// Create a deduplicated list of all unique API versions
	uniqueVersions := make([]string, 0, len(allAPIVersions))
	for version := range allAPIVersions {
		uniqueVersions = append(uniqueVersions, version)
	}
	sort.Strings(uniqueVersions)

	baseSchema := map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"$id":         "https://githedgehog.com/schemas/fabric",
		"title":       "GitHedgehog Fabric Schema",
		"description": "Schema for GitHedgehog Fabric and Wiring resources",
		"type":        "object",
		"required":    []string{"apiVersion", "kind", "metadata"},
		"properties": map[string]interface{}{
			"apiVersion": map[string]interface{}{
				"type":        "string",
				"description": "The versioned schema of this resource",
				"enum":        uniqueVersions,
			},
			"kind": map[string]interface{}{
				"type":        "string",
				"description": "The type of the resource",
			},
			"metadata": map[string]interface{}{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Name of the resource",
					},
					"namespace": map[string]interface{}{
						"type":        "string",
						"description": "Namespace of the resource",
					},
				},
				"additionalProperties": false,
			},
			"spec": map[string]interface{}{
				"type":                 "object",
				"description":          "Specification of the resource",
				"additionalProperties": true,
			},
		},
		"additionalProperties": false,
	}

	definitions := make(map[string]interface{})

	for kind, schema := range kindSchemas {
		schemaData, err := json.Marshal(schema)
		if err != nil {
			continue
		}

		var schemaMap map[string]interface{}
		if err := json.Unmarshal(schemaData, &schemaMap); err != nil {
			continue
		}

		delete(schemaMap, "$schema")

		if id, exists := schemaMap["$id"].(string); exists && id != "" {
			schemaMap["$id"] = id
		}

		// Enforce stricter validation on property objects
		enforceStrictValidation(schemaMap)

		definitions[kind] = schemaMap
	}

	baseSchema["definitions"] = definitions

	// Create dependency section to enforce apiVersion and kind combinations
	apiVersionDependency := make(map[string]interface{})

	for kind, versionsMap := range kindVersions {
		kindVersionsList := make([]string, 0, len(versionsMap))
		for version := range versionsMap {
			kindVersionsList = append(kindVersionsList, version)
		}
		sort.Strings(kindVersionsList)

		for _, version := range kindVersionsList {
			if _, ok := apiVersionDependency[version]; !ok {
				apiVersionDependency[version] = map[string]interface{}{
					"properties": map[string]interface{}{
						"kind": map[string]interface{}{
							"enum": []string{kind},
						},
					},
				}
			} else {
				// Add this kind to the existing version's enum
				props := apiVersionDependency[version].(map[string]interface{})["properties"].(map[string]interface{})
				kindEnum := props["kind"].(map[string]interface{})["enum"].([]string)
				kindEnum = append(kindEnum, kind)
				props["kind"].(map[string]interface{})["enum"] = kindEnum
			}
		}
	}

	// Convert the apiVersionDependency map to the format needed for dependencies
	apiVersionConditions := make([]map[string]interface{}, 0, len(apiVersionDependency))
	for version, condition := range apiVersionDependency {
		apiVersionConditions = append(apiVersionConditions, map[string]interface{}{
			"if": map[string]interface{}{
				"properties": map[string]interface{}{
					"apiVersion": map[string]interface{}{
						"enum": []string{version},
					},
				},
			},
			"then": condition,
		})
	}

	baseSchema["allOf"] = apiVersionConditions

	oneOf := make([]map[string]interface{}, 0, len(kindSchemas))

	for kind := range kindSchemas {
		resourceSchema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"kind": map[string]interface{}{
					"enum": []string{kind},
				},
			},
			"required": []string{"apiVersion", "kind", "metadata"},
			"allOf": []map[string]interface{}{
				{
					"$ref": "#/definitions/" + kind,
				},
			},
		}
		oneOf = append(oneOf, resourceSchema)
	}

	baseSchema["oneOf"] = oneOf

	b, err := json.MarshalIndent(baseSchema, "", "\t")
	if err != nil {
		return "", fmt.Errorf("marshaling schema: %w", err)
	}

	return string(b), nil
}

func enforceStrictValidation(schema map[string]interface{}) {
	if schema["type"] == "object" {
		// Set additionalProperties to false for all objects
		schema["additionalProperties"] = false

		// Process properties recursively
		if props, ok := schema["properties"].(map[string]interface{}); ok {
			for _, propSchema := range props {
				if propMap, ok := propSchema.(map[string]interface{}); ok {
					enforceStrictValidation(propMap)
				}
			}
		}

		// Process items for arrays
		if items, ok := schema["items"].(map[string]interface{}); ok {
			enforceStrictValidation(items)
		}
	}
}

func isResourceType(kind string) bool {
	if strings.HasSuffix(kind, "List") ||
		strings.HasSuffix(kind, "Options") ||
		kind == "Status" ||
		kind == "WatchEvent" ||
		kind == "APIGroup" ||
		kind == "APIGroupList" ||
		kind == "APIResourceList" ||
		kind == "APIVersions" {
		return false
	}

	return true
}

func newInstance(t reflect.Type) interface{} {
	if t.Kind() == reflect.Ptr {
		return reflect.New(t.Elem()).Interface()
	}

	return reflect.New(t).Interface()
}

func WriteJSONSchema(workDir string) error {
	schema, err := GenerateJSONSchema()
	if err != nil {
		return fmt.Errorf("generating schema: %w", err)
	}

	schemaPath := filepath.Join(workDir, "hhfab.schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0600); err != nil {
		return fmt.Errorf("writing schema file: %w", err)
	}

	if err := GenerateVSCodeSettings(workDir); err != nil {
		return fmt.Errorf("generating VS Code settings: %w", err)
	}

	slog.Info("Generated JSONSchema and VS Code settings", "schema", "hhfab.schema.json", "settings", ".vscode/settings.json")

	return nil
}

func GenerateVSCodeSettings(workDir string) error {
	settingsDir := filepath.Join(workDir, ".vscode")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		return fmt.Errorf("creating .vscode directory: %w", err)
	}

	settings := map[string]interface{}{
		"yaml.schemas": map[string]interface{}{
			"./hhfab.schema.json": []string{
				"fab.yaml",
				"vlab.generated.yaml",
				"include/*.yaml",
			},
		},
		"editor.insertSpaces": false,
		"editor.tabSize":      2,

		"yaml.completion":                          true,
		"yaml.format.enable":                       true,
		"yaml.validate":                            true,
		"yaml.hover":                               true,
		"yaml.schemaStore.enable":                  true,
		"yaml.suggest.parentSkeletonSelectedFirst": true,
	}

	b, err := json.MarshalIndent(settings, "", "\t")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}

	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, b, 0600); err != nil {
		return fmt.Errorf("writing settings file: %w", err)
	}

	return nil
}
