// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

var ErrInvalidPrefix = fmt.Errorf("must start with 'v'")

// +kubebuilder:validation:Type=string
type Version string

func (v Version) Parse() (*semver.Version, error) {
	if !strings.HasPrefix(string(v), "v") {
		return nil, fmt.Errorf("parsing version %q: %w", v, ErrInvalidPrefix)
	}

	ver, err := semver.NewVersion(string(v))
	if err != nil {
		return nil, fmt.Errorf("parsing version %q: %w", v, err)
	}

	return ver, nil
}
