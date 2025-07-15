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

	versionsData, err := formatVersions(freshFab.Status.Versions, cfg.Fab.Status.Versions)
	if err != nil {
		return fmt.Errorf("formatting versions: %w", err)
	}

	fmt.Println(versionsData)

	return nil
}

func getVersionsFromCluster(ctx context.Context, c *Config) error {
	kubeconfig := filepath.Join(c.WorkDir, VLABDir, VLABKubeConfig)
	kube, err := kubeutil.NewClient(ctx, kubeconfig, fabapi.SchemeBuilder)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}

	fabObj := &fabapi.Fabricator{}
	if err := kube.Get(ctx, kclient.ObjectKey{Name: "default", Namespace: "fab"}, fabObj); err != nil {
		return fmt.Errorf("getting fabricator object: %w", err)
	}

	slog.Info("Printing versions from live cluster")

	freshFab := fabapi.Fabricator{}
	if err := freshFab.CalculateVersions(fab.Versions); err != nil {
		return fmt.Errorf("calculating default versions: %w", err)
	}

	versionsData, err := formatVersions(freshFab.Status.Versions, fabObj.Status.Versions)
	if err != nil {
		return fmt.Errorf("formatting versions: %w", err)
	}

	fmt.Println(versionsData)

	return nil
}

func formatVersions(releaseVersions, overriddenVersions fabapi.Versions) (string, error) {
	releaseMap, err := convertToMap(releaseVersions)
	if err != nil {
		return "", fmt.Errorf("converting release versions to map: %w", err)
	}

	overriddenMap, err := convertToMap(overriddenVersions)
	if err != nil {
		slog.Warn("Failed to convert overridden versions", "error", err)
		data, _ := kyaml.Marshal(releaseVersions)

		return string(data), nil
	}

	slog.Info("Printing versions of all components (release → override)")

	result := compareVersionMaps(releaseMap, overriddenMap)

	resultData, err := kyaml.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshalling result: %w", err)
	}

	return string(resultData), nil
}

func convertToMap(v interface{}) (map[string]interface{}, error) {
	data, err := kyaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshalling: %w", err)
	}

	var result map[string]interface{}
	if err := kyaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshalling: %w", err)
	}

	return result, nil
}

func compareVersionMaps(releases, overridden map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	for key, releaseValue := range releases {
		releaseMap, isMap := releaseValue.(map[string]interface{})
		if isMap {
			overriddenMap, hasOverridden := overridden[key].(map[string]interface{})
			if hasOverridden {
				result[key] = compareVersionMaps(releaseMap, overriddenMap)
			} else {
				result[key] = releaseMap
			}
		} else {
			releaseStr, isString := releaseValue.(string)
			if !isString {
				result[key] = releaseValue

				continue
			}

			overriddenValue, hasOverridden := overridden[key]
			if hasOverridden {
				overriddenStr, isString := overriddenValue.(string)
				if isString && overriddenStr != "" && overriddenStr != releaseStr {
					result[key] = fmt.Sprintf("%s → %s", releaseStr, overriddenStr)
				} else {
					result[key] = releaseValue
				}
			} else {
				result[key] = releaseValue
			}
		}
	}

	return result
}
