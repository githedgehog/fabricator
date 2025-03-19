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
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/butaneutil"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"go.githedgehog.com/fabricator/pkg/version"
)

type NodeInstallBuilder struct {
	WorkDir    string
	Fab        fabapi.Fabricator // TODO can we actually get rid of it for the node installation?
	Node       fabapi.Node
	Mode       BuildMode
	Downloader *artificer.Downloader
}

func (b *NodeInstallBuilder) Build(ctx context.Context) error {
	hash, err := b.hash()
	if err != nil {
		return fmt.Errorf("hashing build config: %w", err)
	}

	return buildInstall(ctx, buildInstallOpts{
		WorkDir:               b.WorkDir,
		Name:                  b.Node.Name,
		Type:                  TypeNode,
		Mode:                  b.Mode,
		Hash:                  hash,
		AddPayload:            b.addPayload,
		BuildIgnition:         b.buildIgnition,
		Downloader:            b.Downloader,
		FlatcarUSBRootVersion: b.Fab.Status.Versions.Fabricator.ControlUSBRoot,
	})
}

func (b *NodeInstallBuilder) addPayload(ctx context.Context, slog *slog.Logger, installDir string) error {
	slog.Info("Adding k3s to installer")
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

	slog.Info("Adding flatcar upgrade bin to installer")
	if err := b.Downloader.FromORAS(ctx, installDir, flatcar.UpdateRef, flatcar.Version(b.Fab), []artificer.ORASFile{
		{
			Name: flatcar.UpdateBinName,
		},
	}); err != nil {
		return fmt.Errorf("downloading flatcar-update: %w", err)
	}

	slog.Info("Adding config files to installer")
	fabF, err := os.Create(filepath.Join(installDir, FabName))
	if err != nil {
		return fmt.Errorf("creating fab file: %w", err)
	}
	defer fabF.Close()

	if err := apiutil.PrintFab(b.Fab, nil, []fabapi.Node{b.Node}, fabF); err != nil {
		return fmt.Errorf("printing fab: %w", err)
	}

	return nil
}

//go:embed node_butane.tmpl.yaml
var nodeButaneTmpl string

func (b *NodeInstallBuilder) buildIgnition() ([]byte, error) {
	autoInstallPath := filepath.Join(OSTargetInstallDir, string(TypeNode)+Separator+b.Node.Name+Separator+InstallSuffix)
	if b.Mode == BuildModeManual {
		autoInstallPath = ""
	}

	dummyIP, err := b.Node.Spec.Dummy.IP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing dummy IP: %w", err)
	}
	if dummyIP.Bits() != 31 {
		return nil, fmt.Errorf("dummy IP must be a /31") //nolint:goerr113
	}

	but, err := tmplutil.FromTemplate("node-butane", nodeButaneTmpl, map[string]any{
		"Hostname":       b.Node.Name,
		"PasswordHash":   b.Fab.Spec.Config.Control.DefaultUser.PasswordHash,
		"AuthorizedKeys": b.Fab.Spec.Config.Control.DefaultUser.AuthorizedKeys,
		"MgmtInterface":  b.Node.Spec.Management.Interface,
		"MgmtAddress":    b.Node.Spec.Management.IP,
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

func (b *NodeInstallBuilder) hash() (string, error) {
	h := sha256.New()

	if _, err := h.Write([]byte(version.Version)); err != nil {
		return "", fmt.Errorf("hashing version: %w", err)
	}

	if err := apiutil.PrintFab(b.Fab, nil, []fabapi.Node{b.Node}, h); err != nil {
		return "", fmt.Errorf("hashing fab: %w", err)
	}

	if _, err := fmt.Fprintf(h, "%s", b.Mode); err != nil {
		return "", fmt.Errorf("hashing build mode: %w", err)
	}

	return base64.URLEncoding.EncodeToString(h.Sum(nil)), nil
}
