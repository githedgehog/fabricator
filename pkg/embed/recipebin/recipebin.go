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
