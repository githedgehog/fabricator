package flatcaroem

import (
	_ "embed"
)

//go:embed oem.cpio.gz
var content []byte

func Bytes() []byte {
	return content
}
