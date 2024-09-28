package vlab

import (
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
)

const (
	FlatcarRef = "fabricator/flatcar-vlab"
	ONIERef    = "fabricator/onie-vlab"
)

func FlatcarVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.VLAB.Flatcar
}

func ONIEVersion(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.VLAB.ONIE
}
