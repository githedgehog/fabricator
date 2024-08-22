// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cnc

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/ulikunitz/xz"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

//
// Helper File
//

type File struct {
	Name             string
	Mode             os.FileMode // local mode to set
	InstallTarget    string
	InstallName      string
	InstallMode      os.FileMode
	InstallMkdirMode os.FileMode
}

//
// BuildOp FilesORAS
//

type FilesORAS struct {
	Ref    Ref
	Unpack []string
	Files  []File
}

var _ BuildOp = (*FilesORAS)(nil)

func (op *FilesORAS) Hydrate() error {
	err := op.Ref.StrictValidate()
	if err != nil {
		return errors.Wrap(err, "error validating ref")
	}

	if len(op.Files) == 0 {
		return errors.New("no files specified")
	}

	for _, f := range op.Files {
		if f.Name == "" {
			return errors.New("file name is empty")
		}
	}

	return nil
}

func (op *FilesORAS) Build(basedir string) error {
	skip := true

	for _, f := range op.Files {
		fPath := filepath.Join(basedir, f.Name)
		info, err := os.Stat(fPath)
		if os.IsNotExist(err) {
			skip = false
			slog.Debug("File is missing", "path", fPath)
		} else if err != nil {
			return errors.Wrapf(err, "error statting file %s", fPath)
		} else if info.IsDir() {
			return errors.Errorf("%s is dir, file expected", fPath)
		} else {
			slog.Debug("File is present", "path", fPath)
		}
	}

	if skip {
		slog.Debug("Downloading SKIPPED (files exists)", "name", op.Ref.Name)

		return nil
	}

	slog.Info("Downloading", "name", op.Ref, "to", basedir)

	fs, err := file.New(basedir)
	if err != nil {
		return errors.Wrapf(err, "error creating oras file store in %s", basedir)
	}
	defer fs.Close()

	repo, err := remote.NewRepository(op.Ref.Repo + "/" + op.Ref.Name)
	if err != nil {
		return errors.Wrapf(err, "error creating oras remote repo %s", op.Ref.Repo+"/"+op.Ref.Name)
	}

	if op.Ref.IsLocalhost() {
		repo.PlainHTTP = true
	}

	// Get credentials from the docker credential store
	storeOpts := credentials.StoreOptions{}
	credStore, err := credentials.NewStoreFromDocker(storeOpts)
	if err != nil {
		return errors.Wrapf(err, "error creating docker credential store")
	}

	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.DefaultCache,
		Credential: credentials.Credential(credStore),
	}

	pb := mpb.New(mpb.WithWidth(5))
	bars := sync.Map{}

	complete := func(_ context.Context, desc ocispec.Descriptor) error {
		if v, ok := bars.Load(desc.Digest.String()); ok {
			v.(*mpb.Bar).SetCurrent(desc.Size)
		}

		return nil
	}

	_, err = oras.Copy(context.Background(), repo, op.Ref.Tag, fs, op.Ref.Tag, oras.CopyOptions{
		CopyGraphOptions: oras.CopyGraphOptions{
			Concurrency: 3,
			PreCopy: func(ctx context.Context, desc ocispec.Descriptor) error {
				if !slog.Default().Enabled(ctx, slog.LevelInfo) || desc.Size < 1_000_000 { // skip progress bar if < 1MB
					return nil
				}

				name := "blob " + desc.Digest.Encoded()[:12]
				if desc.Annotations != nil {
					name = desc.Annotations["org.opencontainers.image.title"]
				}
				name = "Copying " + name

				bars.Store(desc.Digest.String(), pb.AddSpinner(desc.Size,
					mpb.PrependDecorators(
						decor.Name(name, decor.WCSyncSpaceR),
						decor.Counters(decor.SizeB1024(0), "% .2f / % .2f", decor.WCSyncSpace),
					),
					mpb.AppendDecorators(
						decor.EwmaSpeed(decor.SizeB1024(0), "% .2f", 30),
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
		return errors.Wrapf(err, "error copying files from %s", op.Ref.String())
	}

	pb.Wait()

	for _, f := range op.Unpack {
		err := UnpackFile(basedir, f)
		if err != nil {
			return errors.Wrap(err, "error unpacking file")
		}
	}

	for _, f := range op.Files {
		if f.Mode != 0 {
			err := os.Chmod(filepath.Join(basedir, f.Name), f.Mode)
			if err != nil {
				return errors.Wrap(err, "error setting file mode")
			}
		}
	}

	return nil
}

func UnpackFile(basedir string, name string) error { // TODO validate we've got files we've been looking for?
	fromPath := filepath.Join(basedir, name)
	from, err := os.Open(fromPath)
	if err != nil {
		return errors.Wrapf(err, "error opening file %s", fromPath)
	}
	defer from.Close()

	toPath := filepath.Join(basedir, strings.TrimSuffix(name, filepath.Ext(name)))
	to, err := os.Create(toPath)
	if err != nil {
		return errors.Wrapf(err, "error creating file %s", toPath)
	}
	defer to.Close()

	slog.Info("Unpacking", "from", fromPath)

	var reader io.Reader = bufio.NewReader(from)

	if filepath.Ext(name) == ".xz" {
		reader, err = xz.NewReader(reader)
	} else if filepath.Ext(name) == ".gz" {
		reader, err = gzip.NewReader(reader)
	} else if filepath.Ext(name) == ".bz2" {
		reader = bzip2.NewReader(reader)
	} else {
		return errors.New("unknown extension to unpack")
	}
	if err != nil {
		return errors.Wrapf(err, "error creating unpack reader for %s", fromPath)
	}

	writer := bufio.NewWriter(to)
	defer writer.Flush()

	p := mpb.New(
		mpb.WithWidth(60),
	)

	info, err := from.Stat()
	if err != nil {
		return errors.Wrapf(err, "error statting file %s", fromPath)
	}

	var bar *mpb.Bar
	if slog.Default().Enabled(context.Background(), slog.LevelInfo) && info.Size() > 10_000_000 {
		bar = p.AddBar(info.Size(),
			mpb.PrependDecorators(
				decor.Counters(decor.SizeB1024(0), "% .2f / % .2f", decor.WCSyncSpace),
			),
			mpb.AppendDecorators(
				decor.EwmaSpeed(decor.SizeB1024(0), "% .2f", 30),
				decor.OnComplete(
					decor.EwmaETA(decor.ET_STYLE_GO, 30, decor.WCSyncSpace), "done",
				),
			),
		)

		proxy := bar.ProxyReader(reader)
		defer proxy.Close()
		reader = proxy
	}

	_, err = io.Copy(writer, reader) //nolint:gosec
	if err != nil {
		return errors.Wrap(err, "error copying while unpacking")
	}

	if bar != nil {
		bar.SetCurrent(info.Size())
	}

	p.Wait()

	err = os.Remove(fromPath)
	if err != nil {
		return errors.Wrapf(err, "error removing file %s", fromPath)
	}

	return nil
}

func (op *FilesORAS) RunOps() []RunOp {
	ops := []RunOp{}

	for _, f := range op.Files {
		if f.InstallTarget != "" {
			ops = append(ops, &InstallFile{
				Name:       f.Name,
				Target:     f.InstallTarget,
				TargetName: f.InstallName,
				Mode:       f.InstallMode,
				MkdirMode:  f.InstallMkdirMode,
			})
		}
	}

	return ops
}

//
// BuildOp FileGenerate
//

type FileGenerate struct {
	File    File
	Content ContentGenerator
}

var _ BuildOp = (*FileGenerate)(nil)

func (op *FileGenerate) Hydrate() error {
	if op.File.Name == "" {
		return errors.New("file name is empty")
	}

	// TODO if op.File.Target == "" warn that it's not going to be installed

	return nil
}

func (op *FileGenerate) Build(basedir string) error {
	content, err := op.Content()
	if err != nil {
		return err
	}

	target, err := os.OpenFile(filepath.Join(basedir, op.File.Name), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return errors.Wrapf(err, "error opening file %s", op.File.Name)
	}
	defer target.Close()

	_, err = target.WriteString(content)
	if err != nil {
		return errors.Wrapf(err, "error writing to file %s", op.File.Name)
	}

	return nil
}

func (op *FileGenerate) RunOps() []RunOp {
	if op.File.InstallTarget != "" {
		return []RunOp{
			&InstallFile{
				Name:       op.File.Name,
				Target:     op.File.InstallTarget,
				TargetName: op.File.InstallName,
				Mode:       op.File.InstallMode,
				MkdirMode:  op.File.InstallMkdirMode,
			},
		}
	}

	return nil
}

//
// BuildOp SyncOCI
//

type SyncOCI struct {
	Ref    Ref
	Target Ref
}

var _ BuildOp = (*SyncOCI)(nil)

func (op *SyncOCI) Hydrate() error {
	err := op.Ref.StrictValidate()
	if err != nil {
		return errors.Wrap(err, "error validating ref")
	}

	if op.Target.Name == "" {
		op.Target.Name = op.Ref.Name // It's ok to inherit name
	}
	if op.Target.Tag == "" {
		op.Target.Tag = op.Ref.Tag // It's ok to inherit tag
	}

	err = op.Target.StrictValidate()
	if err != nil {
		return errors.Wrap(err, "error validating target")
	}

	return nil
}

func (op *SyncOCI) filePath() string {
	return strings.ReplaceAll(fmt.Sprintf("%s@%s", op.Ref.Name, op.Ref.Tag), "/", "_") + ".oci"
}

func (op *SyncOCI) Build(basedir string) error {
	path := filepath.Join(basedir, op.filePath())

	skip := true

	info, err := os.Stat(filepath.Join(path, "index.json"))
	if os.IsNotExist(err) {
		skip = false
		slog.Debug("File is missing", "name", path)
	} else if err != nil {
		return errors.Wrapf(err, "error statting file %s", path)
	} else if info.IsDir() {
		return errors.Errorf("file expected but dir found %s", path)
	} else {
		slog.Debug("File is present", "name", path)
	}

	if skip {
		slog.Debug("Downloading SKIPPED", "name", op.Ref.Name)
	} else {
		slog.Info("Downloading", "ref", op.Ref, "to", path)

		err = copyOCI("docker://"+op.Ref.String(), "oci:"+path, op.Ref.IsLocalhost())
		if err != nil {
			return err
		}
	}

	return nil
}

func (op *SyncOCI) RunOps() []RunOp {
	return []RunOp{
		&PushOCI{
			Name:   op.filePath(),
			Target: op.Target,
		},
	}
}

func copyOCI(from, to string, insecureSource bool) error {
	srcRef, err := alltransports.ParseImageName(from)
	if err != nil {
		return errors.Wrapf(err, "error parsing source ref %s", from)
	}
	destRef, err := alltransports.ParseImageName(to)
	if err != nil {
		return errors.Wrapf(err, "error parsing dest ref %s", to)
	}

	policyCtx, err := signature.NewPolicyContext(&signature.Policy{
		Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
	})
	if err != nil {
		return errors.Wrapf(err, "error creating policy context")
	}

	progressChan := make(chan types.ProgressProperties)

	pb := mpb.New(mpb.WithWidth(64))
	bars := map[string]*mpb.Bar{}
	barStart := map[string]time.Time{}
	go func() {
		for p := range progressChan {
			if !slog.Default().Enabled(context.Background(), slog.LevelInfo) || p.Artifact.Size < 1_000_000 { // skip progress bae if < 1MB
				continue
			}

			name := "blob " + p.Artifact.Digest.Encoded()[:12]
			// It doesn't really makes sense to print file name, because it's only available for ORAS sync
			if title, exist := p.Artifact.Annotations["org.opencontainers.image.title"]; exist {
				name = title
			}
			name = "Copying " + name

			digest := p.Artifact.Digest.String()
			if p.Event == types.ProgressEventNewArtifact {
				bars[digest] = pb.AddBar(p.Artifact.Size, mpb.PrependDecorators(
					decor.Name(name, decor.WCSyncSpaceR),
					decor.Counters(decor.SizeB1024(0), "% .2f / % .2f", decor.WCSyncSpace),
				),
					mpb.AppendDecorators(
						decor.EwmaSpeed(decor.SizeB1024(0), "% .2f", 30),
						decor.OnComplete(
							decor.EwmaETA(decor.ET_STYLE_GO, 30, decor.WCSyncSpace), "done",
						),
					))
			} else if p.Event == types.ProgressEventSkipped { //nolint:revive
				// bars[digest].SetCurrent(p.Artifact.Size)
			} else {
				bars[digest].EwmaIncrInt64(int64(p.OffsetUpdate), time.Since(barStart[digest])) //nolint:gosec
			}

			barStart[digest] = time.Now()
		}
	}()

	var sourceInsecure types.OptionalBool
	if insecureSource {
		sourceInsecure = types.OptionalBoolTrue
	}

	_, err = copy.Image(context.Background(), policyCtx, destRef, srcRef, &copy.Options{
		ProgressInterval:   1 * time.Second,
		Progress:           progressChan,
		ImageListSelection: copy.CopyAllImages,
		SourceCtx: &types.SystemContext{
			DockerInsecureSkipTLSVerify: sourceInsecure,
			DockerAuthConfig:            getDockerAuthConfigOrNil(srcRef),
		},
		DestinationCtx: &types.SystemContext{
			DockerAuthConfig: getDockerAuthConfigOrNil(destRef),
		},
	})
	if err != nil {
		return errors.Wrapf(err, "error copying image from %s to %s", from, to)
	}

	pb.Wait()

	return nil
}

func getDockerAuthConfigOrNil(ref types.ImageReference) *types.DockerAuthConfig {
	if ref.Transport().Name() == "docker" {
		storeOpts := credentials.StoreOptions{}
		credStore, err := credentials.NewStoreFromDocker(storeOpts)
		if err != nil {
			slog.Warn("Error getting docker credentials store", "err", err)

			return nil
		}

		baseRepo := strings.SplitN(ref.DockerReference().String(), "/", 2)[0]
		creds, err := credStore.Get(context.Background(), baseRepo)
		if err != nil {
			slog.Warn("Error getting docker credentials", "repo", baseRepo, "err", err)

			return nil
		}

		if creds.Username != "" && creds.Password != "" {
			return &types.DockerAuthConfig{
				Username: creds.Username,
				Password: creds.Password,
			}
		}
	}

	return nil
}
