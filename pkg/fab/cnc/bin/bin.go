package bin

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

//go:embed hhfab-recipe
var runBin []byte

const (
	RECIPE_BIN_NAME = "hhfab-recipe"
)

func WriteRunBin(basedir string) error {
	path := filepath.Join(basedir, RECIPE_BIN_NAME)
	return errors.Wrapf(os.WriteFile(path, runBin, 0o755), "error writing bin %s", path)
}
