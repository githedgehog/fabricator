// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package alloy

import (
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
)

const (
	BinRef   = "fabricator/alloy-bin"
	ImageRef = "fabricator/alloy"
	ChartRef = "fabricator/charts/alloy"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.Alloy
}
