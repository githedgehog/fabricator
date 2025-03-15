// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/hhfab/diagram"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
)

const (
	DefaultVLABGeneratedFile = "vlab.generated.yaml"
)

func VLABGenerate(ctx context.Context, workDir, cacheDir string, builder VLABBuilder, target string) error {
	cfg, err := load(ctx, workDir, cacheDir, false, HydrateModeNever)
	if err != nil {
		return err
	}

	wL := apiutil.NewWiringLoader()
	if err := builder.Build(ctx, wL, cfg.Fab.Spec.Config.Fabric.Mode, cfg.Nodes); err != nil {
		return err
	}

	includeDir := filepath.Join(workDir, IncludeDir)
	wiringFile, err := os.OpenFile(filepath.Join(includeDir, target), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating wiring file: %w", err)
	}
	defer wiringFile.Close()

	if err := apiutil.PrintWiring(ctx, wL.GetClient(), wiringFile); err != nil {
		return fmt.Errorf("writing wiring file: %w", err)
	}

	relName := filepath.Join(IncludeDir, target)
	slog.Info("Generated wiring file", "name", relName)

	files, err := os.ReadDir(includeDir)
	if err != nil {
		return fmt.Errorf("reading include dir %q: %w", includeDir, err)
	}
	for _, file := range files {
		if file.IsDir() || file.Name() == target || !strings.HasSuffix(file.Name(), YAMLExt) {
			continue
		}

		slog.Warn("Include dir contains file(s) other than the generated wiring file", "name", file.Name())
	}

	return nil
}

type VLABUpOpts struct {
	HydrateMode HydrateMode
	NoCreate    bool
	ReCreate    bool
	BuildMode   recipe.BuildMode
	VLABRunOpts
}

func VLABUp(ctx context.Context, workDir, cacheDir string, opts VLABUpOpts) error {
	if opts.ControlUpgrade {
		opts.BuildMode = recipe.BuildModeManual
		opts.VLABRunOpts.BuildMode = recipe.BuildModeManual
	}

	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode)
	if err != nil {
		return err
	}

	vlab, err := c.PrepareVLAB(ctx, opts)
	if err != nil {
		return fmt.Errorf("preparing VLAB: %w", err)
	}

	if err := c.build(ctx, BuildOpts{
		HydrateMode: opts.HydrateMode,
		BuildMode:   opts.BuildMode,
	}); err != nil {
		return fmt.Errorf("building: %w", err)
	}

	return c.VLABRun(ctx, vlab, opts.VLABRunOpts)
}

func loadVLABForHelpers(ctx context.Context, workDir, cacheDir string) (*Config, *VLAB, error) {
	opts := VLABUpOpts{
		HydrateMode: HydrateModeIfNotPresent,
		NoCreate:    true,
	}

	c, err := load(ctx, workDir, cacheDir, true, opts.HydrateMode)
	if err != nil {
		return nil, nil, err
	}

	vlab, err := c.PrepareVLAB(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing VLAB: %w", err)
	}

	return c, vlab, nil
}

func DoVLABSSH(ctx context.Context, workDir, cacheDir, name string, args []string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSSH, name, args)
}

func DoVLABSerial(ctx context.Context, workDir, cacheDir, name string, args []string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSerial, name, args)
}

func DoVLABSerialLog(ctx context.Context, workDir, cacheDir, name string, args []string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABAccess(ctx, vlab, VLABAccessSerialLog, name, args)
}

func DoShowTech(ctx context.Context, workDir, cacheDir string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABShowTech(ctx, vlab)
}

func DoVLABSetupVPCs(ctx context.Context, workDir, cacheDir string, opts SetupVPCsOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.SetupVPCs(ctx, vlab, opts)
}

func DoVLABSetupPeerings(ctx context.Context, workDir, cacheDir string, opts SetupPeeringsOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.SetupPeerings(ctx, vlab, opts)
}

func DoVLABTestConnectivity(ctx context.Context, workDir, cacheDir string, opts TestConnectivityOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.TestConnectivity(ctx, vlab, opts)
}

func DoVLABWait(ctx context.Context, workDir, cacheDir string) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.Wait(ctx, vlab)
}

func DoVLABInspect(ctx context.Context, workDir, cacheDir string, opts InspectOpts) error {
	c, vlab, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.Inspect(ctx, vlab, opts)
}

type SwitchPowerOpts struct {
	Switches    []string   // All switches if empty
	Action      pdu.Action // Power action (e.g., on, off, cycle)
	PDUUsername string
	PDUPassword string
}

func DoSwitchPower(ctx context.Context, workDir, cacheDir string, opts SwitchPowerOpts) error {
	c, _, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABSwitchPower(ctx, opts)
}

type SwitchReinstallOpts struct {
	Switches       []string            // All switches if empty
	Mode           SwitchReinstallMode // "reboot" or "hard-reset"
	SwitchUsername string              // Username for switch access (reboot mode only )
	SwitchPassword string              // Password for switch access (reboot mode only)
	PDUUsername    string              // (hard-reset mode only)
	PDUPassword    string              // (hard-reset mode only)
	WaitReady      bool                // Wait for the switch to be ready
}

type SwitchReinstallMode string

const (
	ReinstallModeReboot    SwitchReinstallMode = "reboot"
	ReinstallModeHardReset SwitchReinstallMode = "hard-reset"
)

var ReinstallModes = []SwitchReinstallMode{
	ReinstallModeReboot,
	ReinstallModeHardReset,
}

func DoSwitchReinstall(ctx context.Context, workDir, cacheDir string, opts SwitchReinstallOpts) error {
	c, _, err := loadVLABForHelpers(ctx, workDir, cacheDir)
	if err != nil {
		return err
	}

	return c.VLABSwitchReinstall(ctx, opts)
}

func Diagram(workDir, format string, styleType diagram.StyleType) error {
	includeDir := filepath.Join(workDir, IncludeDir)

	files, err := os.ReadDir(includeDir)
	if err != nil {
		return fmt.Errorf("reading include directory: %w", err)
	}

	var yamlFiles []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), YAMLExt) {
			yamlPath := filepath.Join(includeDir, file.Name())
			yamlFiles = append(yamlFiles, yamlPath)
			slog.Debug("Found YAML file", "path", yamlPath)
		}
	}

	if len(yamlFiles) == 0 {
		return fmt.Errorf("no YAML files found in include directory") //nolint:goerr113
	}

	var content []byte
	for _, file := range yamlFiles {
		fileContent, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("reading file %s: %w", file, err)
		}
		if len(content) > 0 {
			content = append(content, []byte("\n---\n")...)
		}
		content = append(content, fileContent...)
	}

	loader := apiutil.NewWiringLoader()
	objs, err := loader.Load(content)
	if err != nil {
		return fmt.Errorf("loading wiring YAML: %w", err)
	}

	jsonData, err := json.MarshalIndent(objs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}

	format = strings.ToLower(format)
	switch format {
	case "drawio":
		slog.Debug("Generating draw.io diagram", "style", styleType)
		if err := diagram.GenerateDrawio(workDir, jsonData, styleType); err != nil {
			return fmt.Errorf("generating draw.io diagram: %w", err)
		}
		filePath := filepath.Join(workDir, "vlab-diagram.drawio")
		slog.Info("Generated draw.io diagram", "file", filePath, "style", styleType)
		fmt.Printf("To use this diagram:\n")
		fmt.Printf("1. Open with https://app.diagrams.net/ or the desktop Draw.io application\n")
		fmt.Printf("2. You can edit the diagram and export to PNG, SVG, PDF or other formats\n")
	case "dot":
		slog.Debug("Generating DOT diagram")
		if err := diagram.GenerateDOT(workDir, jsonData); err != nil {
			return fmt.Errorf("generating DOT diagram: %w", err)
		}
		filePath := filepath.Join(workDir, "vlab-diagram.dot")
		slog.Info("Generated graphviz diagram", "file", filePath)
		fmt.Printf("To render this diagram with Graphviz:\n")
		fmt.Printf("1. Install Graphviz: https://graphviz.org/download/\n")
		fmt.Printf("2. Convert to PNG: dot -Tpng %s -o vlab-diagram.png\n", filePath)
		fmt.Printf("3. Convert to SVG: dot -Tsvg %s -o vlab-diagram.svg\n", filePath)
		fmt.Printf("4. Convert to PDF: dot -Tpdf %s -o vlab-diagram.pdf\n", filePath)
	case "mermaid":
		return fmt.Errorf("mermaid format is not supported yet") //nolint:goerr113
	default:
		return fmt.Errorf("unsupported diagram format: %s", format) //nolint:goerr113
	}

	return nil
}
