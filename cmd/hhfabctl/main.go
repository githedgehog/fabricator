// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/hhfabctl"
	"go.githedgehog.com/fabricator/pkg/version"
	"k8s.io/klog/v2"
	kctrl "sigs.k8s.io/controller-runtime"
)

const (
	FlagWorkDir = "workdir"
	FlagName    = "name"
	FlagForce   = "force"
	FlagYes     = "yes"
)

func setupLogger(verbose bool) error {
	logLevel := slog.LevelInfo
	if verbose {
		logLevel = slog.LevelDebug
	}

	logW := os.Stderr
	handler := tint.NewHandler(logW, &tint.Options{
		Level:      logLevel,
		TimeFormat: time.TimeOnly,
		NoColor:    !isatty.IsTerminal(logW.Fd()),
	})

	logger := slog.New(handler)
	slog.SetDefault(logger)
	kctrl.SetLogger(logr.FromSlogHandler(handler))
	klog.SetSlogLogger(logger)

	slog.Info("Hedgehog Fabricator ctl", "version", version.Version)

	return nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	preview := os.Getenv("HHFAB_PREVIEW") == "true"

	workdirDefault, err := os.Getwd()
	if err != nil { // TODO handle this error
		slog.Error("Failed to get working directory", "err", err.Error())
		os.Exit(1) //nolint:gocritic
	}

	var verbose bool
	verboseFlag := &cli.BoolFlag{
		Name:        "verbose",
		Aliases:     []string{"v"},
		Usage:       "verbose output (includes debug)",
		Value:       false,
		Destination: &verbose,
	}

	appName := "hhfabctl"
	usage := "Hedgehog Fabricator API CLI client"
	if len(os.Args) > 0 {
		if strings.HasSuffix(os.Args[0], "kubectl-hhfab") {
			appName = "kubectl hhfab"
			usage = "Hedgehog Fabricator API kubectl plugin"
		}
	}

	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}
	app := &cli.App{
		Name:                   appName,
		Usage:                  usage,
		Version:                version.Version,
		Suggest:                true,
		UseShortOptionHandling: true,
		EnableBashCompletion:   true,
		Flags: []cli.Flag{
			verboseFlag,
		},
		Commands: []*cli.Command{
			{
				Name:  "config",
				Usage: "config helpers",
				Flags: []cli.Flag{
					verboseFlag,
				},
				Subcommands: []*cli.Command{
					{
						Name:  "export",
						Usage: "Export config (Fabricator and ControlNodes)",
						Flags: []cli.Flag{
							verboseFlag,
						},
						Before: func(_ *cli.Context) error {
							return setupLogger(verbose)
						},
						Action: func(_ *cli.Context) error {
							if err := hhfabctl.ConfigExport(ctx); err != nil {
								return fmt.Errorf("config exporting: %w", err)
							}

							return nil
						},
					},
				},
			},
			{
				Name:   "support",
				Usage:  "[PREVIEW] Support dump helpers",
				Hidden: !preview,
				Flags: []cli.Flag{
					verboseFlag,
				},
				Subcommands: []*cli.Command{
					{
						Name:  "dump",
						Usage: "collect support data into archive (EXPERIMENTAL, may contain sensitive data)",
						Flags: []cli.Flag{
							verboseFlag,
							&cli.BoolFlag{
								Name:    FlagYes,
								Aliases: []string{"y"},
								Usage:   "assume yes (accept that support dump may contain sensitive data)",
							},
							&cli.StringFlag{
								Name:    FlagName,
								Aliases: []string{"n"},
								Usage:   "name of the support dump",
								Value:   "hhfab-" + strings.ReplaceAll(time.Now().Format(time.RFC3339), ":", "-"),
							},
							&cli.BoolFlag{
								Name:    FlagForce,
								Aliases: []string{"f"},
								Usage:   "force overwrite existing support dump file",
							},
							&cli.StringFlag{
								Name:    FlagWorkDir,
								Aliases: []string{"w"},
								Usage:   "working directory for creating support dump",
								Value:   workdirDefault,
							},
						},
						Before: func(_ *cli.Context) error {
							return setupLogger(verbose)
						},
						Action: func(cCtx *cli.Context) error {
							if !cCtx.Bool(FlagYes) {
								return cli.Exit("\033[31mWARNING:\033[0m (EXPERIMENTAL) Support dump may contain sensitive data. Please confirm with --yes if you're sure.", 1)
							}

							if err := hhfabctl.SupportDump(ctx, hhfabctl.SupportDumpOpts{
								WorkDir: cCtx.String(FlagWorkDir),
								Name:    cCtx.String(FlagName),
								Force:   cCtx.Bool(FlagForce),
							}); err != nil {
								return fmt.Errorf("collecting support dump: %w", err)
							}

							return nil
						},
					},
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error("Failed", "err", err.Error())
		os.Exit(1)
	}
}
