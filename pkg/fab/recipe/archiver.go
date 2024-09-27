package recipe

import (
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/mholt/archiver/v4"
)

func archiveTarGz(ctx context.Context, src, dst string) error {
	slog.Debug("Archiving", "name", src, "target", dst)

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
