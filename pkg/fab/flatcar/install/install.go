// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package install

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"
)

func Do(_ context.Context, basedir string, dryRun bool) error {
	slog.Debug("Using", "basedir", basedir, "dryRun", dryRun)

	configFile := filepath.Join(basedir, ConfigFile)
	config := Config{}

	if _, err := os.Stat(configFile); err != nil {
		if os.IsNotExist(err) {
			slog.Warn("Config file not found", "file", configFile)
		} else {
			return errors.Wrapf(err, "error checking config file %s", configFile)
		}
	}

	configData, err := os.ReadFile(configFile)
	if err != nil {
		return errors.Wrapf(err, "error reading config file %s", configFile)
	}

	if err := yaml.Unmarshal(configData, &config); err != nil {
		return errors.Wrapf(err, "error unmarshalling config file %s", configFile)
	}

	slog.Info("Config", "config", config)

	// TODO implement flatcar installer
	// - read config from "basedir" (using sigs.k8s.io/yaml) if file is present
	// - prompt user for missing values

	return nil
}
