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
