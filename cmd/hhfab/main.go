package main

import (
	_ "embed"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/fab"
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

	slog.Debug(motd)
	slog.Debug("Version: " + version)

	return nil
}

//go:embed motd.txt
var motd string

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

	var basedir, preset, wiring, wiringGenType, wiringGenPreset string
	basedirFlag := &cli.StringFlag{
		Name:        "basedir",
		Aliases:     []string{"d"},
		Usage:       "use workir `DIR`",
		Value:       ".hhfab",
		Destination: &basedir,
	}

	var presets []string
	for _, p := range fab.Presets {
		presets = append(presets, string(p))
	}

	// sampleTypes := []string{"collapsedcore"} // TODO move to fabric
	// samplePresets := []string{}
	// for _, p := range sample.PresetsAll {
	// 	samplePresets = append(samplePresets, string(p))
	// }

	var dryRun, nopack bool

	var vm string
	vmFlag := &cli.StringFlag{
		Name:        "vm",
		Usage:       "use vm `VM-NAME`, use `control` for control vm",
		Destination: &vm,
	}

	mngr := fab.NewCNCManager()

	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}
	app := &cli.App{
		Name:                   "hhfab",
		Usage:                  "hedgehog fabricator - build, install and run hedgehog",
		Version:                version,
		Suggest:                true,
		UseShortOptionHandling: true,
		EnableBashCompletion:   true,
		Commands: []*cli.Command{
			{
				Name:    "init",
				Aliases: []string{"i"},
				Usage:   "initialize fabricator with specified PRESET",
				Flags: append([]cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
					&cli.StringFlag{
						Name:        "preset",
						Aliases:     []string{"p"},
						Usage:       "use preset `PRESET` (one of: " + strings.Join(presets, ", ") + ")",
						Required:    true,
						Destination: &preset,
					},
					&cli.StringFlag{
						Name:        "wiring-path",
						Aliases:     []string{"wiring", "w"},
						Usage:       "use wiring diagram from `FILE` (or dir), use '-' to read from stdin instead",
						Destination: &wiring,
					},
					// TODO support specifying wiring type and preset explicitly
					// &cli.StringFlag{
					// 	Name:        "wiring-type",
					// 	Aliases:     []string{"wt"},
					// 	Usage:       "use wiring diagram sample type (one of: " + strings.Join(sampleTypes, ", ") + ")",
					// 	Destination: &wiringGenType,
					// },
					// &cli.StringFlag{
					// 	Name:        "wiring-preset",
					// 	Aliases:     []string{"wp"},
					// 	Usage:       "use wiring diagram sample preset (one of: " + strings.Join(samplePresets, ", ") + ")",
					// 	Destination: &wiringGenPreset,
					// },
					// TODO support loading from existing config
					// &cli.StringFlag{
					// 	Name:        "config",
					// 	Aliases:     []string{"c"},
					// 	Usage:       "start from existing config `FILE`",
					// 	Destination: &config,
					// },
					// TODO support reset before init, is it really needed?
					// &cli.BoolFlag{
					// 	Name:        "reset",
					// 	Usage:       "reset configs in basedir before init if present",
					// 	Destination: &reset,
					// },
				}, mngr.Flags()...),
				Before: func(cCtx *cli.Context) error {
					return setupLogger(verbose, brief)
				},
				Action: func(cCtx *cli.Context) error {
					err := mngr.Init(basedir, cnc.Preset(preset), wiring, wiringGenType, wiringGenPreset)
					if err != nil {
						return errors.Wrap(err, "error initializing")
					}

					return errors.Wrap(mngr.Save(), "error saving")
				},
			},
			{
				Name:    "build",
				Aliases: []string{"b"},
				Usage:   "build bundles",
				Flags: []cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
					&cli.BoolFlag{
						Name:        "nopack",
						Usage:       "do not pack bundles",
						Destination: &nopack,
					},
					// TODO support reset before build
					// &cli.BoolFlag{
					// 	Name:        "reset",
					// 	Usage:       "reset bundles in basedir before building",
					// 	Destination: &reset,
					// },
				},
				Before: func(ctx *cli.Context) error {
					return setupLogger(verbose, brief)
				},
				Action: func(cCtx *cli.Context) error {
					err := mngr.Load(basedir)
					if err != nil {
						return errors.Wrap(err, "error loading")
					}

					return errors.Wrap(mngr.Build(!nopack), "error building bundles")
				},
			},
			{
				Name:    "pack",
				Aliases: []string{"p"},
				Usage:   "pack install bundles",
				Flags: []cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
				},
				Before: func(ctx *cli.Context) error {
					return setupLogger(verbose, brief)
				},
				Action: func(cCtx *cli.Context) error {
					err := mngr.Load(basedir)
					if err != nil {
						return errors.Wrap(err, "error loading")
					}

					return errors.Wrap(mngr.Pack(), "error packing bundles")
				},
			},
			{
				Name:  "dump",
				Usage: "load fabricator and dump hydrated config",
				Flags: []cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
				},
				Before: func(ctx *cli.Context) error {
					return setupLogger(verbose, brief)
				},
				Action: func(cCtx *cli.Context) error {
					err := mngr.Load(basedir)
					if err != nil {
						return errors.Wrap(err, "error loading")
					}

					return errors.Wrap(mngr.Dump(), "error dumping hydrated config")
				},
			},
			{
				Name:    "vlab",
				Aliases: []string{"v"},
				Usage:   "vlab management",
				Flags: []cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
				},
				Subcommands: []*cli.Command{
					{
						Name:    "start",
						Aliases: []string{"up"},
						Usage:   "start vlab",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							&cli.BoolFlag{
								Name:        "dry-run",
								Usage:       "dry run, prepare vms but not actually run them",
								Destination: &dryRun,
							},
							&cli.BoolFlag{
								Name:  "kill-stale-vms",
								Usage: "kill stale vms before starting",
							},
							&cli.BoolFlag{
								Name:  "compact",
								Usage: "run more lightweight vms, small risks",
							},
						},
						Before: func(ctx *cli.Context) error {
							return setupLogger(verbose, brief)
						},
						Action: func(cCtx *cli.Context) error {
							err := mngr.Load(basedir)
							if err != nil {
								return errors.Wrap(err, "error loading")
							}

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun)
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							killStaleVMs := cCtx.Bool("kill-stale-vms")

							return errors.Wrap(svc.StartServer(killStaleVMs, cCtx.Bool("compact")), "error starting vlab")
						},
					},
					{
						Name:    "ssh",
						Aliases: []string{"s"},
						Usage:   "ssh to vm, args passed to ssh command, will use jump host if needed",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							vmFlag,
						},
						Before: func(ctx *cli.Context) error {
							return setupLogger(verbose, brief)
						},
						Action: func(cCtx *cli.Context) error {
							err := mngr.Load(basedir)
							if err != nil {
								return errors.Wrap(err, "error loading")
							}

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun)
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							return errors.Wrap(svc.SSH(vm, cCtx.Args().Slice()), "error vm ssh")
						},
					},
					{
						Name:    "serial",
						Aliases: []string{"console", "c"},
						Usage:   "connect to vm serial console, no args for selector",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							vmFlag,
						},
						Before: func(ctx *cli.Context) error {
							return setupLogger(verbose, brief)
						},
						Action: func(cCtx *cli.Context) error {
							err := mngr.Load(basedir)
							if err != nil {
								return errors.Wrap(err, "error loading")
							}

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun)
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							return errors.Wrap(svc.Serial(vm), "error vm serial")
						},
					},
					{
						Name:    "details",
						Aliases: []string{"vms"},
						Usage:   "list all vms with interactive detailed info",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							vmFlag,
						},
						Before: func(ctx *cli.Context) error {
							return setupLogger(verbose, brief)
						},
						Action: func(cCtx *cli.Context) error {
							err := mngr.Load(basedir)
							if err != nil {
								return errors.Wrap(err, "error loading")
							}

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun)
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							return errors.Wrap(svc.List(), "error vm list")
						},
					},
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error("Failed", "err", err)
		os.Exit(1)
	}
}
