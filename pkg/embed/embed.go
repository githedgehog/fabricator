package embed

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

func Bytes(compressed []byte) ([]byte, error) {
	r := bytes.NewReader(compressed)
	gzR, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gzR.Close()

	content, err := io.ReadAll(gzR)
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}

	return content, nil
}
