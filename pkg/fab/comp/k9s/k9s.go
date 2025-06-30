// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package k9s

import (
	_ "embed"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
)

const (
	Ref           = "fabricator/k9s"
	BinName       = "k9s"
	HomeConfigDir = "/home/core/.config"
	ConfigDir     = "k9s"
	PluginsFile   = "plugins.yaml"
	ConfigFile    = "config.yaml"
	UserID        = 500 // "core" user
	GroupID       = 500 // "core" group
)

//go:embed k9s_config.yaml
var Config []byte

//go:embed k9s_plugins.yaml
var Plugins []byte

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.K9s
}
