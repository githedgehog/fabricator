// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipebin

import (
	_ "embed"

	"go.githedgehog.com/fabricator/pkg/embed"
)

//go:embed hhfab-recipe.gz
var compressed []byte

func Bytes() ([]byte, error) {
	return embed.Bytes(compressed) //nolint:wrapcheck
}
