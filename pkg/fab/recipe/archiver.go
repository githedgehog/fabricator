// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mholt/archiver/v4"
)

func archiveTarGz(ctx context.Context, src, dst string) error {
	files, err := archiver.FilesFromDisk(nil, map[string]string{
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

	format := archiver.CompressedArchive{
		Compression: archiver.Gz{
			Multithreaded:    true,
			CompressionLevel: gzip.BestSpeed,
		},
		Archival: archiver.Tar{},
	}

	err = format.Archive(ctx, out, files)
	if err != nil {
		return fmt.Errorf("archiving %s: %w", src, err)
	}

	return nil
}
