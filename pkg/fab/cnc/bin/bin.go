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
var runBin []byte

const (
	RECIPE_BIN_NAME = "hhfab-recipe"
)

func WriteRunBin(basedir string) error {
	path := filepath.Join(basedir, RECIPE_BIN_NAME)
	return errors.Wrapf(os.WriteFile(path, runBin, 0o755), "error writing bin %s", path)
}
