package artificer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"go.githedgehog.com/fabricator/api/meta"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	Version = "v1"
)

type Downloader struct {
	cacheDir   string
	repo       string
	prefix     string
	orasClient *auth.Client
	m          sync.Mutex
}

func NewDownloaderWithDockerCreds(cacheDir, repo, prefix string) (*Downloader, error) {
	cacheDir = filepath.Join(cacheDir, Version)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating cache dir %q: %w", cacheDir, err)
	}

	storeOpts := credentials.StoreOptions{}
	credStore, err := credentials.NewStoreFromDocker(storeOpts)
	if err != nil {
		return nil, fmt.Errorf("creating docker credential store: %w", err)
	}

	return &Downloader{
		cacheDir: cacheDir,
		repo:     repo,
		prefix:   prefix,
		orasClient: &auth.Client{
			Client:     retry.DefaultClient,
			Cache:      auth.DefaultCache,
			Credential: credentials.Credential(credStore),
		},
	}, nil
}

type ORASFile struct {
	Name   string
	Target string
	Mode   os.FileMode
}

func (d *Downloader) FromORAS(ctx context.Context, destPath, name string, version meta.Version, files []ORASFile) error {
	if len(files) == 0 {
		return fmt.Errorf("no files to download") //nolint:goerr113
	}

	return d.WithORAS(ctx, name, version, func(cachePath string) error {
		for _, file := range files {
			target := file.Target
			if target == "" {
				target = file.Name
			}

			src := filepath.Join(cachePath, file.Name)
			dst := filepath.Join(destPath, target)
			if err := copyFileOrDir(src, dst); err != nil {
				return err
			}

			if file.Mode != 0 {
				if err := os.Chmod(dst, file.Mode); err != nil {
					return fmt.Errorf("chmod %q: %w", dst, err)
				}
			}
		}

		return nil
	})
}

func (d *Downloader) WithORAS(ctx context.Context, name string, version meta.Version, do func(cachePath string) error) error {
	cachePath, err := d.getORAS(ctx, name, version)
	if err != nil {
		return fmt.Errorf("getting oras: %w", err)
	}

	if err := do(cachePath); err != nil {
		return fmt.Errorf("running func: %w", err)
	}

	return nil
}

func (d *Downloader) getORAS(ctx context.Context, name string, version meta.Version) (string, error) {
	d.m.Lock()
	defer d.m.Unlock()

	cacheName := name + "@" + string(version)
	cacheName = strings.ReplaceAll(cacheName, "/", "_")
	cachePath := filepath.Join(d.cacheDir, cacheName)

	stat, err := os.Stat(cachePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat cache %q: %w", cachePath, err)
	}
	if err == nil && !stat.IsDir() {
		return "", fmt.Errorf("cache %q is not a directory", cachePath) //nolint:goerr113
	}

	if err != nil && errors.Is(err, os.ErrNotExist) {
		tmp, err := os.MkdirTemp(d.cacheDir, "download-*")
		if err != nil {
			return "", fmt.Errorf("creating temp dir: %w", err)
		}
		defer os.RemoveAll(tmp)

		fs, err := file.New(tmp)
		if err != nil {
			return "", fmt.Errorf("creating oras file store in %q: %w", tmp, err)
		}
		defer fs.Close()

		ref := strings.Trim(d.repo, "/") + "/" + strings.Trim(d.prefix, "/") + "/" + strings.Trim(name, "/")

		repo, err := remote.NewRepository(ref)
		if err != nil {
			return "", fmt.Errorf("creating oras remote repo %s: %w", ref, err)
		}

		if strings.HasPrefix(d.repo, "127.0.0.1:") || strings.HasPrefix(d.repo, "localhost:") {
			repo.PlainHTTP = true
		}

		repo.Client = d.orasClient

		slog.Info("Downloading", "name", name, "version", version)

		pb := mpb.New(mpb.WithWidth(5), mpb.WithOutput(os.Stderr))
		bars := sync.Map{}

		complete := func(_ context.Context, desc ocispec.Descriptor) error {
			if v, ok := bars.Load(desc.Digest.String()); ok {
				v.(*mpb.Bar).SetCurrent(desc.Size)
			}

			return nil
		}

		_, err = oras.Copy(ctx, repo, string(version), fs, string(version), oras.CopyOptions{
			CopyGraphOptions: oras.CopyGraphOptions{
				Concurrency: 4,
				PreCopy: func(ctx context.Context, desc ocispec.Descriptor) error {
					if !slog.Default().Enabled(ctx, slog.LevelInfo) {
						return nil
					}

					name := ""
					if desc.Annotations != nil {
						name = desc.Annotations["org.opencontainers.image.title"]
					}
					if name == "" {
						return nil
					}

					name = "Downloading " + name

					bars.Store(desc.Digest.String(), pb.AddSpinner(desc.Size,
						mpb.PrependDecorators(
							decor.Name(name, decor.WCSyncSpaceR),
							decor.Counters(decor.SizeB1024(0), "% .2f / % .2f", decor.WCSyncSpace),
						),
						mpb.AppendDecorators(
							// decor.EwmaSpeed(decor.SizeB1024(0), "% .2f", 30),
							decor.OnComplete(
								decor.EwmaETA(decor.ET_STYLE_GO, 30, decor.WCSyncSpace), "done",
							),
						)))

					return nil
				},
				PostCopy:      complete,
				OnCopySkipped: complete,
			},
		})
		if err != nil {
			return "", errors.Wrapf(err, "error copying files from %s", ref)
		}

		pb.Wait()

		if err := os.Rename(tmp, cachePath); err != nil {
			return "", fmt.Errorf("moving %q to %q: %w", tmp, cachePath, err)
		}
	}

	return cachePath, nil
}

func copyFileOrDir(src, dst string) error {
	stat, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source %q: %w", src, err)
	}

	if stat.IsDir() {
		return CopyDir(src, dst)
	}

	return CopyFile(src, dst)
}

func CopyFile(src, dst string) error {
	srcF, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %q: %w", src, err)
	}
	defer srcF.Close()

	dstF, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %q: %w", dst, err)
	}
	defer dstF.Close()

	if _, err := io.Copy(dstF, srcF); err != nil {
		return fmt.Errorf("copying file %q to %q: %w", src, dst, err)
	}

	return nil
}

func CopyDir(src, dst string) error {
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		return fmt.Errorf("copying dir %q to %q: %w", src, dst, err)
	}

	return nil
}
