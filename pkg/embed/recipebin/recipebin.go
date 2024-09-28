package recipebin

import (
	_ "embed"
)

//go:embed hhfab-recipe
var data []byte

func Bytes() []byte {
	return data
}
