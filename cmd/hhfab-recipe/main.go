package main

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/fab/cnc"
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

//go:embed motd.txt
var motd string

func main() {
	fmt.Println(motd)
	fmt.Println("Version:", version)

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
		Value:       ".",
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
		Name:                   "hhfab-recipe",
		Usage:                  "hedgehog fabricator recipe runner - follow actions (steps) from recipe.yaml",
		Version:                version,
		Suggest:                true,
		UseShortOptionHandling: true,
		EnableBashCompletion:   true,
		Commands: []*cli.Command{
			{
				Name:      "run",
				Usage:     "run steps from recipe.yaml in the basedir",
				UsageText: "Empty or 'all' for all actions (default) or list actions as args to run",
				Flags: []cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
					dryRunFlag,
				},
				Before: func(cCtx *cli.Context) error {
					return setupLogger(verbose, brief)
				},
				Action: func(cCtx *cli.Context) error {
					return errors.Wrapf(cnc.RunRecipe(basedir, cCtx.Args().Slice(), dryRun), "error running recipe")
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error("Failed", "err", err.Error())
		os.Exit(1)
	}
}
