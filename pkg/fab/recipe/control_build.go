// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/f8r"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/gateway"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k9s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/ntp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/reloader"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/butaneutil"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"go.githedgehog.com/fabricator/pkg/version"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type ControlInstallBuilder struct {
	WorkDir    string
	Fab        fabapi.Fabricator
	Control    fabapi.ControlNode
	Nodes      []fabapi.FabNode
	Wiring     kclient.Reader
	Mode       BuildMode
	Downloader *artificer.Downloader
}

const (
	FabName    = "fab.yaml"
	WiringName = "wiring.yaml"
)

var AirgapArtifactLists = []comp.ListOCIArtifacts{
	flatcar.Artifacts,
	certmanager.Artifacts,
	zot.Artifacts,
	reloader.Artifacts,
	fabric.Artifacts,
	ntp.Artifacts,
	f8r.Artifacts,
	gateway.Artifacts,
}

func (b *ControlInstallBuilder) Build(ctx context.Context) error {
	hash, err := b.hash(ctx)
	if err != nil {
		return fmt.Errorf("hashing build config: %w", err)
	}

	return buildInstall(ctx, buildInstallOpts{
		WorkDir:               b.WorkDir,
		Name:                  b.Control.Name,
		Type:                  TypeControl,
		Mode:                  b.Mode,
		Hash:                  hash,
		AddPayload:            b.addPayload,
		BuildIgnition:         b.buildIgnition,
		Downloader:            b.Downloader,
		FlatcarUSBRootVersion: b.Fab.Status.Versions.Fabricator.ControlUSBRoot,
	})
}

func (b *ControlInstallBuilder) addPayload(ctx context.Context, slog *slog.Logger, installDir string) error {
	slog.Info("Adding k3s and tools to installer")
	if err := b.Downloader.FromORAS(ctx, installDir, k3s.Ref, k3s.Version(b.Fab), []artificer.ORASFile{
		{
			Name: k3s.BinName,
		},
		{
			Name: k3s.InstallName,
			Mode: 0o700,
		},
		{
			Name: k3s.AirgapName,
		},
	}); err != nil {
		return fmt.Errorf("downloading k3s: %w", err)
	}

	if err := b.Downloader.FromORAS(ctx, installDir, k9s.Ref, k9s.Version(b.Fab), []artificer.ORASFile{
		{
			Name: k9s.BinName,
		},
	}); err != nil {
		return fmt.Errorf("downloading k9s: %w", err)
	}

	slog.Info("Adding zot to installer")
	if err := b.Downloader.FromORAS(ctx, installDir, zot.AirgapRef, zot.Version(b.Fab), []artificer.ORASFile{
		{
			Name: zot.AirgapImageName,
		},
		{
			Name: zot.AirgapChartName,
		},
	}); err != nil {
		return fmt.Errorf("downloading zot: %w", err)
	}

	slog.Info("Adding flatcar upgrade bin to installer")
	if err := b.Downloader.FromORAS(ctx, installDir, flatcar.UpdateRef, flatcar.Version(b.Fab), []artificer.ORASFile{
		{
			Name: flatcar.UpdateBinName,
		},
	}); err != nil {
		return fmt.Errorf("downloading flatcar-update: %w", err)
	}

	slog.Info("Adding cert-manager to installer")
	if err := b.Downloader.FromORAS(ctx, installDir, certmanager.AirgapRef, certmanager.Version(b.Fab), []artificer.ORASFile{
		{
			Name: certmanager.AirgapImageName,
		},
		{
			Name: certmanager.AirgapChartName,
		},
	}); err != nil {
		return fmt.Errorf("downloading cert-manager: %w", err)
	}

	slog.Info("Adding config and wiring files to installer")
	fabF, err := os.Create(filepath.Join(installDir, FabName))
	if err != nil {
		return fmt.Errorf("creating fab file: %w", err)
	}
	defer fabF.Close()

	if err := apiutil.PrintFab(b.Fab, []fabapi.ControlNode{b.Control}, b.Nodes, fabF); err != nil {
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

	slog.Info("Adding CLIs to installer")
	// TODO remove if it'll be managed by control agent?
	if err := b.Downloader.FromORAS(ctx, installDir, fabric.CtlRef, b.Fab.Status.Versions.Fabric.Ctl, []artificer.ORASFile{
		{
			Name: fabric.CtlBinName,
		},
	}); err != nil {
		return fmt.Errorf("downloading hhfctl: %w", err)
	}

	// TODO remove if it'll be managed by control agent?
	if err := b.Downloader.FromORAS(ctx, installDir, f8r.CtlRef, b.Fab.Status.Versions.Fabricator.Ctl, []artificer.ORASFile{
		{
			Name: f8r.CtlBinName,
		},
	}); err != nil {
		return fmt.Errorf("downloading hhfabctl: %w", err)
	}

	if b.Fab.Spec.Config.Registry.IsAirgap() {
		slog.Info("Adding airgap artifacts to installer")

		airgapArts, err := comp.CollectArtifacts(b.Fab, AirgapArtifactLists...)
		if err != nil {
			return fmt.Errorf("collecting airgap artifacts: %w", err)
		}

		for ref, version := range airgapArts {
			if err := b.Downloader.GetOCI(ctx, ref, version, installDir); err != nil {
				return fmt.Errorf("downloading airgap artifact %q: %w", ref, err)
			}
		}
	}

	return nil
}

//go:embed control_butane.tmpl.yaml
var controlButaneTmpl string

func (b *ControlInstallBuilder) buildIgnition() ([]byte, error) {
	autoInstallPath := filepath.Join(OSTargetInstallDir, string(TypeControl)+Separator+b.Control.Name+Separator+InstallSuffix)
	if b.Mode == BuildModeManual {
		autoInstallPath = ""
	}

	dummyIP, err := b.Control.Spec.Dummy.IP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing dummy IP: %w", err)
	}
	if dummyIP.Bits() != 31 {
		return nil, fmt.Errorf("dummy IP must be a /31") //nolint:goerr113
	}

	but, err := tmplutil.FromTemplate("control-butane", controlButaneTmpl, map[string]any{
		"Hostname":       b.Control.Name,
		"PasswordHash":   b.Fab.Spec.Config.Control.DefaultUser.PasswordHash,
		"AuthorizedKeys": b.Fab.Spec.Config.Control.DefaultUser.AuthorizedKeys,
		"MgmtInterface":  b.Control.Spec.Management.Interface,
		"MgmtAddress":    b.Control.Spec.Management.IP,
		"ControlVIP":     b.Fab.Spec.Config.Control.VIP,
		"ExtInterface":   b.Control.Spec.External.Interface,
		"ExtAddress":     b.Control.Spec.External.IP,
		"ExtGateway":     b.Control.Spec.External.Gateway,
		"ExtDNS":         b.Control.Spec.External.DNS,
		"DummyAddress":   dummyIP.Masked().String(),
		"DummyGateway":   dummyIP.Masked().Addr().Next().String(),
		"AutoInstall":    autoInstallPath,
	})
	if err != nil {
		return nil, fmt.Errorf("butane: %w", err)
	}

	ign, err := butaneutil.Translate(but)
	if err != nil {
		return nil, fmt.Errorf("translating butane: %w", err)
	}

	return ign, nil
}

func (b *ControlInstallBuilder) hash(ctx context.Context) (string, error) {
	h := sha256.New()

	if _, err := h.Write([]byte(version.Version)); err != nil {
		return "", fmt.Errorf("hashing version: %w", err)
	}

	if err := apiutil.PrintFab(b.Fab, []fabapi.ControlNode{b.Control}, b.Nodes, h); err != nil {
		return "", fmt.Errorf("hashing fab: %w", err)
	}

	if err := apiutil.PrintWiring(ctx, b.Wiring, h); err != nil {
		return "", fmt.Errorf("hashing wiring: %w", err)
	}

	if _, err := fmt.Fprintf(h, "%s", b.Mode); err != nil {
		return "", fmt.Errorf("hashing build mode: %w", err)
	}

	return base64.URLEncoding.EncodeToString(h.Sum(nil)), nil
}
