// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package flatcar

import (
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
)

const (
	Ref     = "fabricator/flatcar-update"
	BinName = "flatcar_production_update.gz"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Fabricator.Flatcar
}
