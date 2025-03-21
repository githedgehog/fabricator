// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/artificer"
	"go.githedgehog.com/fabricator/pkg/embed/recipebin"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
	"go.githedgehog.com/fabricator/pkg/fab/comp/f8r"
	"go.githedgehog.com/fabricator/pkg/fab/comp/fabric"
	"go.githedgehog.com/fabricator/pkg/fab/comp/flatcar"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k9s"
	"go.githedgehog.com/fabricator/pkg/fab/comp/ntp"
	"go.githedgehog.com/fabricator/pkg/fab/comp/reloader"
	"go.githedgehog.com/fabricator/pkg/fab/comp/zot"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/butaneutil"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"go.githedgehog.com/fabricator/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed control_butane.tmpl.yaml
var controlButaneTmpl string

type BuildMode string

const (
	BuildModeManual BuildMode = "manual"
	BuildModeUSB    BuildMode = "usb"
	BuildModeISO    BuildMode = "iso"
)

var BuildModes = []BuildMode{BuildModeManual, BuildModeUSB, BuildModeISO}

type ControlInstallBuilder struct {
	WorkDir    string
	Fab        fabapi.Fabricator
	Control    fabapi.ControlNode
	Nodes      []fabapi.Node
	Wiring     client.Reader
	Mode       BuildMode
	Downloader *artificer.Downloader
}

const (
	FabName                      = "fab.yaml"
	WiringName                   = "wiring.yaml"
	InstallSuffix                = "-install"
	InstallArchiveSuffix         = InstallSuffix + ".tgz"
	InstallIgnitionSuffix        = InstallSuffix + ".ign"
	InstallUSBImageWorkdirSuffix = InstallSuffix + "-usb.wip"
	InstallUSBImageSuffix        = InstallSuffix + "-usb.img"
	InstallISOImageSuffix        = InstallSuffix + "-usb.iso"
	InstallHashSuffix            = InstallSuffix + ".inhash"
	RecipeBin                    = "hhfab-recipe"
)

var AirgapArtifactLists = []comp.ListOCIArtifacts{
	flatcar.Artifacts,
	certmanager.Artifacts,
	zot.Artifacts,
	reloader.Artifacts,
	fabric.Artifacts,
	ntp.Artifacts,
	f8r.Artifacts,
}

func (b *ControlInstallBuilder) Build(ctx context.Context) error {
	if !slices.Contains(BuildModes, b.Mode) {
		return fmt.Errorf("invalid build mode %q", b.Mode) //nolint:goerr113
	}

	installDir := filepath.Join(b.WorkDir, b.Control.Name+InstallSuffix)
	installArchive := filepath.Join(b.WorkDir, b.Control.Name+InstallArchiveSuffix)
	installIgnition := filepath.Join(b.WorkDir, b.Control.Name+InstallIgnitionSuffix)
	installHashFile := filepath.Join(b.WorkDir, b.Control.Name+InstallHashSuffix)

	newHash, err := b.hash(ctx)
	if err != nil {
		return fmt.Errorf("hashing: %w", err)
	}

	if existingHash, err := os.ReadFile(installHashFile); err == nil {
		slog.Debug("Checking existing installers", "new", newHash, "existing", string(existingHash))

		files := []string{installDir, installArchive, installIgnition}
		if b.Mode == BuildModeUSB {
			files = []string{installDir, filepath.Join(b.WorkDir, b.Control.Name+InstallUSBImageSuffix)}
		}
		if b.Mode == BuildModeISO {
			files = []string{installDir, filepath.Join(b.WorkDir, b.Control.Name+InstallISOImageSuffix)}
		}
		if string(existingHash) == newHash && isPresent(files...) {
			slog.Info("Using existing installers")

			return nil
		}
	}

	if err := removeIfExists(installHashFile); err != nil {
		return fmt.Errorf("removing hash file: %w", err)
	}

	slog.Info("Building installer", "control", b.Control.Name)

	if err := removeIfExists(installDir); err != nil {
		return fmt.Errorf("removing install dir: %w", err)
	}
	if err := removeIfExists(installArchive); err != nil {
		return fmt.Errorf("removing install archive: %w", err)
	}
	if err := removeIfExists(installIgnition); err != nil {
		return fmt.Errorf("removing install ignition: %w", err)
	}
	if err := removeIfExists(filepath.Join(b.WorkDir, b.Control.Name+InstallUSBImageWorkdirSuffix)); err != nil {
		return fmt.Errorf("removing install usb image workdir: %w", err)
	}
	if err := removeIfExists(filepath.Join(b.WorkDir, b.Control.Name+InstallUSBImageSuffix)); err != nil {
		return fmt.Errorf("removing install usb image: %w", err)
	}
	if err := removeIfExists(filepath.Join(b.WorkDir, b.Control.Name+InstallISOImageSuffix)); err != nil {
		return fmt.Errorf("removing install iso image: %w", err)
	}

	if err := os.MkdirAll(installDir, 0o700); err != nil {
		return fmt.Errorf("creating install dir: %w", err)
	}

	slog.Info("Adding recipe bin to installer", "control", b.Control.Name)
	recipeBin, err := recipebin.Bytes()
	if err != nil {
		return fmt.Errorf("getting recipe bin: %w", err)
	}
	if err := os.WriteFile(filepath.Join(installDir, RecipeBin), recipeBin, 0o700); err != nil { //nolint:gosec
		return fmt.Errorf("writing recipe bin: %w", err)
	}

	slog.Info("Adding k3s and tools to installer", "control", b.Control.Name)
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

	slog.Info("Adding zot to installer", "control", b.Control.Name)
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

	slog.Info("Adding Flatcar Upgrade bin to installer", "control", b.Control.Name)
	if err := b.Downloader.FromORAS(ctx, installDir, flatcar.UpdateRef, flatcar.Version(b.Fab), []artificer.ORASFile{
		{
			Name: flatcar.UpdateBinName,
		},
	}); err != nil {
		return fmt.Errorf("downloading flatcar-update: %w", err)
	}

	slog.Info("Adding cert-manager to installer", "control", b.Control.Name)
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

	slog.Info("Adding config and wiring files to installer", "control", b.Control.Name)
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
		slog.Info("Adding airgap artifacts to installer", "control", b.Control.Name)

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

	if b.Mode == BuildModeUSB || b.Mode == BuildModeISO {
		if err := b.buildUSBImage(ctx); err != nil {
			return fmt.Errorf("building USB image: %w", err)
		}
	} else {
		slog.Info("Archiving installer", "path", installArchive, "control", b.Control.Name)
		if err := archiveTarGz(ctx, installDir, installArchive); err != nil {
			return fmt.Errorf("archiving install: %w", err)
		}

		slog.Info("Creating ignition", "path", installIgnition, "control", b.Control.Name)
		ign, err := controlIgnition(b.Fab, b.Control, "")
		if err != nil {
			return fmt.Errorf("creating ignition: %w", err)
		}

		if err := os.WriteFile(installIgnition, ign, 0o600); err != nil {
			return fmt.Errorf("writing ignition: %w", err)
		}
	}

	if err := os.WriteFile(installHashFile, []byte(newHash), 0o600); err != nil {
		return fmt.Errorf("writing hash: %w", err)
	}

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

func controlIgnition(fab fabapi.Fabricator, control fabapi.ControlNode, autoInstall string) ([]byte, error) {
	dummyIP, err := control.Spec.Dummy.IP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing dummy IP: %w", err)
	}
	if dummyIP.Bits() != 31 {
		return nil, fmt.Errorf("dummy IP must be a /31") //nolint:goerr113
	}

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
		"DummyAddress":   dummyIP.Masked().String(),
		"DummyGateway":   dummyIP.Masked().Addr().Next().String(),
		"AutoInstall":    autoInstall,
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

func isPresent(files ...string) bool {
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}

	return true
}
