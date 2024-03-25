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
	"hash/fnv"
	"os"
	"path/filepath"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"
)

const CacheFile = "cache.yaml"

type Cache struct {
	Hashes map[string]uint64 `json:"hashes,omitempty"`
}

func (c *Cache) Save(basedir string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return errors.Wrapf(err, "error marshalling cache")
	}

	return errors.Wrapf(os.WriteFile(filepath.Join(basedir, CacheFile), data, 0o600), "error writing cache")
}

func (c *Cache) Load(basedir string) error {
	data, err := os.ReadFile(filepath.Join(basedir, CacheFile))
	if err != nil {
		return errors.Wrapf(err, "error reading cache")
	}

	return errors.Wrapf(yaml.UnmarshalStrict(data, c), "error unmarshalling cache")
}

func (c *Cache) hash(values ...any) (uint64, error) {
	hash, err := hashstructure.Hash(values, hashstructure.FormatV2, &hashstructure.HashOptions{
		Hasher: fnv.New64(),
	})
	if err != nil {
		return 0, errors.Wrapf(err, "error hashing values")
	}

	return hash, nil
}

func (c *Cache) IsActual(name string, values ...any) (bool, error) {
	hash, err := c.hash(values...)
	if err != nil {
		return false, err
	}

	cached, ok := c.Hashes[name]

	return ok && cached == hash, nil
}

func (c *Cache) Add(name string, values ...any) error {
	hash, err := c.hash(values...)
	if err != nil {
		return err
	}

	c.Hashes[name] = hash

	return nil
}
