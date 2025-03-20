// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1

const (
	ns = "fabricator.githedgehog.com"

	RoleLabelValue = "true"
	RoleTaintValue = RoleLabelValue
)

func RoleLabelKey(role FabNodeRole) string {
	return "role." + ns + "/" + string(role)
}

func RoleTaintKey(role FabNodeRole) string {
	return RoleLabelKey(role)
}
