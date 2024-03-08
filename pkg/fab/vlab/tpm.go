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

package vlab

import (
	"context"
	"log/slog"
	"os"

	"github.com/pkg/errors"
)

// Makes sure that swtpm config is initialized before we start VMs
func InitTPMConfig(ctx context.Context, svcCfg *ServiceConfig) error {
	// TODO should we run all tpm commands with XDG_CONFIG_HOME set to the .hhfab/vlab-vms?

	// swtpm_setup pre 0.7.0
	_, err := os.Stat("/usr/share/swtpm/swtpm-create-user-config-files")
	if err == nil {
		err = execCmd(ctx, svcCfg, "", false, "/usr/share/swtpm/swtpm-create-user-config-files", []string{})
		if err != nil {
			// Most probably it's just refusing to overwrite existing config
			slog.Debug("swtpm-create-user-config-files failed, ignoring", "error", err)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return errors.Wrapf(err, "error checking for swtpm-create-user-config-files")
	}

	// swtpm_setup 0.7.0+
	err = execCmd(ctx, svcCfg, "", false, "swtpm_setup", []string{}, "--create-config-files", "skip-if-exist")
	if err != nil {
		return errors.Wrapf(err, "error running swtpm_setup --create-config-files")
	}

	return nil
}
