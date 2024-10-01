package artificer

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	"github.com/pkg/errors"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

func copyOCI(ctx context.Context, src, dst string, srcAuth, dstAuth *types.DockerAuthConfig) error {
	srcRef, err := alltransports.ParseImageName(src)
	if err != nil {
		return errors.Wrapf(err, "error parsing source ref %s", src)
	}
	destRef, err := alltransports.ParseImageName(dst)
	if err != nil {
		return errors.Wrapf(err, "error parsing dest ref %s", dst)
	}

	policyCtx, err := signature.NewPolicyContext(&signature.Policy{
		Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
	})
	if err != nil {
		return errors.Wrapf(err, "error creating policy context")
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
	srcRefName := srcRef.DockerReference().Name()
	if strings.HasPrefix(srcRefName, "127.0.0.1:") || strings.HasPrefix(srcRefName, "localhost:") {
		sourceInsecure = types.OptionalBoolTrue
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
		return errors.Wrapf(err, "error copying OCI from %s to %s", src, dst)
	}

	pb.Wait()

	return nil
}
