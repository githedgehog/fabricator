package cnc

import (
	"os"
	"path/filepath"
)

func ReadOrGenerateSSHKey(basedir string, name string, comment string) (string, error) {
	path := filepath.Join(basedir, name)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		err := (&ExecCommand{
			Name: "ssh-keygen",
			Args: []string{
				"-t", "ed25519", "-C", comment, "-f", name, "-N", "",
			},
		}).Run(basedir)
		if err != nil {
			return "", err
		}
	}

	data, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", err
	}

	return string(data), nil
}
