package main

import (
	_ "embed"
	"fmt"
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
	"go.githedgehog.com/fabricator/pkg/fab/vlab"
	"go.githedgehog.com/fabricator/pkg/fab/wiring"
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

	slog.Debug("\n" + motd)
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

	var basedir, fromConfig, preset, wiringGenType, wiringGenPreset string
	var wiringPath cli.StringSlice
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

	var dryRun, hydrate, nopack bool

	var vm string
	vmFlag := &cli.StringFlag{
		Name:        "vm",
		Usage:       "use vm `VM-NAME`, use `control` for control vm",
		Destination: &vm,
	}

	var hlabConfig, hlabKubeconfig string

	var wgChainControlLink bool
	var wgControlLinksCount, wgSpinesCount, wgFabricLinksCount, wgMCLAGLeafsCount, wgOrphanLeafsCount, wgMCLAGSessionLinks, wgMCLAGPeerLinks uint

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
				Name:  "init",
				Usage: "initialize fabricator with specified PRESET",
				Flags: append([]cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
					&cli.StringFlag{
						Name:        "config",
						Aliases:     []string{"c"},
						Usage:       "start from existing config `FILE`",
						Destination: &fromConfig,
					},
					&cli.StringFlag{
						Name:        "preset",
						Aliases:     []string{"p"},
						Usage:       "use preset `PRESET` (one of: " + strings.Join(presets, ", ") + ")",
						Required:    true,
						Destination: &preset,
					},
					&cli.StringSliceFlag{
						Name:        "wiring",
						Aliases:     []string{"w"},
						Usage:       "use wiring diagram from `FILE` (or dir), use '-' to read from stdin, use multiple times to merge",
						Destination: &wiringPath,
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
					// TODO support reset before init, is it really needed?
					// &cli.BoolFlag{
					// 	Name:        "reset",
					// 	Usage:       "reset configs in basedir before init if present",
					// 	Destination: &reset,
					// },
					&cli.BoolFlag{
						Name:        "auto-hydrate",
						Usage:       "automatically hydrate wiring diagram if needed (if some IPs/ASN/etc missing)",
						Value:       true,
						Destination: &hydrate,
					},
				}, mngr.Flags()...),
				Before: func(cCtx *cli.Context) error {
					return setupLogger(verbose, brief)
				},
				Action: func(cCtx *cli.Context) error {
					err := mngr.Init(basedir, fromConfig, cnc.Preset(preset), wiringPath.Value(), wiringGenType, wiringGenPreset, hydrate)
					if err != nil {
						return errors.Wrap(err, "error initializing")
					}

					return errors.Wrap(mngr.Save(), "error saving")
				},
			},
			{
				Name:  "build",
				Usage: "build bundles",
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
				Name:  "pack",
				Usage: "pack install bundles",
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
				Name:  "vlab",
				Usage: "fully virtual lab (VLAB) management",
				Flags: []cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
				},
				Subcommands: []*cli.Command{
					{
						Name:  "up",
						Usage: "bring up vlab vms",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							&cli.StringFlag{
								Name:    "config",
								Usage:   "use vlab config `FILE`",
								Aliases: []string{"c"},
							},
							&cli.BoolFlag{
								Name:        "dry-run",
								Usage:       "dry run, prepare vms but not actually run them",
								Destination: &dryRun,
							},
							&cli.BoolFlag{
								Name:  "kill-stale-vms",
								Usage: "kill stale vms before starting",
							},
							&cli.StringFlag{
								Name:  "vm-size",
								Usage: "run with one of the predefined sizes (one of: " + strings.Join(vlab.VM_SIZES, ", ") + ")",
							},
							&cli.BoolFlag{
								Name:  "install-complete",
								Usage: "run installer and complete vlab (for testing)",
							},
							&cli.StringFlag{
								Name:  "run-complete",
								Usage: "run installer, run provided script and than complete vlab (for testing)",
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

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun, cCtx.String("config"), cCtx.String("vm-size"))
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							killStaleVMs := cCtx.Bool("kill-stale-vms")

							return errors.Wrap(svc.StartServer(killStaleVMs, cCtx.Bool("install-complete"), cCtx.String("run-complete")), "error starting vlab")
						},
					},
					{
						Name:  "ssh",
						Usage: "ssh to vm, args passed to ssh command, will use jump host if needed",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							vmFlag,
							&cli.StringFlag{
								Name:    "config",
								Usage:   "use vlab config `FILE`",
								Aliases: []string{"c"},
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

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun, cCtx.String("config"), "")
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							return errors.Wrap(svc.SSH(vm, cCtx.Args().Slice()), "error vm ssh")
						},
					},
					{
						Name:  "serial",
						Usage: "connect to vm serial console, no args for selector",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							vmFlag,
							&cli.StringFlag{
								Name:    "config",
								Usage:   "use vlab config `FILE`",
								Aliases: []string{"c"},
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

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun, cCtx.String("config"), "")
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							return errors.Wrap(svc.Serial(vm), "error vm serial")
						},
					},
					{
						Name:  "details",
						Usage: "list all vms with interactive detailed info",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							vmFlag,
							&cli.StringFlag{
								Name:    "config",
								Usage:   "use vlab config `FILE`",
								Aliases: []string{"c"},
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

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun, cCtx.String("config"), "")
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							return errors.Wrap(svc.List(), "error vm list")
						},
					},
					{
						Name:  "vfio-pci-bind",
						Usage: "bind all device used in vlab to vfio-pci driver for pci passthrough",
						Flags: []cli.Flag{
							basedirFlag,
							verboseFlag,
							briefFlag,
							&cli.StringFlag{
								Name:    "config",
								Usage:   "use vlab config `FILE`",
								Aliases: []string{"c"},
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

							svc, err := fab.LoadVLAB(basedir, mngr, dryRun, cCtx.String("config"), "")
							if err != nil {
								return errors.Wrap(err, "error loading vlab")
							}

							return errors.Wrap(svc.VFIOPCIBindAll(), "error binding to vfio-pci")
						},
					},
				},
			},
			{
				Name:  "wiring",
				Usage: "tools for working with wiring diagram",
				Flags: []cli.Flag{
					basedirFlag,
					verboseFlag,
					briefFlag,
				},
				Subcommands: []*cli.Command{
					{
						Name:  "sample",
						Usage: "sample wiring diagram",
						Flags: []cli.Flag{
							verboseFlag,
							briefFlag,
							// TODO
						},
						Subcommands: []*cli.Command{
							{
								Name:  "spine-leaf",
								Usage: "sample wiring diagram for spine-leaf topology",
								Flags: []cli.Flag{
									verboseFlag,
									briefFlag,
									&cli.BoolFlag{
										Category:    "spine-leaf",
										Name:        "chain-control-link",
										Usage:       "chain control links instead of all switches directly connected to control node",
										Destination: &wgChainControlLink,
										Value:       false,
									},
									&cli.UintFlag{
										Category:    "spine-leaf",
										Name:        "control-links-count",
										Usage:       "number of control links",
										Destination: &wgControlLinksCount,
										Value:       2,
									},
									&cli.UintFlag{
										Category:    "spine-leaf",
										Name:        "spines-count",
										Usage:       "number of spines",
										Destination: &wgSpinesCount,
										Value:       2,
									},
									&cli.UintFlag{
										Category:    "spine-leaf",
										Name:        "fabric-links-count",
										Usage:       "number of fabric links",
										Destination: &wgFabricLinksCount,
										Value:       2,
									},
									&cli.UintFlag{
										Category:    "spine-leaf",
										Name:        "mclag-leafs-count",
										Usage:       "number of mclag leafs (should be even)",
										Destination: &wgMCLAGLeafsCount,
										Value:       2,
									},
									&cli.UintFlag{
										Category:    "spine-leaf",
										Name:        "orphan-leafs-count",
										Usage:       "number of orphan leafs",
										Destination: &wgOrphanLeafsCount,
										Value:       1,
									},
									&cli.UintFlag{
										Category:    "spine-leaf",
										Name:        "mclag-session-links",
										Usage:       "number of mclag session links",
										Destination: &wgMCLAGSessionLinks,
										Value:       2,
									},
									&cli.UintFlag{
										Category:    "spine-leaf",
										Name:        "mclag-peer-links",
										Usage:       "number of mclag peer links",
										Destination: &wgMCLAGPeerLinks,
										Value:       2,
									},
								},
								Before: func(ctx *cli.Context) error {
									return setupLogger(verbose, brief)
								},
								Action: func(cCtx *cli.Context) error {
									data, err := (&wiring.SpineLeafBuilder{
										ChainControlLink:  wgChainControlLink,
										ControlLinksCount: uint8(wgControlLinksCount),
										SpinesCount:       uint8(wgSpinesCount),
										FabricLinksCount:  uint8(wgFabricLinksCount),
										MCLAGLeafsCount:   uint8(wgMCLAGLeafsCount),
										OrphanLeafsCount:  uint8(wgOrphanLeafsCount),
										MCLAGSessionLinks: uint8(wgMCLAGSessionLinks),
										MCLAGPeerLinks:    uint8(wgMCLAGPeerLinks),
									}).Build()
									if err != nil {
										return errors.Wrap(err, "error building sample")
									}

									return errors.Wrapf(data.Write(os.Stdout), "error writing sample")
								},
							},
						},
					},
					{
						Name:  "hydrate",
						Usage: "hydrate wiring diagram",
						Flags: []cli.Flag{
							verboseFlag,
							briefFlag,
							&cli.StringFlag{
								Name:    "wiring",
								Aliases: []string{"w"},
								Usage:   "use wiring `FILE`",
							},
						},
						Before: func(ctx *cli.Context) error {
							return setupLogger(verbose, brief)
						},
						Action: func(cCtx *cli.Context) error {
							err := wiring.HydratePath(cCtx.String("wiring"))
							if err != nil {
								return errors.Wrap(err, "error hydrating")
							}

							return nil
						},
					},
					{
						Name:  "graph",
						Usage: "generate dot graph from wiring diagram (experimental)",
						Flags: []cli.Flag{
							verboseFlag,
							briefFlag,
							&cli.StringFlag{
								Name:    "wiring",
								Aliases: []string{"w"},
								Usage:   "use wiring `FILE`",
							},
						},
						Before: func(ctx *cli.Context) error {
							return setupLogger(verbose, brief)
						},
						Action: func(cCtx *cli.Context) error {
							data, err := wiring.Visualize(cCtx.String("wiring"))
							if err != nil {
								return errors.Wrap(err, "error visualizing")
							}

							fmt.Println(data)

							return nil
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
