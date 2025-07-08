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

	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

func Versions(ctx context.Context, workDir, cacheDir string, hMode HydrateMode, live bool) error {
	if live {
		cfg := &Config{
			WorkDir:  workDir,
			CacheDir: cacheDir,
		}

		return getVersionsFromCluster(ctx, cfg)
	}

	configPath := filepath.Join(workDir, FabConfigFile)
	if _, err := os.Stat(configPath); err != nil && errors.Is(err, os.ErrNotExist) {
		slog.Info("No configuration found", "file", FabConfigFile, "action", "Showing release versions")
		freshFab := fabapi.Fabricator{}
		if err := freshFab.CalculateVersions(fab.Versions); err != nil {
			return fmt.Errorf("calculating default versions: %w", err)
		}
		data, err := kyaml.Marshal(freshFab.Status.Versions)
		if err != nil {
			return fmt.Errorf("marshalling versions: %w", err)
		}
		fmt.Println(string(data))

		return nil
	}

	cfg, err := load(ctx, workDir, cacheDir, true, hMode, "")
	if err != nil {
		return err
	}

	freshFab := fabapi.Fabricator{}
	if err := freshFab.CalculateVersions(fab.Versions); err != nil {
		return fmt.Errorf("calculating default versions: %w", err)
	}
	releaseData, err := kyaml.Marshal(freshFab.Status.Versions)
	if err != nil {
		return fmt.Errorf("marshalling release versions: %w", err)
	}
	var release map[string]interface{}
	if err := kyaml.Unmarshal(releaseData, &release); err != nil {
		return fmt.Errorf("unmarshalling release versions: %w", err)
	}

	overridesRaw, err := kyaml.Marshal(cfg.Fab.Spec.Overrides.Versions)
	if err != nil {
		slog.Warn("Failed to marshal overrides", "error", err)
		fmt.Println(string(releaseData))

		return nil
	}

	var overrides map[string]interface{}
	if err := kyaml.Unmarshal(overridesRaw, &overrides); err != nil {
		slog.Warn("Failed to unmarshal overrides", "error", err)
		fmt.Println(string(releaseData))

		return nil
	}

	if len(overrides) == 0 {
		slog.Info("Printing versions of all components")
		fmt.Println(string(releaseData))

		return nil
	}

	slog.Info("Printing versions of all components (release → override)")
	merged := make(map[string]interface{})

	for category, value := range release {
		merged[category] = processVersionCategory(category, value, overrides)
	}

	mergedData, err := kyaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshalling merged versions: %w", err)
	}
	fmt.Println(string(mergedData))

	return nil
}

func getVersionsFromCluster(ctx context.Context, c *Config) error {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	kube, err := kubeutil.NewClient(ctx, kubeconfig, fabapi.SchemeBuilder)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}

	fab := &fabapi.Fabricator{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: "default", Namespace: "fab"}, fab); err != nil {
		return fmt.Errorf("getting fabricator object: %w", err)
	}

	slog.Info("Printing versions from live cluster")

	releaseData, err := kyaml.Marshal(fab.Status.Versions)
	if err != nil {
		return fmt.Errorf("marshalling versions: %w", err)
	}

	overridesRaw, err := kyaml.Marshal(fab.Spec.Overrides.Versions)
	if err != nil {
		slog.Warn("Failed to marshal overrides", "error", err)
		fmt.Println(string(releaseData))

		return nil
	}

	var overrides map[string]interface{}
	if err := kyaml.Unmarshal(overridesRaw, &overrides); err != nil {
		slog.Warn("Failed to unmarshal overrides", "error", err)
		fmt.Println(string(releaseData))

		return nil
	}

	if len(overrides) == 0 {
		slog.Info("Printing versions of all components")
		fmt.Println(string(releaseData))

		return nil
	}

	var release map[string]interface{}
	if err := kyaml.Unmarshal(releaseData, &release); err != nil {
		slog.Warn("Failed to unmarshal release versions", "error", err)
		fmt.Println(string(releaseData))

		return nil
	}

	slog.Info("Printing versions of all components (release → override)")
	merged := make(map[string]interface{})

	for category, value := range release {
		merged[category] = processVersionCategory(category, value, overrides)
	}

	mergedData, err := kyaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshalling merged versions: %w", err)
	}
	fmt.Println(string(mergedData))

	return nil
}

func processVersionCategory(category string, releaseValue interface{}, overrides map[string]interface{}) interface{} {
	releaseCat, isMapRelease := releaseValue.(map[string]interface{})
	if !isMapRelease {
		return releaseValue
	}

	result := make(map[string]interface{})

	overrideCat, overrideExists := overrides[category]
	if !overrideExists {
		return releaseCat
	}

	overrideMap, isMapOverride := overrideCat.(map[string]interface{})
	if !isMapOverride {
		return releaseCat
	}

	for compName, releaseVer := range releaseCat {
		releaseVerStr, isString := releaseVer.(string)
		if !isString {
			nestedResult := processVersionCategory(compName, releaseVer, overrideMap)
			result[compName] = nestedResult

			continue
		}

		if overrideComp, exists := overrideMap[compName]; exists {
			overrideVerStr, isOverrideString := overrideComp.(string)
			if isOverrideString {
				result[compName] = fmt.Sprintf("%s → %s", releaseVerStr, overrideVerStr)
			} else {
				result[compName] = releaseVer
			}
		} else {
			result[compName] = releaseVer
		}
	}

	return result
}
