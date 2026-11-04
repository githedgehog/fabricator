// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"os"
	"strings"

	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
)

// dmiChassisAssetTagPath is where the guest sees the SMBIOS chassis asset tag
// VLAB marks its VMs with.
const dmiChassisAssetTagPath = "/sys/class/dmi/id/chassis_asset_tag"

// isVLAB reports whether this machine is a VLAB VM.
func isVLAB() bool {
	tag, err := os.ReadFile(dmiChassisAssetTagPath)

	return err == nil && strings.TrimSpace(string(tag)) == flatcar.VLABAssetTag
}
