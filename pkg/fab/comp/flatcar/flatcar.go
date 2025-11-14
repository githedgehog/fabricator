// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package flatcar

import (
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
)

const (
	ToolboxArchiveRef = "fabricator/toolbox"
	ToolboxArchiveBin = "toolbox.tar"
	ToolboxRef        = "toolbox"
	Home              = "/home/core"
	UpdateRef         = "fabricator/flatcar-update"
	UpdateBinName     = "flatcar_production_update.gz"
)

const ToolboxConfig = `
TOOLBOX_DOCKER_IMAGE=ghcr.io/githedgehog/toolbox
TOOLBOX_DOCKER_TAG=latest
TOOLBOX_USER=root
`

func ToolboxVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.Toolbox
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ToolboxRef: ToolboxVersion(cfg),
	}, nil
}

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Fabricator.Flatcar
}
