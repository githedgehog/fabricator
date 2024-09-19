package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/hhfab"
	"go.githedgehog.com/fabricator/pkg/version"
)

const (
	FlagCatGlobal          = "Global options:"
	FlagNameRegistryRepo   = "registry-repo"
	FlagNameRegistryPrefix = "registry-prefix"
	FlagNameDev            = "dev"
	FlagNameConfig         = "config"
	FlagNameWithDefaults   = "with-defaults"
	FlagNameWiring         = "wiring"
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
		Usage:       "run as if hhfab was started in `PATH` instead of the current working directory",
		EnvVars:     []string{"HHFAB_WORK_DIR"},
		Value:       defaultWorkDir,
		Destination: &workDir,
		Category:    FlagCatGlobal,
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting user home dir: %w", err)
	}

	defaultCacheDir := filepath.Join(home, ".hhfab-cache")
	var cacheDir string
	cacheDirFlag := &cli.StringFlag{
		Name:        "cache-dir",
		Usage:       "use cache dir `DIR` for caching downloaded files",
		EnvVars:     []string{"HHFAB_CACHE_DIR"},
		Value:       defaultCacheDir,
		Destination: &cacheDir,
		Category:    FlagCatGlobal,
	}

	before := func() cli.BeforeFunc {
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
			logger := slog.New(
				tint.NewHandler(logW, &tint.Options{
					Level:      logLevel,
					TimeFormat: time.TimeOnly,
					NoColor:    !isatty.IsTerminal(logW.Fd()),
				}),
			)
			slog.SetDefault(logger)

			args := []any{
				"version", version.Version,
			}

			if workDir != defaultWorkDir {
				args = append(args, "workdir", workDir)
			}

			if cacheDir != defaultCacheDir {
				args = append(args, "cache", cacheDir)
			}

			slog.Info("Hedgehog Fabricator", args...)

			return nil
		}
	}

	defaultFlags := []cli.Flag{
		workDirFlag,
		cacheDirFlag,
		verboseFlag,
		briefFlag,
	}

	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}
	app := &cli.App{
		Name:  "hhfab",
		Usage: "hedgehog fabricator - build, install and run hedgehog",
		Description: `Create Hedgehog configs, wiring diagram, build an installer and optionally run the virtual lab (VLAB):
	1.  Initialize working dir by running 'hhfab init', to use default creds use '--dev' (unsafe)
	2a. If building for physical environment, use 'hhfab wiring sample' to generate sample wiring diagram
	2b. If building for VLAB, use 'hhfab wiring vlab' to generate VLAB wiring diagram
	3.  Validate configs and wiring with 'hhfab validate' at any time (optional)
	4.  Build Hedgehog installer with 'hhfab build'
	5.  Use 'hhfab vlab up' to run VLAB (will run build automatically if needed)
		`,
		Version:                version.Version,
		Suggest:                true,
		UseShortOptionHandling: true,
		EnableBashCompletion:   true,
		Commands: []*cli.Command{
			{
				Name:  "init",
				Usage: "initializes working dir (current dir by default) with a new fab.yaml and other files",
				Flags: append(defaultFlags,
					&cli.StringFlag{
						Name:    FlagNameRegistryRepo,
						Usage:   "download artifacts from `REPO`",
						EnvVars: []string{"HHFAB_REG_REPO"},
						Value:   hhfab.DefaultRepo,
					},
					&cli.StringFlag{
						Name:    FlagNameRegistryPrefix,
						Usage:   "prepend artifact names with `PREFIX`",
						EnvVars: []string{"HHFAB_REG_PREFIX"},
						Value:   hhfab.DefaultPrefix,
					},
					&cli.BoolFlag{
						Name:    FlagNameDev,
						Usage:   "use default credentials (unsafe)",
						EnvVars: []string{"HHFAB_DEV"},
					},
					&cli.StringFlag{
						Name:    FlagNameConfig,
						Aliases: []string{"c"},
						Usage:   "use existing config file `PATH`",
						EnvVars: []string{"HHFAB_CONFIG"},
					},
					&cli.BoolFlag{
						Name:  FlagNameWithDefaults,
						Usage: "use full config with all default values",
					},
					&cli.StringSliceFlag{
						Name:    FlagNameWiring,
						Aliases: []string{"w"},
						Usage:   "include wiring diagram `FILE` with ext .yaml (any Fabric API objects)",
					},
				),
				Before: before(),
				Action: func(c *cli.Context) error {
					if err := hhfab.Init(hhfab.InitConfig{
						WorkDir:      workDir,
						CacheDir:     cacheDir,
						Repo:         c.String(FlagNameRegistryRepo),
						Prefix:       c.String(FlagNameRegistryPrefix),
						WithDefaults: c.Bool(FlagNameWithDefaults),
						ImportConfig: c.String(FlagNameConfig),
						Wiring:       c.StringSlice(FlagNameWiring),
						Dev:          c.Bool(FlagNameDev),
						Airgap:       false,
					}); err != nil {
						return fmt.Errorf("initializing: %w", err)
					}

					return nil
				},
			},
			{
				Name:   "validate",
				Usage:  "validate config and included wiring files",
				Flags:  defaultFlags,
				Before: before(),
				Action: func(c *cli.Context) error {
					cfg, err := hhfab.Load(workDir, cacheDir)
					if err != nil {
						return fmt.Errorf("loading config: %w", err)
					}

					fmt.Println(cfg)
					panic("not implemented")

					return nil
				},
			},
			{
				Name:   "build",
				Usage:  "build installers",
				Flags:  defaultFlags,
				Before: before(),
				Action: func(c *cli.Context) error {
					cfg, err := hhfab.Load(workDir, cacheDir)
					if err != nil {
						return fmt.Errorf("loading config: %w", err)
					}

					fmt.Println(cfg)
					panic("not implemented")

					return nil
				},
			},
			{
				Name:        "_helpers",
				Hidden:      true,
				Subcommands: []*cli.Command{
					// TODO things to auto run with sudo
					// kill stale vms
					// create/remove taps/bridges
					// prepare interfaces for passthrough
				},
			},
		},
	}

	return app.Run(os.Args) //nolint:wrapcheck
}
