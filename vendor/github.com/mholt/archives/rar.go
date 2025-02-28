package archives

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/nwaples/rardecode/v2"
)

func init() {
	RegisterFormat(Rar{})
}

type Rar struct {
	// If true, errors encountered during reading or writing
	// a file within an archive will be logged and the
	// operation will continue on remaining files.
	ContinueOnError bool

	// Password to open archives.
	Password string
}

func (Rar) Extension() string { return ".rar" }
func (Rar) MediaType() string { return "application/vnd.rar" }

func (r Rar) Match(_ context.Context, filename string, stream io.Reader) (MatchResult, error) {
	var mr MatchResult

	// match filename
	if strings.Contains(strings.ToLower(filename), r.Extension()) {
		mr.ByName = true
	}

	// match file header (there are two versions; allocate buffer for larger one)
	buf, err := readAtMost(stream, len(rarHeaderV5_0))
	if err != nil {
		return mr, err
	}

	matchedV1_5 := len(buf) >= len(rarHeaderV1_5) &&
		bytes.Equal(rarHeaderV1_5, buf[:len(rarHeaderV1_5)])
	matchedV5_0 := len(buf) >= len(rarHeaderV5_0) &&
		bytes.Equal(rarHeaderV5_0, buf[:len(rarHeaderV5_0)])

	mr.ByStream = matchedV1_5 || matchedV5_0

	return mr, nil
}

// Archive is not implemented for RAR because it is patent-encumbered.

func (r Rar) Extract(ctx context.Context, sourceArchive io.Reader, handleFile FileHandler) error {
	var options []rardecode.Option
	if r.Password != "" {
		options = append(options, rardecode.Password(r.Password))
	}

	rr, err := rardecode.NewReader(sourceArchive, options...)
	if err != nil {
		return err
	}

	// important to initialize to non-nil, empty value due to how fileIsIncluded works
	skipDirs := skipList{}

	for {
		if err := ctx.Err(); err != nil {
			return err // honor context cancellation
		}

		hdr, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if r.ContinueOnError {
				log.Printf("[ERROR] Advancing to next file in rar archive: %v", err)
				continue
			}
			return err
		}
		if fileIsIncluded(skipDirs, hdr.Name) {
			continue
		}

		info := rarFileInfo{hdr}
		file := FileInfo{
			FileInfo:      info,
			Header:        hdr,
			NameInArchive: hdr.Name,
			Open: func() (fs.File, error) {
				return fileInArchive{io.NopCloser(rr), info}, nil
			},
		}

		err = handleFile(ctx, file)
		if errors.Is(err, fs.SkipAll) {
			break
		} else if errors.Is(err, fs.SkipDir) && file.IsDir() {
			skipDirs.add(hdr.Name)
		} else if err != nil {
			return fmt.Errorf("handling file: %s: %w", hdr.Name, err)
		}
	}

	return nil
}

// rarFileInfo satisfies the fs.FileInfo interface for RAR entries.
type rarFileInfo struct {
	fh *rardecode.FileHeader
}

func (rfi rarFileInfo) Name() string       { return path.Base(rfi.fh.Name) }
func (rfi rarFileInfo) Size() int64        { return rfi.fh.UnPackedSize }
func (rfi rarFileInfo) Mode() os.FileMode  { return rfi.fh.Mode() }
func (rfi rarFileInfo) ModTime() time.Time { return rfi.fh.ModificationTime }
func (rfi rarFileInfo) IsDir() bool        { return rfi.fh.IsDir }
func (rfi rarFileInfo) Sys() any           { return nil }

var (
	rarHeaderV1_5 = []byte("Rar!\x1a\x07\x00")     // v1.5
	rarHeaderV5_0 = []byte("Rar!\x1a\x07\x01\x00") // v5.0
)

// Interface guard
var _ Extractor = Rar{}
