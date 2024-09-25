package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/hhfab"
	"go.githedgehog.com/fabricator/pkg/version"
)

const (
	FlagCatGlobal                 = "Global options:"
	FlagNameRegistryRepo          = "registry-repo"
	FlagNameRegistryPrefix        = "registry-prefix"
	FlagNameConfig                = "config"
	FlagNameWiring                = "wiring"
	FlagCatGenConfig              = "Generate initial config:"
	FlagNameDefaultPasswordHash   = "default-password-hash"
	FlagNameDefaultAuthorizedKeys = "default-authorized-keys"
	FlagNameTLSSAN                = "tls-san"
	FlagNameDev                   = "dev"
	FlagNameAirgap                = "airgap"
	FlagNameFabricMode            = "fabric-mode"
	FlagNameCount                 = "count"
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

	fabricModes := []string{}
	for _, m := range meta.FabricModes {
		fabricModes = append(fabricModes, string(m))
	}

	hydrateModes := []string{}
	for _, m := range hhfab.HydrateModes {
		hydrateModes = append(hydrateModes, string(m))
	}

	var hydrateMode string
	hMode := &cli.StringFlag{
		Name:        "hydrate-mode",
		Aliases:     []string{"hm"},
		Usage:       "set hydrate mode: one of " + strings.Join(hydrateModes, ", "),
		Value:       string(hhfab.HydrateModeIfNotPresent),
		Destination: &hydrateMode,
	}

	var wgSpinesCount, wgFabricLinksCount, wgMCLAGLeafsCount, wgOrphanLeafsCount, wgMCLAGSessionLinks, wgMCLAGPeerLinks, wgVPCLoopbacks uint
	var wgESLAGLeafGroups string
	var wgExternal bool
	var wgMCLAGServers, wgESLAGServers, wgUnbundledServers, wgBundledServers uint
	vlabWiringGenFlags := []cli.Flag{
		&cli.UintFlag{
			Name:        "spines-count",
			Usage:       "number of spines if fabric mode is spine-leaf",
			Destination: &wgSpinesCount,
		},
		&cli.UintFlag{
			Name:        "fabric-links-count",
			Usage:       "number of fabric links if fabric mode is spine-leaf",
			Destination: &wgFabricLinksCount,
		},
		&cli.UintFlag{
			Name:        "mclag-leafs-count",
			Usage:       "number of mclag leafs (should be even)",
			Destination: &wgMCLAGLeafsCount,
		},
		&cli.StringFlag{
			Name:        "eslag-leaf-groups",
			Usage:       "eslag leaf groups (comma separated list of number of ESLAG switches in each group, should be 2-4 per group, e.g. 2,4,2 for 3 groups with 2, 4 and 2 switches)",
			Destination: &wgESLAGLeafGroups,
		},
		&cli.UintFlag{
			Name:        "orphan-leafs-count",
			Usage:       "number of orphan leafs",
			Destination: &wgOrphanLeafsCount,
		},
		&cli.UintFlag{
			Name:        "mclag-session-links",
			Usage:       "number of mclag session links for each mclag leaf",
			Destination: &wgMCLAGSessionLinks,
		},
		&cli.UintFlag{
			Name:        "mclag-peer-links",
			Usage:       "number of mclag peer links for each mclag leaf",
			Destination: &wgMCLAGPeerLinks,
		},
		&cli.UintFlag{
			Name:        "vpc-loopbacks",
			Usage:       "number of vpc loopbacks for each switch",
			Destination: &wgVPCLoopbacks,
		},
		&cli.BoolFlag{
			Name:        "external",
			Usage:       "include virtual external switch",
			Destination: &wgExternal,
		},
		&cli.UintFlag{
			Name:        "mclag-servers",
			Usage:       "number of MCLAG servers to generate for MCLAG switches",
			Destination: &wgMCLAGServers,
			Value:       2,
		},
		&cli.UintFlag{
			Name:        "eslag-servers",
			Usage:       "number of ESLAG servers to generate for ESLAG switches",
			Destination: &wgESLAGServers,
			Value:       2,
		},
		&cli.UintFlag{
			Name:        "unbundled-servers",
			Usage:       "number of unbundled servers to generate for switches (only for one of the first switch in the redundancy group or orphan switch)",
			Destination: &wgUnbundledServers,
			Value:       1,
		},
		&cli.UintFlag{
			Name:        "bundled-servers",
			Usage:       "number of bundled servers to generate for switches (only for one of the second switch in the redundancy group or orphan switch)",
			Destination: &wgBundledServers,
			Value:       1,
		},
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
					&cli.StringFlag{
						Name:    FlagNameConfig,
						Aliases: []string{"c"},
						Usage:   "use existing config file `PATH`",
						EnvVars: []string{"HHFAB_CONFIG"},
					},
					&cli.StringSliceFlag{
						Name:    FlagNameWiring,
						Aliases: []string{"w"},
						Usage:   "include wiring diagram `FILE` with ext .yaml (any Fabric API objects)",
					},
					&cli.StringFlag{
						Name:    FlagNameFabricMode,
						Aliases: []string{"mode", "m"},
						Usage:   "set fabric mode: one of " + strings.Join(fabricModes, ", "),
						Value:   string(meta.FabricModeSpineLeaf),
						Action: func(_ *cli.Context, mode string) error {
							if !slices.Contains(fabricModes, mode) {
								return fmt.Errorf("invalid fabric mode %q", mode) //nolint:goerr113
							}

							return nil
						},
					},
					&cli.StringSliceFlag{
						Name:    FlagNameTLSSAN,
						Aliases: []string{"tls"},
						Usage:   "IPs and DNS names that will be used to access API",
						EnvVars: []string{"HHFAB_TLS_SAN"},
					},
					&cli.StringSliceFlag{
						Name:    FlagNameDefaultAuthorizedKeys,
						Aliases: []string{"keys"},
						Usage:   "default authorized `KEYS` for control and switch users",
						EnvVars: []string{"HHFAB_AUTH_KEYS"},
					},
					&cli.StringFlag{
						Name:    FlagNameDefaultPasswordHash,
						Aliases: []string{"passwd"},
						Usage:   "default password `HASH` for control and switch users",
						EnvVars: []string{"HHFAB_PASSWD_HASH"},
					},
					&cli.BoolFlag{
						Name:    FlagNameDev,
						Usage:   "use default credentials (unsafe)",
						EnvVars: []string{"HHFAB_DEV"},
					},
					&cli.BoolFlag{
						Name:    FlagNameAirgap,
						Usage:   "airgap mode (no internet access required for installation and operations)",
						EnvVars: []string{"HHFAB_AIRGAP"},
						Value:   true,
					},
				),
				Before: before(),
				Action: func(c *cli.Context) error {
					if err := hhfab.Init(ctx, hhfab.InitConfig{
						WorkDir:      workDir,
						CacheDir:     cacheDir,
						Repo:         c.String(FlagNameRegistryRepo),
						Prefix:       c.String(FlagNameRegistryPrefix),
						ImportConfig: c.String(FlagNameConfig),
						Wiring:       c.StringSlice(FlagNameWiring),
						InitConfigInput: fab.InitConfigInput{
							FabricMode:            meta.FabricMode(c.String(FlagNameFabricMode)),
							TLSSAN:                c.StringSlice(FlagNameTLSSAN),
							DefaultPasswordHash:   c.String(FlagNameDefaultPasswordHash),
							DefaultAuthorizedKeys: c.StringSlice(FlagNameDefaultAuthorizedKeys),
							Dev:                   c.Bool(FlagNameDev),
							Airgap:                c.Bool(FlagNameAirgap),
						},
					}); err != nil {
						return fmt.Errorf("initializing: %w", err)
					}

					return nil
				},
			},
			{
				Name:   "validate",
				Usage:  "validate config and included wiring files",
				Flags:  append(defaultFlags, hMode),
				Before: before(),
				Action: func(_ *cli.Context) error {
					if err := hhfab.Validate(ctx, workDir, cacheDir, hhfab.HydrateMode(hydrateMode)); err != nil {
						return fmt.Errorf("validating: %w", err)
					}

					return nil
				},
			},
			{
				Name:  "sample",
				Usage: "generate sample wiring diagram",
				Subcommands: []*cli.Command{
					{
						Name:    "spine-leaf",
						Aliases: []string{"sl"},
						Usage:   "generate sample spine-leaf wiring diagram",
						Flags:   defaultFlags,
						Before:  before(),
						Action: func(_ *cli.Context) error {
							panic("not implemented")
						},
					},
					{
						Name:    "collapsed-core",
						Aliases: []string{"cc"},
						Usage:   "generate sample collapsed-core wiring diagram",
						Flags:   defaultFlags,
						Before:  before(),
						Action: func(_ *cli.Context) error {
							panic("not implemented")
						},
					},
				},
			},
			{
				Name:   "build",
				Usage:  "build installers",
				Flags:  append(defaultFlags, hMode),
				Before: before(),
				Action: func(_ *cli.Context) error {
					if err := hhfab.Build(ctx, workDir, cacheDir, hhfab.HydrateMode(hydrateMode)); err != nil {
						return fmt.Errorf("building: %w", err)
					}

					return nil
				},
			},
			{
				Name: "vlab",
				Subcommands: []*cli.Command{
					{
						Name:    "generate",
						Aliases: []string{"gen"},
						Usage:   "generate VLAB wiring diagram",
						Flags:   append(defaultFlags, vlabWiringGenFlags...),
						Before:  before(),
						Action: func(_ *cli.Context) error {
							builder := hhfab.VLABBuilder{
								SpinesCount:       uint8(wgSpinesCount),      //nolint:gosec
								FabricLinksCount:  uint8(wgFabricLinksCount), //nolint:gosec
								MCLAGLeafsCount:   uint8(wgMCLAGLeafsCount),  //nolint:gosec
								ESLAGLeafGroups:   wgESLAGLeafGroups,
								OrphanLeafsCount:  uint8(wgOrphanLeafsCount),  //nolint:gosec
								MCLAGSessionLinks: uint8(wgMCLAGSessionLinks), //nolint:gosec
								MCLAGPeerLinks:    uint8(wgMCLAGPeerLinks),    //nolint:gosec
								VPCLoopbacks:      uint8(wgVPCLoopbacks),      //nolint:gosec
								MCLAGServers:      uint8(wgMCLAGServers),      //nolint:gosec
								ESLAGServers:      uint8(wgESLAGServers),      //nolint:gosec
								UnbundledServers:  uint8(wgUnbundledServers),  //nolint:gosec
								BundledServers:    uint8(wgBundledServers),    //nolint:gosec
							}

							if err := hhfab.VLABGenerate(ctx, workDir, cacheDir, builder, hhfab.DefaultVLABGeneratedFile); err != nil {
								return fmt.Errorf("generating VLAB wiring diagram: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "up",
						Usage:  "run VLAB",
						Flags:  append(defaultFlags, hMode),
						Before: before(),
						Action: func(_ *cli.Context) error {
							if err := hhfab.VLABUp(ctx, workDir, cacheDir, hhfab.HydrateMode(hydrateMode)); err != nil {
								return fmt.Errorf("running VLAB: %w", err)
							}

							return nil
						},
					},
				},
			},
			{
				Name:   "_helpers",
				Usage:  "shouldn't be used directly, will be called by hhfab automatically",
				Hidden: true,
				Subcommands: []*cli.Command{
					{
						Name:  "setup-taps",
						Usage: "setup tap devices and a bridge for VLAB",
						Flags: append(defaultFlags,
							&cli.IntFlag{
								Name:     FlagNameCount,
								Usage:    "number of tap devices to prepare (or cleanup if count is 0)",
								Required: true,
								Action: func(_ *cli.Context, v int) error {
									if v < 0 {
										return fmt.Errorf("count must be zero or positive") //nolint:goerr113
									}

									if v > 100 {
										return fmt.Errorf("count must be less than 100") //nolint:goerr113
									}

									return nil
								},
							},
						),
						Before: before(),
						Action: func(c *cli.Context) error {
							if err := hhfab.PrepareTaps(ctx, c.Int(FlagNameCount)); err != nil {
								return fmt.Errorf("preparing taps: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "vfio-pci-bind",
						Usage:  "bind all device used in VLAB to vfio-pci driver for PCI passthrough",
						Flags:  defaultFlags,
						Before: before(),
						Action: func(c *cli.Context) error {
							if err := hhfab.PreparePassthrough(ctx, c.Args().Slice()); err != nil {
								return fmt.Errorf("preparing passthrough: %w", err)
							}

							return nil
						},
					},
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
