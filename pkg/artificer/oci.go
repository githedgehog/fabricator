// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package artificer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"go.githedgehog.com/fabricator/api/meta"
)

func UploadOCIArchive(ctx context.Context, workDir, name string, version meta.Version, repo, prefix, username, password string) error {
	cacheName := ociCacheName(name, version)
	srcRef := "oci:" + filepath.Join(workDir, cacheName)
	dstRef := "docker://" + strings.Trim(repo, "/") + "/" + strings.Trim(prefix, "/") + "/" + strings.Trim(name, "/") + ":" + string(version)

	if err := copyOCI(ctx, srcRef, dstRef, nil, &types.DockerAuthConfig{Username: username, Password: password}); err != nil {
		return fmt.Errorf("uploading OCI archive %s to %s: %w", srcRef, dstRef, err)
	}

	return nil
}

func InstallOCIArchive(ctx context.Context, workDir, name string, version meta.Version, dstPath, ref string) error {
	cacheName := ociCacheName(name, version)
	srcRef := "oci:" + filepath.Join(workDir, cacheName)
	dstRef := "docker-archive:" + dstPath + ":" + ref

	if err := copyOCI(ctx, srcRef, dstRef, nil, nil); err != nil {
		return fmt.Errorf("installing OCI archive %s to %s: %w", srcRef, dstRef, err)
	}

	return nil
}

func copyOCI(ctx context.Context, src, dst string, srcAuth, dstAuth *types.DockerAuthConfig) error {
	srcRef, err := alltransports.ParseImageName(src)
	if err != nil {
		return fmt.Errorf("parsing source ref %s: %w", src, err)
	}
	destRef, err := alltransports.ParseImageName(dst)
	if err != nil {
		return fmt.Errorf("parsing destination ref %s: %w", dst, err)
	}

	policyCtx, err := signature.NewPolicyContext(&signature.Policy{
		Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
	})
	if err != nil {
		return fmt.Errorf("creating policy context: %w", err)
	}

	progressChan := make(chan types.ProgressProperties)

	pb := mpb.New(mpb.WithWidth(40), mpb.WithOutput(os.Stderr))

	bars := map[string]*mpb.Bar{}
	barStart := map[string]time.Time{}

	go func() {
		for p := range progressChan {
			if !slog.Default().Enabled(ctx, slog.LevelInfo) || p.Artifact.Size < 1_000_000 { // skip progress bae if < 1MB
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
	if srcRef.Transport().Name() == "docker" {
		srcRefName := srcRef.DockerReference().Name()
		if strings.HasPrefix(srcRefName, "127.0.0.1:") || strings.HasPrefix(srcRefName, "localhost:") {
			sourceInsecure = types.OptionalBoolTrue
		}
	}

	_, err = copy.Image(ctx, policyCtx, destRef, srcRef, &copy.Options{
		ProgressInterval:   1 * time.Second,
		Progress:           progressChan,
		ImageListSelection: copy.CopyAllImages,
		SourceCtx: &types.SystemContext{
			DockerInsecureSkipTLSVerify: sourceInsecure,
			DockerAuthConfig:            srcAuth,
		},
		DestinationCtx: &types.SystemContext{
			DockerAuthConfig: dstAuth,
		},
	})
	if err != nil {
		return fmt.Errorf("copying OCI from %s to %s: %w", src, dst, err)
	}

	pb.Wait()

	return nil
}
