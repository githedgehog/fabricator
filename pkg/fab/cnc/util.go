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

package cnc

import (
	"os"
	"path/filepath"
)

func ReadOrGenerateSSHKey(basedir string, name string, comment string) (string, error) {
	path := filepath.Join(basedir, name)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		err := (&ExecCommand{
			Name: "ssh-keygen",
			Args: []string{
				"-t", "ed25519", "-C", comment, "-f", name, "-N", "",
			},
		}).Run(basedir)
		if err != nil {
			return "", err
		}
	}

	data, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", err
	}

	return string(data), nil
}
