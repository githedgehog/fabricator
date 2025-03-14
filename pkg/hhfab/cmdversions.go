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

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"sigs.k8s.io/yaml"
)

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

	cfg, err := load(ctx, workDir, cacheDir, true, hMode, "")
	if err != nil {
		return err
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

	overridesRaw, err := yaml.Marshal(cfg.Fab.Spec.Overrides.Versions)
	if err != nil {
		slog.Warn("Failed to marshal overrides", "error", err)
		fmt.Println(string(releaseData))

		return nil
	}

	var overrides map[string]interface{}
	if err := yaml.Unmarshal(overridesRaw, &overrides); err != nil {
		slog.Warn("Failed to unmarshal overrides", "error", err)
		fmt.Println(string(releaseData))

		return nil
	}

	if len(overrides) == 0 {
		slog.Info("Printing versions of all components")
		fmt.Println(string(releaseData))

		return nil
	}

	slog.Info("Printing versions of all components (overridden←→release)")
	merged := make(map[string]interface{})

	for category, value := range release {
		merged[category] = processVersionCategory(category, value, overrides)
	}

	mergedData, err := yaml.Marshal(merged)
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
				result[compName] = fmt.Sprintf("%s←→%s", overrideVerStr, releaseVerStr)
			} else {
				result[compName] = releaseVer
			}
		} else {
			result[compName] = releaseVer
		}
	}

	return result
}
