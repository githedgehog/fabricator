// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mholt/archives"
)

func archiveTarGz(ctx context.Context, src, dst string) error {
	files, err := archives.FilesFromDisk(ctx, nil, map[string]string{
		src: filepath.Base(src),
	})
	if err != nil {
		return fmt.Errorf("getting files for bundle %s: %w", src, err)
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating target %q: %w", dst, err)
	}
	defer out.Close()

	format := archives.CompressedArchive{
		Compression: archives.Gz{
			Multithreaded:    true,
			CompressionLevel: gzip.BestSpeed,
		},
		Archival: archives.Tar{},
	}

	err = format.Archive(ctx, out, files)
	if err != nil {
		return fmt.Errorf("archiving %s: %w", src, err)
	}

	return nil
}
