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

package iso

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"go.githedgehog.com/fabricator/pkg/fab"
)

// Build builds the Control Node ISO only, based on the pre-built control-instal bundle, not generic
func Build(_ context.Context, basedir string) error {
	start := time.Now()

	installer := filepath.Join(basedir, fab.BundleControlInstall.Name)
	target := filepath.Join(basedir, "control-node.iso")
	workdir := filepath.Join(basedir, fab.BundleControlISO.Name)

	slog.Info("Building Control Node ISO", "target", target, "workdir", workdir, "installer", installer)

	// TODO implement ISO building
	// - use "workdir" as working directory where all needed files will be downloaded and configs built (see fab/ctrl_os.go)
	// - include all files from "installer" path and later run "hhfab-recipe ..." on that files on first boot
	// - use "target" as final ISO file

	slog.Info("ISO building done", "took", time.Since(start))

	return nil
}
