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

package bin

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

//go:embed hhfab-recipe
var recipeBin []byte

//go:embed hhfab-flatcar-install
var flatcarInstallBin []byte

const (
	RecipeBinName         = "hhfab-recipe"
	FlatcarInstallBinName = "hhfab-flatcar-install"
)

func WriteRecipeBin(basedir string) error {
	path := filepath.Join(basedir, RecipeBinName)

	return errors.Wrapf(os.WriteFile(path, recipeBin, 0o755), "error writing bin %s", path) //nolint:gosec
}

func WriteFlatcarInstallBin(basedir string) error {
	path := filepath.Join(basedir, FlatcarInstallBinName)

	return errors.Wrapf(os.WriteFile(path, flatcarInstallBin, 0o755), "error writing bin %s", path) //nolint:gosec
}
