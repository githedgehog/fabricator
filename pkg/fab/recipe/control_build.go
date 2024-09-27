package recipe

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	butane "github.com/coreos/butane/config"
	butanecommon "github.com/coreos/butane/config/common"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ControlInstallBuilder struct {
	WorkDir    string
	CacheDir   string
	Repo       string
	Prefix     string
	Fab        fabapi.Fabricator
	Control    fabapi.ControlNode
	Wiring     client.Reader
	Downloader *artificer.Downloader
}

const (
	FabName               = "fab.yaml"
	WiringName            = "wiring.yaml"
	InstallSuffix         = "-install"
	InstallArchiveSuffix  = InstallSuffix + ".tgz"
	InstallIgnitionSuffix = InstallSuffix + ".ign"
)

func (b *ControlInstallBuilder) Build(ctx context.Context) error {
	v := b.Fab.Status.Versions

	installDir := filepath.Join(b.WorkDir, b.Control.Name+InstallSuffix)
	installArchive := filepath.Join(b.WorkDir, b.Control.Name+InstallArchiveSuffix)
	installIgnition := filepath.Join(b.WorkDir, b.Control.Name+InstallIgnitionSuffix)

	if err := removeIfExists(installDir); err != nil {
		return fmt.Errorf("removing install dir: %w", err)
	}
	if err := removeIfExists(installArchive); err != nil {
		return fmt.Errorf("removing install archive: %w", err)
	}
	if err := removeIfExists(installIgnition); err != nil {
		return fmt.Errorf("removing install ignition: %w", err)
	}

	if err := os.MkdirAll(installDir, 0o700); err != nil {
		return fmt.Errorf("creating install dir: %w", err)
	}

	if err := b.Downloader.FromORAS(ctx, installDir, k3s.Ref, v.Platform.K3s, []artificer.ORASFile{
		{
			Name: k3s.BinName,
		},
		{
			Name: k3s.InstallName,
		},
		{
			Name: k3s.AirgapName,
		},
	}); err != nil {
		return fmt.Errorf("downloading k3s: %w", err)
	}

	if err := b.Downloader.FromORAS(ctx, installDir, zot.AirgapRef, v.Platform.Zot, []artificer.ORASFile{
		{
			Name: zot.AirgapImageName,
		},
		{
			Name: zot.AirgapChartName,
		},
	}); err != nil {
		return fmt.Errorf("downloading zot: %w", err)
	}

	if err := b.Downloader.FromORAS(ctx, installDir, certmanager.AirgapRef, v.Platform.CertManager, []artificer.ORASFile{
		{
			Name: certmanager.AirgapImageName,
		},
		{
			Name: certmanager.AirgapChartName,
		},
	}); err != nil {
		return fmt.Errorf("downloading cert-manager: %w", err)
	}

	fabF, err := os.Create(filepath.Join(installDir, FabName))
	if err != nil {
		return fmt.Errorf("creating fab file: %w", err)
	}
	defer fabF.Close()

	if err := apiutil.PrintFab(b.Fab, []fabapi.ControlNode{b.Control}, fabF); err != nil {
		return fmt.Errorf("printing fab: %w", err)
	}

	wiringF, err := os.Create(filepath.Join(installDir, WiringName))
	if err != nil {
		return fmt.Errorf("creating wiring file: %w", err)
	}
	defer wiringF.Close()

	if err := apiutil.PrintWiring(ctx, b.Wiring, wiringF); err != nil {
		return fmt.Errorf("printing wiring: %w", err)
	}

	ign, err := controlIgnition(b.Fab, b.Control)
	if err != nil {
		return fmt.Errorf("creating ignition: %w", err)
	}

	if err := os.WriteFile(installIgnition, ign, 0o600); err != nil {
		return fmt.Errorf("writing ignition: %w", err)
	}

	// TODO OCI sync for airgap

	return nil
}

func removeIfExists(path string) error {
	_, err := os.Stat(path)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %q: %w", path, err)
	}

	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing %q: %w", path, err)
	}

	return nil
}

//go:embed control_butane.tmpl.yaml
var controlButaneTmpl string

func controlIgnition(fab fabapi.Fabricator, control fabapi.ControlNode) ([]byte, error) {
	but, err := tmplutil.FromTemplate("butane", controlButaneTmpl, map[string]any{
		"Hostname":       control.Name,
		"PasswordHash":   fab.Spec.Config.Control.DefaultUser.PasswordHash,
		"AuthorizedKeys": fab.Spec.Config.Control.DefaultUser.AuthorizedKeys,
		"MgmtInterface":  control.Spec.Management.Interface,
		"MgmtAddress":    control.Spec.Management.IP,
		"ControlVIP":     fab.Spec.Config.Control.VIP,
		"ExtInterface":   control.Spec.External.Interface,
		"ExtAddress":     control.Spec.External.IP,
		"ExtGateway":     control.Spec.External.Gateway,
		"ExtDNS":         control.Spec.External.DNS,
	})
	if err != nil {
		return nil, fmt.Errorf("butane: %w", err)
	}

	options := butanecommon.TranslateBytesOptions{}
	options.NoResourceAutoCompression = true
	options.Pretty = true

	ign, report, err := butane.TranslateBytes([]byte(but), options)
	if err != nil {
		return nil, fmt.Errorf("translating config: %w", err)
	}
	if len(report.Entries) > 0 {
		slog.Warn("butane", "report", report.String())

		return nil, fmt.Errorf("butane produced warnings and strict mode is enabled") //nolint:goerr113
	}

	return ign, nil
}
