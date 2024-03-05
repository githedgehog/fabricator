package cnc

import (
	"hash/fnv"
	"os"
	"path/filepath"

	"github.com/mitchellh/hashstructure/v2"
	"sigs.k8s.io/yaml"
)

const CACHE_FILE = "cache.yaml"

type Cache struct {
	Hashes map[string]uint64 `json:"hashes,omitempty"`
}

func (c *Cache) Save(basedir string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(basedir, CACHE_FILE), data, 0o644)
}

func (c *Cache) Load(basedir string) error {
	data, err := os.ReadFile(filepath.Join(basedir, CACHE_FILE))
	if err != nil {
		return err
	}

	return yaml.UnmarshalStrict(data, c)
}

func (c *Cache) hash(values ...any) (uint64, error) {
	hash, err := hashstructure.Hash(values, hashstructure.FormatV2, &hashstructure.HashOptions{
		Hasher: fnv.New64(),
	})
	if err != nil {
		return 0, err
	}

	return hash, nil
}

func (c *Cache) IsActual(name string, values ...any) (bool, error) {
	hash, err := c.hash(values)
	if err != nil {
		return false, err
	}

	cached, ok := c.Hashes[name]

	return ok && cached == hash, nil
}

func (c *Cache) Add(name string, values ...any) error {
	hash, err := c.hash(values)
	if err != nil {
		return err
	}

	c.Hashes[name] = hash

	return nil
}
