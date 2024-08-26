// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	_ "embed"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/fab/flatcar/install"
)

var version = "(devel)"

func setupLogger(verbose, brief bool) error {
	if verbose && brief {
		return cli.Exit("verbose and brief are mutually exclusive", 1)
	}

	logLevel := slog.LevelInfo
	if verbose {
		logLevel = slog.LevelDebug
	} else if brief {
		logLevel = slog.LevelWarn
	}

	logW := os.Stdout
	logger := slog.New(
		tint.NewHandler(logW, &tint.Options{
			Level:      logLevel,
			TimeFormat: time.TimeOnly,
			NoColor:    !isatty.IsTerminal(logW.Fd()),
		}),
	)
	slog.SetDefault(logger)

	return nil
}

func main() {
	var verbose, brief bool
	verboseFlag := &cli.BoolFlag{
		Name:        "verbose",
		Aliases:     []string{"v"},
		Usage:       "verbose output (includes debug)",
		Destination: &verbose,
	}
	briefFlag := &cli.BoolFlag{
		Name:        "brief",
		Aliases:     []string{"b"},
		Usage:       "brief output (only warn and error)",
		Destination: &brief,
	}

	var basedir string
	basedirFlag := &cli.StringFlag{
		Name:        "basedir",
		Aliases:     []string{"d"},
		Usage:       "use workir `DIR`",
		Value:       ".", // TODO should it be some default path?
		Destination: &basedir,
	}

	var dryRun bool
	dryRunFlag := &cli.BoolFlag{
		Name:        "dry-run",
		Aliases:     []string{"n"},
		Usage:       "dry run (don't actually run anything)",
		Destination: &dryRun,
	}

	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}
	app := &cli.App{
		Name:                   "hhfab-flatcar-install",
		Usage:                  "hedgehog flatcar installer",
		Version:                version,
		Suggest:                true,
		UseShortOptionHandling: true,
		EnableBashCompletion:   true,
		Flags: []cli.Flag{
			basedirFlag,
			verboseFlag,
			briefFlag,
			dryRunFlag,
		},
		Before: func(_ *cli.Context) error {
			return setupLogger(verbose, brief)
		},
		Action: func(_ *cli.Context) error {
			slog.Info("Running flatcar-instal", "version", version)

			return errors.Wrapf(install.Do(context.TODO(), basedir, dryRun), "error running flatcar installer")
		},
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error("Failed", "err", err.Error())
		os.Exit(1)
	}
}
