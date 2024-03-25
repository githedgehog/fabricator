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
	"os"

	"github.com/pkg/errors"
)

type fileMarker struct {
	path string
}

func (m fileMarker) Is() bool {
	_, err := os.Stat(m.path)
	if os.IsNotExist(err) {
		return false
	}

	return err == nil
}

func (m fileMarker) Mark() error {
	f, err := os.Create(m.path)
	if err != nil {
		return errors.Wrapf(err, "failed to create marker file %s", m.path)
	}
	defer f.Close()

	return nil
}
