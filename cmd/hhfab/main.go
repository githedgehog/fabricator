// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/go-logr/logr"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	slogmulti "github.com/samber/slog-multi"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/hhfab"
	"go.githedgehog.com/fabricator/pkg/version"
	"gopkg.in/natefinch/lumberjack.v2"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	FlagCatGlobal                 = "Global options:"
	FlagNameRegistryRepo          = "registry-repo"
	FlagNameRegistryPrefix        = "registry-prefix"
	FlagNameConfig                = "config"
	FlagNameForce                 = "force"
	FlagNameWiring                = "wiring"
	FlagNameImportHostUpstream    = "import-host-upstream"
	FlagCatGenConfig              = "Generate initial config (ignored when importing):"
	FlagNameDefaultPasswordHash   = "default-password-hash"
	FlagNameDefaultAuthorizedKeys = "default-authorized-keys"
	FlagNameTLSSAN                = "tls-san"
	FlagNameDev                   = "dev"
	FlagIncludeONIE               = "include-onie"
	FlagControlNodeMgmtLink       = "control-node-mgmt-link"
	FlagNameFabricMode            = "fabric-mode"
	FlagNameCount                 = "count"
	FlagNameKillStale             = "kill-stale"
	FlagNameControlsRestricted    = "controls-restricted"
	FlagNameServersRestricted     = "servers-restricted"
	FlagNameReCreate              = "recreate"
	FlagNameBuildMode             = "build-mode"
	FlagNameControlUpgrade        = "control-upgrade"
	FlagNameFailFast              = "fail-fast"
	FlagNameReady                 = "ready"
)

func main() {
	if err := Run(context.Background()); err != nil {
		// TODO what if slog isn't initialized yet?
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func Run(ctx context.Context) error {
	var verbose, brief, yes bool
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
	yesFlag := &cli.BoolFlag{
		Name:        "yes",
		Aliases:     []string{"y"},
		Usage:       "assume yes",
		Destination: &yes,
	}
	yesCheck := func(_ *cli.Context) error {
		if !yes {
			return cli.Exit("\033[31mWARNING:\033[0m Potentially dangerous operation. Please confirm with --yes if you're sure.", 1)
		}

		return nil
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
	var wgMCLAGServers, wgESLAGServers, wgUnbundledServers, wgBundledServers uint
	var wgNoSwitches bool
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
		&cli.BoolFlag{
			Name:        "no-switches",
			Usage:       "do not generate any switches",
			Destination: &wgNoSwitches,
		},
	}

	var accessName string
	accessNameFlag := &cli.StringFlag{
		Name:        "name",
		Aliases:     []string{"n"},
		Usage:       "name of the VM or HW to access",
		Destination: &accessName,
	}

	before := func(quiet bool) cli.BeforeFunc {
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

			logFile := &lumberjack.Logger{
				Filename:   "/var/log/hhfab.log",
				MaxSize:    5, // MB
				MaxBackups: 4,
				MaxAge:     30, // days
				Compress:   true,
				FileMode:   0o644,
			}

			handler := slogmulti.Fanout(
				tint.NewHandler(logW, &tint.Options{
					Level:      logLevel,
					TimeFormat: time.TimeOnly,
					NoColor:    !isatty.IsTerminal(logW.Fd()),
				}),
				slog.NewTextHandler(logFile, &slog.HandlerOptions{
					Level: slog.LevelDebug,
				}),
			)

			logger := slog.New(handler)
			slog.SetDefault(logger)
			ctrl.SetLogger(logr.FromSlogHandler(handler))
			klog.SetSlogLogger(logger)

			if quiet {
				return nil
			}

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

	onReadyCommands := []string{}
	for _, cmd := range hhfab.AllOnReady {
		onReadyCommands = append(onReadyCommands, string(cmd))
	}

	buildModes := []string{}
	for _, m := range recipe.BuildModes {
		buildModes = append(buildModes, string(m))
	}

	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}
	app := &cli.App{
		Name:  "hhfab",
		Usage: "hedgehog fabricator - build, install and run hedgehog",
		Description: `Create Hedgehog configs, wiring diagram, build an installer and optionally run the virtual lab (VLAB):
	1.  Initialize working dir by running 'hhfab init', to use default creds use '--dev' (unsafe)
	2a. If building for physical environment, use 'hhfab sample' to generate sample wiring diagram
	2b. If building for VLAB, use 'hhfab vlab gen' to generate VLAB wiring diagram
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
					&cli.BoolFlag{
						Name:    FlagNameForce,
						Aliases: []string{"f"},
						Usage:   "overwrite existing files",
						EnvVars: []string{"HHFAB_FORCE"},
					},
					&cli.StringSliceFlag{
						Name:    FlagNameWiring,
						Aliases: []string{"w"},
						Usage:   "include wiring diagram `FILE` with ext .yaml (any Fabric API objects)",
					},
					&cli.StringFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNameFabricMode,
						Aliases:  []string{"mode", "m"},
						Usage:    "set fabric mode: one of " + strings.Join(fabricModes, ", "),
						Value:    string(meta.FabricModeSpineLeaf),
						Action: func(_ *cli.Context, mode string) error {
							if !slices.Contains(fabricModes, mode) {
								return fmt.Errorf("invalid fabric mode %q", mode) //nolint:goerr113
							}

							return nil
						},
					},
					&cli.StringSliceFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNameTLSSAN,
						Aliases:  []string{"tls"},
						Usage:    "IPs and DNS names that will be used to access API",
						EnvVars:  []string{"HHFAB_TLS_SAN"},
					},
					&cli.StringSliceFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNameDefaultAuthorizedKeys,
						Aliases:  []string{"keys"},
						Usage:    "default authorized `KEYS` for control and switch users",
						EnvVars:  []string{"HHFAB_AUTH_KEYS"},
					},
					&cli.StringFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNameDefaultPasswordHash,
						Aliases:  []string{"passwd"},
						Usage:    "default password `HASH` for control and switch users",
						EnvVars:  []string{"HHFAB_PASSWD_HASH"},
					},
					&cli.BoolFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNameDev,
						Usage:    "use default dev credentials (unsafe)",
						EnvVars:  []string{"HHFAB_DEV"},
					},
					&cli.BoolFlag{
						Category: FlagCatGenConfig,
						Name:     FlagIncludeONIE,
						Hidden:   true,
						Usage:    "include tested ONIE updaters for supported switches in the build",
						EnvVars:  []string{"HHFAB_INCLUDE_ONIE"},
					},
					&cli.BoolFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNameImportHostUpstream,
						Hidden:   true,
						Usage:    "import host repo/prefix and creds from docker config as an upstream registry mode and config (creds will be stored plain text)",
						EnvVars:  []string{"HHFAB_IMPORT_HOST_UPSTREAM"},
					},
					&cli.StringFlag{
						Category: FlagCatGenConfig,
						Name:     FlagControlNodeMgmtLink,
						Hidden:   true,
						Usage:    "control node management link (for pci passthrough for VLAB-only)",
						EnvVars:  []string{"HHFAB_CONTROL_NODE_MGMT_LINK"},
					},
				),
				Before: before(false),
				Action: func(c *cli.Context) error {
					if err := hhfab.Init(ctx, hhfab.InitConfig{
						WorkDir:            workDir,
						CacheDir:           cacheDir,
						Repo:               c.String(FlagNameRegistryRepo),
						Prefix:             c.String(FlagNameRegistryPrefix),
						ImportConfig:       c.String(FlagNameConfig),
						Force:              c.Bool(FlagNameForce),
						Wiring:             c.StringSlice(FlagNameWiring),
						ImportHostUpstream: c.Bool(FlagNameImportHostUpstream),
						InitConfigInput: fab.InitConfigInput{
							FabricMode:                meta.FabricMode(c.String(FlagNameFabricMode)),
							TLSSAN:                    c.StringSlice(FlagNameTLSSAN),
							DefaultPasswordHash:       c.String(FlagNameDefaultPasswordHash),
							DefaultAuthorizedKeys:     c.StringSlice(FlagNameDefaultAuthorizedKeys),
							Dev:                       c.Bool(FlagNameDev),
							IncludeONIE:               c.Bool(FlagIncludeONIE),
							ControlNodeManagementLink: c.String(FlagControlNodeMgmtLink),
						},
					}); err != nil {
						return fmt.Errorf("initializing: %w", err)
					}

					return nil
				},
			},
			{
				Name:   "validate",
				Usage:  "validate config and wiring files",
				Flags:  append(defaultFlags, hMode),
				Before: before(false),
				Action: func(_ *cli.Context) error {
					if err := hhfab.Validate(ctx, workDir, cacheDir, hhfab.HydrateMode(hydrateMode)); err != nil {
						return fmt.Errorf("validating: %w", err)
					}

					return nil
				},
			},
			{
				Name:   "versions",
				Usage:  "print versions of all components",
				Flags:  append(defaultFlags, hMode),
				Before: before(false),
				Action: func(_ *cli.Context) error {
					if err := hhfab.Versions(ctx, workDir, cacheDir, hhfab.HydrateMode(hydrateMode)); err != nil {
						return fmt.Errorf("printing versions: %w", err)
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
						Before:  before(false),
						Action: func(_ *cli.Context) error {
							return fmt.Errorf("not available") //nolint:goerr113
						},
					},
					{
						Name:    "collapsed-core",
						Aliases: []string{"cc"},
						Usage:   "generate sample collapsed-core wiring diagram",
						Flags:   defaultFlags,
						Before:  before(false),
						Action: func(_ *cli.Context) error {
							return fmt.Errorf("not available") //nolint:goerr113
						},
					},
				},
			},
			{
				Name:  "build",
				Usage: "build installers",
				Flags: append(defaultFlags, hMode,
					&cli.StringFlag{
						Name:    FlagNameBuildMode,
						Aliases: []string{"mode", "m"},
						Usage:   "build mode: one of " + strings.Join(buildModes, ", "),
						EnvVars: []string{"HHFAB_BUILD_MODE"},
						Value:   string(recipe.BuildModeISO),
					},
				),
				Before: before(false),
				Action: func(c *cli.Context) error {
					if err := hhfab.Build(ctx, workDir, cacheDir, hhfab.BuildOpts{
						HydrateMode: hhfab.HydrateMode(hydrateMode),
						BuildMode:   recipe.BuildMode(c.String(FlagNameBuildMode)),
					}); err != nil {
						return fmt.Errorf("building: %w", err)
					}

					return nil
				},
			},
			{
				Name:  "vlab",
				Usage: "operate Virtual Lab",
				Subcommands: []*cli.Command{
					{
						Name:    "generate",
						Aliases: []string{"gen"},
						Usage:   "generate VLAB wiring diagram",
						Flags:   append(defaultFlags, vlabWiringGenFlags...),
						Before:  before(false),
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
								NoSwitches:        wgNoSwitches,
							}

							if err := hhfab.VLABGenerate(ctx, workDir, cacheDir, builder, hhfab.DefaultVLABGeneratedFile); err != nil {
								return fmt.Errorf("generating VLAB wiring diagram: %w", err)
							}

							return nil
						},
					},
					{
						Name:  "up",
						Usage: "run VLAB",
						Flags: append(defaultFlags, hMode,
							&cli.BoolFlag{
								Name:    FlagNameReCreate,
								Aliases: []string{"f"},
								Usage:   "recreate VLAB (destroy and create new config and VMs)",
							},
							&cli.BoolFlag{
								Name:    FlagNameKillStale,
								Usage:   "kill stale VMs automatically based on VM UUIDs used",
								EnvVars: []string{"HHFAB_KILL_STALE"},
								Value:   true,
							},
							&cli.BoolFlag{
								Name:    FlagNameControlsRestricted,
								Usage:   "restrict control nodes from having access to the host (effectively access to internet)",
								EnvVars: []string{"HHFAB_CONTROLS_RESTRICTED"},
								Value:   true,
							},
							&cli.BoolFlag{
								Name:    FlagNameServersRestricted,
								Usage:   "restrict server nodes from having access to the host (effectively access to internet)",
								EnvVars: []string{"HHFAB_SERVERS_RESTRICTED"},
								Value:   true,
							},
							&cli.StringFlag{
								Name:    FlagNameBuildMode,
								Aliases: []string{"mode", "m"},
								Usage:   "build mode: one of " + strings.Join(buildModes, ", "),
								EnvVars: []string{"HHFAB_BUILD_MODE"},
								Value:   string(recipe.BuildModeISO),
							},
							&cli.BoolFlag{
								Name:    FlagNameControlUpgrade,
								Aliases: []string{"upgrade"},
								Usage:   "force upgrade control node(s), expected to use after initial successful installation",
								EnvVars: []string{"HHFAB_CONTROL_UPGRADE"},
								Value:   false,
							},
							&cli.BoolFlag{
								Name:  FlagNameFailFast,
								Usage: "exit on first error",
								Value: true,
							},
							&cli.StringSliceFlag{
								Name:    FlagNameReady,
								Aliases: []string{"r"},
								Usage:   "run commands on all VMs ready (one of: " + strings.Join(onReadyCommands, ", ") + ")",
							},
						),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.VLABUp(ctx, workDir, cacheDir, hhfab.VLABUpOpts{
								HydrateMode: hhfab.HydrateMode(hydrateMode),
								ReCreate:    c.Bool(FlagNameReCreate),
								BuildMode:   recipe.BuildMode(c.String(FlagNameBuildMode)),
								VLABRunOpts: hhfab.VLABRunOpts{
									KillStale:          c.Bool(FlagNameKillStale),
									ControlsRestricted: c.Bool(FlagNameControlsRestricted),
									ServersRestricted:  c.Bool(FlagNameServersRestricted),
									BuildMode:          recipe.BuildMode(c.String(FlagNameBuildMode)),
									ControlUpgrade:     c.Bool(FlagNameControlUpgrade),
									FailFast:           c.Bool(FlagNameFailFast),
									OnReady:            c.StringSlice(FlagNameReady),
								},
							}); err != nil {
								return fmt.Errorf("running VLAB: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "ssh",
						Usage:  "ssh to a VLAB VM or HW if supported",
						Flags:  append(defaultFlags, accessNameFlag),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABSSH(ctx, workDir, cacheDir, accessName, c.Args().Slice()); err != nil {
								return fmt.Errorf("ssh: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "serial",
						Usage:  "get serial console of a VLAB VM or HW if supported",
						Flags:  append(defaultFlags, accessNameFlag),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABSerial(ctx, workDir, cacheDir, accessName, c.Args().Slice()); err != nil {
								return fmt.Errorf("serial: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "seriallog",
						Usage:  "get serial console log of a VLAB VM or HW if supported",
						Flags:  append(defaultFlags, accessNameFlag),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABSerialLog(ctx, workDir, cacheDir, accessName, c.Args().Slice()); err != nil {
								return fmt.Errorf("serial log: %w", err)
							}

							return nil
						},
					},
					{
						Name:    "setup-vpcs",
						Aliases: []string{"vpcs"},
						Usage:   "setup VPCs and VPCAttachments for all servers and configure networking on them",
						Flags: append(defaultFlags, accessNameFlag,
							&cli.BoolFlag{
								Name:    "wait-switches-ready",
								Aliases: []string{"wait"},
								Usage:   "wait for switches to be ready before and after configuring VPCs and VPCAttachments",
								Value:   true,
							},
							&cli.BoolFlag{
								Name:    "force-cleanup",
								Aliases: []string{"f"},
								Usage:   "start with removing all existing VPCs and VPCAttachments",
							},
							&cli.StringFlag{
								Name:  "vlanns",
								Usage: "VLAN namespace for VPCs",
								Value: "default",
							},
							&cli.StringFlag{
								Name:  "ipns",
								Usage: "IPv4 namespace for VPCs",
								Value: "default",
							},
							&cli.IntFlag{
								Name:    "servers-per-subnet",
								Aliases: []string{"servers"},
								Usage:   "number of servers per subnet",
								Value:   1,
							},
							&cli.IntFlag{
								Name:    "subnets-per-vpc",
								Aliases: []string{"subnets"},
								Usage:   "number of subnets per VPC",
								Value:   1,
							},
							&cli.StringSliceFlag{
								Name:    "dns-servers",
								Aliases: []string{"dns"},
								Usage:   "DNS servers for VPCs advertised by DHCP",
							},
							&cli.StringSliceFlag{
								Name:    "time-servers",
								Aliases: []string{"ntp"},
								Usage:   "Time servers for VPCs advertised by DHCP",
							},
							&cli.UintFlag{
								Name:    "interface-mtu",
								Aliases: []string{"mtu"},
								Usage:   "interface MTU for VPCs advertised by DHCP",
							},
						),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABSetupVPCs(ctx, workDir, cacheDir, hhfab.SetupVPCsOpts{
								WaitSwitchesReady: c.Bool("wait-switches-ready"),
								ForceCleanup:      c.Bool("force-cleanup"),
								VLANNamespace:     c.String("vlanns"),
								IPv4Namespace:     c.String("ipns"),
								ServersPerSubnet:  c.Int("servers-per-subnet"),
								SubnetsPerVPC:     c.Int("subnets-per-vpc"),
								DNSServers:        c.StringSlice("dns-servers"),
								TimeServers:       c.StringSlice("time-servers"),
								InterfaceMTU:      uint16(c.Uint("interface-mtu")), //nolint:gosec
							}); err != nil {
								return fmt.Errorf("setup-vpcs: %w", err)
							}

							return nil
						},
					},
					{
						Name:    "setup-peerings",
						Aliases: []string{"peers"},
						Usage:   "setup VPC and External Peerings per requests (remove all if empty)",
						UsageText: strings.TrimSpace(strings.ReplaceAll(`
							Setup test scenario with VPC/External Peerings by specifying requests in the format described below.

							Example command:

							$ hhfab vlab setup-peerings 1+2 2+4:r=border 1~as5835 2~as5835:subnets=sub1,sub2:prefixes=0.0.0.0/0,22.22.22.0/24

							Which will produce:
							1. VPC peering between vpc-01 and vpc-02
							2. Remote VPC peering between vpc-02 and vpc-04 on switch group named border
							3. External peering for vpc-01 with External as5835 with default vpc subnet and any routes from external permitted
							4. External peering for vpc-02 with External as5835 with subnets sub1 and sub2 exposed from vpc-02 and default route
							   from external permitted as well any route that belongs to 22.22.22.0/24

							VPC Peerings:

							1+2 -- VPC peering between vpc-01 and vpc-02
							demo-1+demo-2 -- VPC peering between vpc-demo-1 and vpc-demo-2
							1+2:r -- remote VPC peering between vpc-01 and vpc-02 on switch group if only one switch group is present
							1+2:r=border -- remote VPC peering between vpc-01 and vpc-02 on switch group named border
							1+2:remote=border -- same as above

							External Peerings:

							1~as5835 -- external peering for vpc-01 with External as5835
							1~ -- external peering for vpc-1 with external if only one external is present for ipv4 namespace of vpc-01, allowing
								default subnet and any route from external
							1~:subnets=default@prefixes=0.0.0.0/0 -- external peering for vpc-1 with auth external with default vpc subnet and
								default route from external permitted
							1~as5835:subnets=default,other:prefixes=0.0.0.0/0_le32_ge32,22.22.22.0/24 -- same but with more details
							1~as5835:s=default,other:p=0.0.0.0/0_le32_ge32,22.22.22.0/24 -- same as above
						`, "							", "")),
						Flags: append(defaultFlags, accessNameFlag,
							&cli.BoolFlag{
								Name:    "wait-switches-ready",
								Aliases: []string{"wait"},
								Usage:   "wait for switches to be ready before and after configuring peerings",
								Value:   true,
							},
						),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABSetupPeerings(ctx, workDir, cacheDir, hhfab.SetupPeeringsOpts{
								WaitSwitchesReady: c.Bool("wait-switches-ready"),
								Requests:          c.Args().Slice(),
							}); err != nil {
								return fmt.Errorf("setup-peerings: %w", err)
							}

							return nil
						},
					},
					{
						Name:    "test-connectivity",
						Aliases: []string{"conns"},
						Usage:   "test connectivity between all servers",
						Flags: append(defaultFlags, accessNameFlag,
							&cli.BoolFlag{
								Name:    "wait-switches-ready",
								Aliases: []string{"wait"},
								Usage:   "wait for switches to be ready before testing connectivity",
								Value:   true,
							},
							&cli.IntFlag{
								Name:  "pings",
								Usage: "number of pings to send between each pair of servers (0 to disable)",
								Value: 5,
							},
							&cli.IntFlag{
								Name:  "iperfs",
								Usage: "seconds of iperf3 test to run between each pair of reachable servers (0 to disable)",
								Value: 10,
							},
							&cli.Float64Flag{
								Name:  "iperfs-speed",
								Usage: "minimum speed in Mbits/s for iperf3 test to consider successful (0 to not check speeds)",
								Value: 8500,
							},
							&cli.IntFlag{
								Name:  "curls",
								Usage: "number of curl tests to run for each server to test external connectivity (0 to disable)",
								Value: 3,
							},
						),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABTestConnectivity(ctx, workDir, cacheDir, hhfab.TestConnectivityOpts{
								WaitSwitchesReady: c.Bool("wait-switches-ready"),
								PingsCount:        c.Int("pings"),
								IPerfsSeconds:     c.Int("iperfs"),
								IPerfsMinSpeed:    c.Float64("iperfs-speed"),
								CurlsCount:        c.Int("curls"),
							}); err != nil {
								return fmt.Errorf("test-connectivity: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "switch",
						Usage:  "manage switch reinstall or power",
						Flags:  append(defaultFlags, accessNameFlag),
						Before: before(false),
						Subcommands: []*cli.Command{
							{
								Name:  "power",
								Usage: "manage switch power state (ON, OFF, or CYCLE)",
								Flags: []cli.Flag{
									&cli.StringFlag{
										Name:    "name",
										Aliases: []string{"n"},
										Usage:   "name of the switch to power ON|OFF|CYCLE",
									},
									&cli.BoolFlag{
										Name:  "all",
										Usage: "apply action to all switches",
									},
									yesFlag,
								},
								UsageText: "hhfab vlab switch power [--name <switchName>|--all] <action>",
								Action: func(c *cli.Context) error {
									switchName := c.String("name")
									if c.Bool("all") {
										switchName = "--all"
									}
									if switchName == "" {
										return fmt.Errorf("missing required flag: --name/-n") //nolint:goerr113
									}

									if c.NArg() != 1 {
										return fmt.Errorf("unexpected amount of agruments (use ON, OFF, or CYCLE)") //nolint:goerr113
									}

									powerAction := strings.ToUpper(c.Args().First())
									if powerAction != "ON" && powerAction != "OFF" && powerAction != "CYCLE" {
										return fmt.Errorf("invalid power action: %s (use ON, OFF, or CYCLE)", powerAction) //nolint:goerr113
									}

									if err := yesCheck(c); err != nil {
										return err
									}

									if err := hhfab.DoSwitchPower(ctx, workDir, cacheDir, switchName, powerAction); err != nil {
										return fmt.Errorf("failed to power switch: %w", err)
									}

									return nil
								},
							},
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
						Before: before(true),
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
						Before: before(true),
						Action: func(c *cli.Context) error {
							if err := hhfab.PreparePassthrough(ctx, c.Args().Slice()); err != nil {
								return fmt.Errorf("preparing passthrough: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "kill-stale-vms",
						Usage:  "kill stale VLAB VMs",
						Flags:  defaultFlags,
						Before: before(true),
						Action: func(_ *cli.Context) error {
							if _, err := hhfab.CheckStaleVMs(ctx, true); err != nil {
								return fmt.Errorf("killing stale vms: %w", err)
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
