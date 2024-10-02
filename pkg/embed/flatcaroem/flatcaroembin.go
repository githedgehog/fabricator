// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package flatcaroem

import (
	_ "embed"
)

//go:embed oem.cpio.gz
var content []byte

func Bytes() []byte {
	return content
}
