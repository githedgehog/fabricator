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
	ctrl "sigs.k8s.io/controller-runtime"
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
	ctrl.SetLogger(logr.FromSlogHandler(handler))
	klog.SetSlogLogger(logger)

	return nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var verbose bool
	verboseFlag := &cli.BoolFlag{
		Name:        "verbose",
		Aliases:     []string{"v"},
		Usage:       "verbose output (includes debug)",
		Value:       true, // TODO disable debug by default
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
		},
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error("Failed", "err", err.Error())
		os.Exit(1)
	}
}
