// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package k9s

import (
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
)

const (
	Ref     = "fabricator/k9s"
	BinName = "k9s"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.K9s
}
