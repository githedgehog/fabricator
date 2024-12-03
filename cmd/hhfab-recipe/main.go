// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	slogmulti "github.com/samber/slog-multi"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/version"
	"gopkg.in/natefinch/lumberjack.v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	FlagCatGlobal = "Global options:"
)

func main() {
	if err := Run(context.Background()); err != nil {
		// TODO what if slog isn't initialized yet?
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func Run(ctx context.Context) error {
	var verbose, brief bool
	verboseFlag := &cli.BoolFlag{
		Name:        "verbose",
		Aliases:     []string{"v"},
		Usage:       "verbose output (includes debug)",
		EnvVars:     []string{"HHFAB_VERBOSE"},
		Destination: &verbose,
		Category:    FlagCatGlobal,
	}
	briefFlag := &cli.BoolFlag{
		Name:        "brief",
		Aliases:     []string{"b"},
		Usage:       "brief output (only warn and error)",
		EnvVars:     []string{"HHFAB_BRIEF"},
		Destination: &brief,
		Category:    FlagCatGlobal,
	}

	defaultWorkDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current dir: %w", err)
	}

	var workDir string
	workDirFlag := &cli.StringFlag{
		Name:        "workdir",
		Usage:       "run as if hhfab-recipe was started in `PATH` instead of the current working directory",
		EnvVars:     []string{"HHFAB_WORK_DIR"},
		Value:       defaultWorkDir,
		Destination: &workDir,
		Category:    FlagCatGlobal,
	}

	before := func(installLog bool) cli.BeforeFunc {
		return func(_ *cli.Context) error {
			if verbose && brief {
				return cli.Exit("verbose and brief are mutually exclusive", 1)
			}

			logLevel := slog.LevelInfo
			if verbose {
				logLevel = slog.LevelDebug
			} else if brief {
				logLevel = slog.LevelWarn
			}

			logW := os.Stderr
			handlers := []slog.Handler{
				tint.NewHandler(logW, &tint.Options{
					Level:      logLevel,
					TimeFormat: time.TimeOnly,
					NoColor:    !isatty.IsTerminal(logW.Fd()),
				}),
			}

			if installLog {
				logFile := &lumberjack.Logger{
					Filename:   recipe.InstallLog,
					MaxSize:    5, // MB
					MaxBackups: 4,
					MaxAge:     30, // days
					Compress:   true,
					FileMode:   0o644,
				}

				handlers = append(handlers, slog.NewTextHandler(logFile, &slog.HandlerOptions{
					Level: slog.LevelDebug,
				}))
			}

			handler := slogmulti.Fanout(handlers...)
			logger := slog.New(handler)
			slog.SetDefault(logger)
			ctrl.SetLogger(logr.FromSlogHandler(handler))

			args := []any{
				"version", version.Version,
			}

			if workDir != defaultWorkDir {
				args = append(args, "workdir", workDir)
			}

			slog.Info("Hedgehog Fabricator Recipe", args...)

			return nil
		}
	}

	defaultFlags := []cli.Flag{
		workDirFlag,
		verboseFlag,
		briefFlag,
	}

	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}
	app := &cli.App{
		Name:                   "hhfab-recipe",
		Usage:                  "hedgehog fabricator recipe runner",
		Version:                version.Version,
		Suggest:                true,
		UseShortOptionHandling: true,
		EnableBashCompletion:   true,
		Commands: []*cli.Command{
			{
				Name: "control",
				Subcommands: []*cli.Command{
					{
						Name:   "install",
						Usage:  "install control node",
						Flags:  defaultFlags,
						Before: before(true),
						Action: func(_ *cli.Context) error {
							err := recipe.DoControlInstall(ctx, workDir)
							if err != nil {
								return fmt.Errorf("control install: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "upgrade",
						Usage:  "upgrade control node",
						Flags:  defaultFlags,
						Before: before(true),
						Action: func(_ *cli.Context) error {
							err := recipe.DoControlUpgrade(ctx, workDir)
							if err != nil {
								return fmt.Errorf("control upgrade: %w", err)
							}

							return nil
						},
					},
				},
			},
		},
	}

	return app.Run(os.Args) //nolint:wrapcheck
}
