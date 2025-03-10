// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFlatcarVersion(t *testing.T) {
	assert.True(t,
		strings.HasPrefix(
			string(Versions.Fabricator.ControlUSBRoot),
			string(Versions.Fabricator.Flatcar)+"-"),
		"ControlUSBRoot version should be based on the Fabricator Flatcar version")

	assert.Equal(t,
		string(Versions.Fabricator.Flatcar),
		string(Versions.VLAB.Flatcar),
		"VLAB Flatcar version should match Fabricator Flatcar version")
}
