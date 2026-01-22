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
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/samber/lo"
	slogmulti "github.com/samber/slog-multi"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/fab/recipe"
	"go.githedgehog.com/fabricator/pkg/hhfab"
	"go.githedgehog.com/fabricator/pkg/hhfab/diagram"
	"go.githedgehog.com/fabricator/pkg/hhfab/pdu"
	"go.githedgehog.com/fabricator/pkg/version"
	"golang.org/x/term"
	"gopkg.in/natefinch/lumberjack.v2"
	"k8s.io/klog/v2"
	kctrl "sigs.k8s.io/controller-runtime"
)

const (
	FlagCatGlobal                 = "Global options:"
	FlagCatVMSizes                = "VLAB VM size overrides:"
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
	FlagIncludeCLS                = "include-cls"
	FlagNodeMgmtLinks             = "node-mgmt-links"
	FlagOOBMgmtIface              = "mgmt-link"
	FlagGateway                   = "gateway"
	FlagGateways                  = "gateways"
	FlagO11yDefaults              = "o11y-defaults"
	FlagO11yLabels                = "o11y-labels"
	FlagNameFabricMode            = "fabric-mode"
	FlagNameCount                 = "count"
	FlagNameIface                 = "iface"
	FlagNameKillStale             = "kill-stale"
	FlagNameControlsRestricted    = "controls-restricted"
	FlagNameServersRestricted     = "servers-restricted"
	FlagNameReCreate              = "recreate"
	FlagNameBuildMode             = "build-mode"
	FlagNameBuildControls         = "build-controls"
	FlagNameBuildGateways         = "build-gateways"
	FlagNameObservabilityTargets  = "o11y-targets"
	FlagNameAutoUpgrade           = "auto-upgrade"
	FlagNameFailFast              = "fail-fast"
	FlagNameReady                 = "ready"
	FlagNameCollectShowTech       = "collect-show-tech"
	FlagNameVPCMode               = "vpc-mode"
	FlagRegEx                     = "regex"
	FlagInvertRegex               = "invert-regex"
	FlagResultsFile               = "results-file"
	FlagExtended                  = "extended"
	FlagPauseOnFailure            = "pause-on-failure"
	FlagHashPolicy                = "hash-policy"
	FlagListTests                 = "list-tests"
	FlagReleaseTestRegexes        = "release-test-regexes"
	FlagReleaseTestRegexesInvert  = "release-test-regexes-invert"
)

func main() {
	if err := Run(context.Background()); err != nil {
		// TODO what if slog isn't initialized yet?
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func Run(ctx context.Context) error {
	preview := os.Getenv("HHFAB_PREVIEW") == "true"

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
	hModeFlags := []cli.Flag{
		&cli.StringFlag{
			Name:        "hydrate-mode",
			Aliases:     []string{"hm"},
			Usage:       "set hydrate mode: one of " + strings.Join(hydrateModes, ", "),
			Value:       string(hhfab.HydrateModeIfNotPresent),
			Destination: &hydrateMode,
		},
	}

	var wgSpinesCount, wgFabricLinksCount, wgMeshLinksCount, wgMCLAGLeafsCount, wgOrphanLeafsCount, wgMCLAGSessionLinks, wgMCLAGPeerLinks uint
	var wgESLAGLeafGroups string
	var wgMCLAGServers, wgESLAGServers, wgUnbundledServers, wgBundledServers, wgMultiHomedServers uint
	var wgNoSwitches bool
	var wgGatewayUplinks uint
	var wgGatewayDriver string
	var wgGatewayWorkers uint
	var wgBGPExternals, wgL2Externals, wgExtMCLAGConns, wgExtESLAGConns, wgExtOrphanConns uint
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
			Name:        "mesh-links-count",
			Usage:       "number of mesh links",
			Destination: &wgMeshLinksCount,
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
		&cli.UintFlag{
			Name:        "multihomed-servers",
			Usage:       "number of multihomed servers (2 connections to 2 different orphan leaves)",
			Destination: &wgMultiHomedServers,
		},
		&cli.BoolFlag{
			Name:        "no-switches",
			Usage:       "do not generate any switches",
			Destination: &wgNoSwitches,
		},
		&cli.UintFlag{
			Name:        "gateway-uplinks",
			Usage:       "number of uplinks for gateway, if 0 defaults to the number of spines or mesh nodes (up to 2)",
			Destination: &wgGatewayUplinks,
		},
		&cli.StringFlag{
			Name:        "gateway-driver",
			Usage:       "gateway driver to use (one of: " + strings.Join(hhfab.GatewayDrivers, ", ") + ")",
			Destination: &wgGatewayDriver,
			Value:       hhfab.GatewayDriverKernel,
		},
		&cli.UintFlag{
			Name:        "gateway-workers",
			Usage:       "number of workers for gateway",
			Destination: &wgGatewayWorkers,
			Value:       8,
		},
		&cli.UintFlag{
			Name:        "externals-bgp",
			Usage:       "number of BGP externals to generate",
			Destination: &wgBGPExternals,
		},
		&cli.UintFlag{
			Name:        "externals-l2",
			Usage:       "number of L2 externals to generate",
			Destination: &wgL2Externals,
		},
		&cli.UintFlag{
			Name:        "external-mclag-connections",
			Usage:       "number of external connections from MCLAG switches. NOTE: only 1 external connection in total is supported if using virtual switches",
			Destination: &wgExtMCLAGConns,
		},
		&cli.UintFlag{
			Name:        "external-eslag-connections",
			Usage:       "number of external connections from ESLAG switches. NOTE: only 1 external connection in total is supported if using virtual switches",
			Destination: &wgExtESLAGConns,
		},
		&cli.UintFlag{
			Name:        "external-orphan-connections",
			Usage:       "number of external connections from orphan switches. NOTE: only 1 external connection in total is supported if using virtual switches",
			Destination: &wgExtOrphanConns,
		},
	}

	var accessName string
	accessNameFlags := []cli.Flag{
		&cli.StringFlag{
			Name:        "name",
			Aliases:     []string{"n"},
			Usage:       "name of the VM or HW to access",
			Destination: &accessName,
		},
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

			fileHandler := slog.NewTextHandler(logFile, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			})

			slog.SetDefault(slog.New(slogmulti.Fanout(
				tint.NewHandler(logW, &tint.Options{
					Level:      logLevel,
					TimeFormat: time.TimeOnly,
					NoColor:    !isatty.IsTerminal(logW.Fd()),
				}),
				fileHandler,
			)))

			kubeHandler := slogmulti.Fanout(
				tint.NewHandler(logW, &tint.Options{
					Level:      slog.LevelInfo,
					TimeFormat: time.TimeOnly,
					NoColor:    !isatty.IsTerminal(logW.Fd()),
				}),
				fileHandler,
			)
			kctrl.SetLogger(logr.FromSlogHandler(kubeHandler))
			klog.SetSlogLogger(slog.New(kubeHandler))

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

	reinstallModes := []string{}
	for _, m := range hhfab.ReinstallModes {
		reinstallModes = append(reinstallModes, string(m))
	}

	powerActions := []string{}
	for _, m := range pdu.Actions {
		powerActions = append(powerActions, string(m))
	}

	pduFlags := []cli.Flag{
		&cli.StringFlag{
			Name:    "pdu-username",
			Usage:   "PDU username to attempt a reboot (" + string(hhfab.ReinstallModeHardReset) + " mode only)",
			EnvVars: []string{hhfab.VLABEnvPDUUsername},
		},
		&cli.StringFlag{
			Name:    "pdu-password",
			Usage:   "PDU password to attempt a reboot (" + string(hhfab.ReinstallModeHardReset) + " mode only)",
			EnvVars: []string{hhfab.VLABEnvPDUPassword},
		},
	}

	showTechConsoleFlags := []cli.Flag{
		&cli.StringFlag{
			Name:    "switch-username",
			Usage:   "switch username for console show-tech",
			EnvVars: []string{hhfab.VLABEnvSwitchUser},
		},
		&cli.StringFlag{
			Name:    "switch-password",
			Usage:   "switch password for console show-tech",
			EnvVars: []string{hhfab.VLABEnvSwitchPass},
		},
	}

	builFlags := []cli.Flag{
		&cli.StringFlag{
			Name:    FlagNameBuildMode,
			Aliases: []string{"mode", "m"},
			Usage:   "build mode: one of " + strings.Join(buildModes, ", "),
			EnvVars: []string{"HHFAB_BUILD_MODE"},
			Value:   string(recipe.BuildModeISO),
		},
		&cli.StringFlag{
			Name:    FlagNameObservabilityTargets,
			Usage:   "inject extra observability targets",
			EnvVars: []string{"HHFAB_O11Y_TARGETS"},
		},
	}

	var joinToken string
	joinTokenFlags := []cli.Flag{
		&cli.StringFlag{
			Name:        "join-token",
			Aliases:     []string{"j", "join"},
			Usage:       "join token for the cluster",
			Category:    FlagCatGenConfig,
			Destination: &joinToken,
			EnvVars:     []string{hhfab.JoinTokenEnv},
		},
	}

	var saveJoinToken bool
	saveJoinTokenFlags := []cli.Flag{
		&cli.BoolFlag{
			Name:        "save-join-token",
			Usage:       "save join token passed using flag or env var to the config",
			Category:    FlagCatGenConfig,
			Destination: &saveJoinToken,
			EnvVars:     []string{"HHFAB_SAVE_JOIN_TOKEN"},
		},
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
				Flags: flatten(defaultFlags, joinTokenFlags, saveJoinTokenFlags, []cli.Flag{
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
						EnvVars:  []string{"HHFAB_FABRIC_MODE"},
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
						Hidden:   !preview,
						Usage:    "[PREVIEW] include tested ONIE updaters for supported switches in the build",
						EnvVars:  []string{"HHFAB_INCLUDE_ONIE"},
					},
					&cli.BoolFlag{
						Category: FlagCatGenConfig,
						Name:     FlagIncludeCLS,
						Hidden:   !preview,
						Usage:    "[PREVIEW] include Celestica SONiC+ switch profiles",
						EnvVars:  []string{"HHFAB_INCLUDE_CLS"},
					},
					&cli.BoolFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNameImportHostUpstream,
						Hidden:   !preview,
						Usage:    "[PREVIEW] import host repo/prefix and creds from docker config as an upstream registry mode and config (creds will be stored plain text)",
						EnvVars:  []string{"HHFAB_IMPORT_HOST_UPSTREAM"},
					},
					&cli.StringSliceFlag{
						Category: FlagCatGenConfig,
						Name:     FlagNodeMgmtLinks,
						Hidden:   !preview,
						Usage:    "[PREVIEW] management links (<node-name>=<pci-address> for pci passthrough for VLAB-only)",
						EnvVars:  []string{"HHFAB_NODE_MGMT_LINKS"},
					},
					&cli.BoolFlag{
						Category: FlagCatGenConfig,
						Name:     FlagGateway,
						Aliases:  []string{"gw"},
						Usage:    "enable gateway support and add at least one gateway node",
						EnvVars:  []string{"HHFAB_GATEWAY"},
					},
					&cli.IntFlag{
						Category: FlagCatGenConfig,
						Name:     FlagGateways,
						Aliases:  []string{"gws"},
						Usage:    "add specified number of gateway nodes",
						EnvVars:  []string{"HHFAB_GATEWAYS"},
					},
					&cli.StringFlag{
						Category: FlagCatGenConfig,
						Name:     FlagO11yDefaults,
						Usage:    "default values for observability configuration",
						EnvVars:  []string{"HHFAB_O11Y_DEFAULTS"},
					},
					&cli.StringSliceFlag{
						Category: FlagCatGenConfig,
						Name:     FlagO11yLabels,
						Usage:    "default labels for observability targets",
						EnvVars:  []string{"HHFAB_O11Y_LABELS"},
					},
				}),
				Before: before(false),
				Action: func(c *cli.Context) error {
					mgmtLinks := map[string]string{}
					for _, entry := range c.StringSlice(FlagNodeMgmtLinks) {
						parts := strings.Split(entry, "=")
						if len(parts) != 2 {
							return fmt.Errorf("invalid node management link format: %s", entry) //nolint:err113
						}

						if _, ok := mgmtLinks[parts[0]]; ok {
							return fmt.Errorf("duplicate node management link key: %s", parts[0]) //nolint:err113
						}
						mgmtLinks[parts[0]] = parts[1]
					}

					o11yLabels := map[string]string{}
					for _, entry := range c.StringSlice(FlagO11yLabels) {
						parts := strings.SplitN(entry, "=", 2)
						if len(parts) != 2 {
							return fmt.Errorf("invalid o11y label format: %s", entry) //nolint:err113
						}

						if _, ok := o11yLabels[parts[0]]; ok {
							return fmt.Errorf("duplicate o11y label key: %s", parts[0]) //nolint:err113
						}
						o11yLabels[parts[0]] = parts[1]
					}

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
							FabricMode:            meta.FabricMode(c.String(FlagNameFabricMode)),
							TLSSAN:                c.StringSlice(FlagNameTLSSAN),
							DefaultPasswordHash:   c.String(FlagNameDefaultPasswordHash),
							DefaultAuthorizedKeys: c.StringSlice(FlagNameDefaultAuthorizedKeys),
							Dev:                   c.Bool(FlagNameDev),
							IncludeONIE:           c.Bool(FlagIncludeONIE),
							IncludeCLS:            c.Bool(FlagIncludeCLS),
							NodeManagementLinks:   mgmtLinks,
							Gateway:               c.Bool(FlagGateway),
							Gateways:              c.Int(FlagGateways),
							JoinToken:             joinToken,
							SaveJoinToken:         saveJoinToken,
							O11yDefaults:          fabapi.ObservabilityDefaults(c.String(FlagO11yDefaults)),
							O11yLabels:            o11yLabels,
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
				Flags:  flatten(defaultFlags, hModeFlags),
				Before: before(false),
				Action: func(_ *cli.Context) error {
					if err := hhfab.Validate(ctx, workDir, cacheDir, hhfab.HydrateMode(hydrateMode)); err != nil {
						return fmt.Errorf("validating: %w", err)
					}

					return nil
				},
			},
			{
				Name:  "diagram",
				Usage: "generate a diagram to visualze topology",
				UsageText: strings.TrimSpace(`
			Generate network topology diagrams in different formats from your wiring diagram.

			FORMATS:
			   drawio (default) - Creates a diagram.io file that can be opened with https://app.diagrams.net
			                      You can edit the diagram and export to various formats including PNG, SVG, PDF.

			   dot             - Creates a Graphviz DOT file that can be rendered using Graphviz tools:
			                      - Install Graphviz: https://graphviz.org/download
			                      - Convert to PNG: 'dot -Tpng vlab-diagram.dot -o vlab-diagram.png'
			                      - Convert to SVG: 'dot -Tsvg vlab-diagram.dot -o vlab-diagram.svg'
			                      - Convert to PDF: 'dot -Tpdf vlab-diagram.dot -o vlab-diagram.pdf'

			   mermaid         - Creates a Mermaid diagram compatible with mermaid.live or Markdown viewers:
			                      - View online: https://mermaid.live
			                      - Or use a Markdown editor with Mermaid support

			EXAMPLES:
			   # Generate default draw.io diagram
			   hhfab diagram

			   # Generate dot diagram for graphviz
			   hhfab diagram --format dot

			   # Generate draw.io diagram with custom style
			   hhfab diagram --format drawio --style hedgehog`),
				Flags: flatten(defaultFlags, []cli.Flag{
					&cli.StringFlag{
						Name:    "format",
						Aliases: []string{"f"},
						Usage: "diagram format: " + strings.Join(lo.Map(diagram.Formats,
							func(item diagram.Format, _ int) string { return string(item) }), ", "),
						Value: string(diagram.FormatDrawio),
					},
					&cli.StringFlag{
						Name:    "style",
						Aliases: []string{"s"},
						Usage: "diagram style (only applies to drawio format): " + strings.Join(lo.Map(diagram.StyleTypes,
							func(item diagram.StyleType, _ int) string { return string(item) }), ", "),
						Value: string(diagram.StyleTypeDefault),
					},
					&cli.StringFlag{
						Name:    "output",
						Aliases: []string{"o"},
						Usage:   "output file path for the generated diagram (default: result/diagram.{format})",
					},
					&cli.BoolFlag{
						Name:  "live",
						Usage: "load resources from actually running API instead of the config file (fab.yaml and include/*)",
					},
				}),
				Before: before(false),
				Action: func(c *cli.Context) error {
					format := diagram.Format(strings.ToLower(c.String("format")))
					styleType := diagram.StyleType(c.String("style"))
					if err := hhfab.Diagram(ctx, workDir, cacheDir, c.Bool("live"), format, styleType, c.String("output")); err != nil {
						return fmt.Errorf("failed to generate %s diagram: %w", format, err)
					}

					return nil
				},
			},
			{
				Name:   "versions",
				Usage:  "print versions of all components",
				Flags:  flatten(defaultFlags, hModeFlags),
				Before: before(false),
				Action: func(_ *cli.Context) error {
					if err := hhfab.Versions(ctx, workDir, cacheDir, hhfab.HydrateMode(hydrateMode)); err != nil {
						return fmt.Errorf("printing versions: %w", err)
					}

					return nil
				},
			},
			{
				Name:  "build",
				Usage: "build installers",
				Flags: flatten(defaultFlags, hModeFlags, builFlags, joinTokenFlags, []cli.Flag{
					&cli.BoolFlag{
						Name:    FlagNameBuildControls,
						Aliases: []string{"controls"},
						Usage:   "build control node(s)",
						Value:   true,
					},
					&cli.BoolFlag{
						Name:    FlagNameBuildGateways,
						Aliases: []string{"gateways"},
						Usage:   "build gateway node(s)",
						Value:   true,
					},
				}),
				Before: before(false),
				Action: func(c *cli.Context) error {
					if err := hhfab.Build(ctx, workDir, cacheDir, hhfab.BuildOpts{
						HydrateMode:          hhfab.HydrateMode(hydrateMode),
						BuildMode:            recipe.BuildMode(c.String(FlagNameBuildMode)),
						BuildControls:        c.Bool(FlagNameBuildControls),
						BuildGateways:        c.Bool(FlagNameBuildGateways),
						SetJoinToken:         joinToken,
						ObservabilityTargets: c.String(FlagNameObservabilityTargets),
					}); err != nil {
						return fmt.Errorf("building: %w", err)
					}

					return nil
				},
			},
			{
				Name:  "precache",
				Usage: "precache artifacts (only ones needed for build command by default)",
				Flags: flatten(defaultFlags, []cli.Flag{
					&cli.BoolFlag{
						Name:  "all",
						Usage: "include all artifacts",
					},
					&cli.BoolFlag{
						Name:  "vlab",
						Usage: "include VLAB artifacts",
					},
				}),
				Before: before(false),
				Action: func(c *cli.Context) error {
					if err := hhfab.Precache(ctx, workDir, cacheDir, hhfab.PrecacheOpts{
						All:  c.Bool("all"),
						VLAB: c.Bool("vlab"),
					}); err != nil {
						return fmt.Errorf("precaching: %w", err)
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
						Flags:   flatten(defaultFlags, vlabWiringGenFlags, []cli.Flag{yesFlag}),
						Before:  before(false),
						Action: func(_ *cli.Context) error {
							builder := hhfab.VLABBuilder{
								SpinesCount:        uint8(wgSpinesCount),      //nolint:gosec
								FabricLinksCount:   uint8(wgFabricLinksCount), //nolint:gosec
								MeshLinksCount:     uint8(wgMeshLinksCount),   //nolint:gosec
								MCLAGLeafsCount:    uint8(wgMCLAGLeafsCount),  //nolint:gosec
								ESLAGLeafGroups:    wgESLAGLeafGroups,
								OrphanLeafsCount:   uint8(wgOrphanLeafsCount),  //nolint:gosec
								MCLAGSessionLinks:  uint8(wgMCLAGSessionLinks), //nolint:gosec
								MCLAGPeerLinks:     uint8(wgMCLAGPeerLinks),    //nolint:gosec
								MCLAGServers:       uint8(wgMCLAGServers),      //nolint:gosec
								ESLAGServers:       uint8(wgESLAGServers),      //nolint:gosec
								UnbundledServers:   uint8(wgUnbundledServers),  //nolint:gosec
								BundledServers:     uint8(wgBundledServers),    //nolint:gosec
								MultiHomedServers:  uint8(wgMultiHomedServers), //nolint:gosec
								NoSwitches:         wgNoSwitches,
								GatewayUplinks:     uint8(wgGatewayUplinks), //nolint:gosec
								GatewayDriver:      wgGatewayDriver,
								GatewayWorkers:     uint8(wgGatewayWorkers), //nolint:gosec
								ExtBGPCount:        uint8(wgBGPExternals),   //nolint:gosec
								ExtL2Count:         uint8(wgL2Externals),    //nolint:gosec
								ExtMCLAGConnCount:  uint8(wgExtMCLAGConns),  //nolint:gosec
								ExtESLAGConnCount:  uint8(wgExtESLAGConns),  //nolint:gosec
								ExtOrphanConnCount: uint8(wgExtOrphanConns), //nolint:gosec
								YesFlag:            yes,
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
						Flags: flatten(defaultFlags, hModeFlags, builFlags, joinTokenFlags, []cli.Flag{
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
							&cli.BoolFlag{
								Name:    FlagNameAutoUpgrade,
								Aliases: []string{"upgrade"},
								Usage:   "automatically upgrade all node(s), expected to be used after initial successful installation",
								EnvVars: []string{"HHFAB_AUTO_UPGRADE"},
								Value:   false,
							},
							&cli.BoolFlag{
								Name:  FlagNameFailFast,
								Usage: "exit on first error",
								Value: true,
							},
							&cli.BoolFlag{
								Name:    FlagPauseOnFailure,
								Aliases: []string{"p"},
								Usage:   "pause running on-ready commands or release tests on failure (for troubleshooting)",
							},
							&cli.StringSliceFlag{
								Name:    FlagNameReady,
								Aliases: []string{"r"},
								Usage:   "run commands on all VMs ready (one of: " + strings.Join(onReadyCommands, ", ") + ")",
							},
							&cli.StringFlag{
								Name:    FlagOOBMgmtIface,
								Aliases: []string{"oob"},
								Usage:   "management (OOB network) interface name to be used to attach hardware devices (hlab)",
								EnvVars: []string{"HHFAB_OOB_MGMT_IFACE"},
							},
							&cli.BoolFlag{
								Name:    FlagNameCollectShowTech,
								Aliases: []string{"collect"},
								Usage:   "collect show-tech from all devices at exit or error",
								EnvVars: []string{"HHFAB_VLAB_COLLECT"},
							},
							&cli.StringFlag{
								Name:  FlagNameVPCMode,
								Usage: "VPC mode to be used for on-ready commands: empty is default (l2vni), l3vni, etc.",
							},
							&cli.StringSliceFlag{
								Name:    FlagReleaseTestRegexes,
								Aliases: []string{"rt-regex"},
								Usage:   "regex pattern to filter release tests (used when --ready=release-test)",
							},
							&cli.BoolFlag{
								Name:    FlagReleaseTestRegexesInvert,
								Aliases: []string{"rt-invert"},
								Usage:   "invert regex selection for release tests (used when --ready=release-test)",
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "control-cpus",
								Usage:    fmt.Sprintf("override control node VM number of CPUs (if not set: %d)", hhfab.DefaultSizes.Control.CPU),
								EnvVars:  []string{"HHFAB_VLAB_CTRL_CPUS"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "control-ram",
								Usage:    fmt.Sprintf("override control node VM RAM (in MB) (if not set: %d)", hhfab.DefaultSizes.Control.RAM),
								EnvVars:  []string{"HHFAB_VLAB_CTRL_RAM"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "control-disk",
								Usage:    fmt.Sprintf("override control node VM disk size (in GB) (if not set: %d)", hhfab.DefaultSizes.Control.Disk),
								EnvVars:  []string{"HHFAB_VLAB_CTRL_DISK"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "gateway-cpus",
								Usage:    fmt.Sprintf("override gateway node VM number of CPUs (if not set: %d)", hhfab.DefaultSizes.Gateway.CPU),
								EnvVars:  []string{"HHFAB_VLAB_GW_CPUS"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "gateway-ram",
								Usage:    fmt.Sprintf("override gateway node VM RAM (in MB) (if not set: %d)", hhfab.DefaultSizes.Gateway.RAM),
								EnvVars:  []string{"HHFAB_VLAB_GW_RAM"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "gateway-disk",
								Usage:    fmt.Sprintf("override gateway node VM disk size (in GB) (if not set: %d)", hhfab.DefaultSizes.Gateway.Disk),
								EnvVars:  []string{"HHFAB_VLAB_GW_DISK"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "server-cpus",
								Usage:    fmt.Sprintf("override server VM number of CPUs (if not set: %d)", hhfab.DefaultSizes.Server.CPU),
								EnvVars:  []string{"HHFAB_VLAB_SRV_CPUS"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "server-ram",
								Usage:    fmt.Sprintf("override server VM RAM (in MB) (if not set: %d)", hhfab.DefaultSizes.Server.RAM),
								EnvVars:  []string{"HHFAB_VLAB_SRV_RAM"},
							},
							&cli.UintFlag{
								Category: FlagCatVMSizes,
								Name:     "server-disk",
								Usage:    fmt.Sprintf("override server VM disk size (in GB) (if not set: %d)", hhfab.DefaultSizes.Server.Disk),
								EnvVars:  []string{"HHFAB_VLAB_SRV_DISK"},
							},
						}),
						Before: before(false),
						Action: func(c *cli.Context) error {
							onReady := []hhfab.OnReady{}
							for _, readyRaw := range c.StringSlice(FlagNameReady) {
								ready := hhfab.FromShortOnReady(readyRaw)
								if !slices.Contains(hhfab.AllOnReady, ready) {
									return fmt.Errorf("invalid on-ready command: %s", readyRaw) //nolint:err113
								}

								onReady = append(onReady, ready)
							}

							if err := hhfab.VLABUp(ctx, workDir, cacheDir, hhfab.VLABUpOpts{
								HydrateMode:          hhfab.HydrateMode(hydrateMode),
								ReCreate:             c.Bool(FlagNameReCreate),
								BuildMode:            recipe.BuildMode(c.String(FlagNameBuildMode)),
								SetJoinToken:         joinToken,
								ObservabilityTargets: c.String(FlagNameObservabilityTargets),
								VMSizesOverrides: hhfab.VMSizes{
									Control: hhfab.VMSize{
										CPU:  c.Uint("control-cpus"),
										RAM:  c.Uint("control-ram"),
										Disk: c.Uint("control-disk"),
									},
									Gateway: hhfab.VMSize{
										CPU:  c.Uint("gateway-cpus"),
										RAM:  c.Uint("gateway-ram"),
										Disk: c.Uint("gateway-disk"),
									},
									Server: hhfab.VMSize{
										CPU:  c.Uint("server-cpus"),
										RAM:  c.Uint("server-ram"),
										Disk: c.Uint("server-disk"),
									},
								},
								VLABRunOpts: hhfab.VLABRunOpts{
									KillStale:                c.Bool(FlagNameKillStale),
									ControlsRestricted:       c.Bool(FlagNameControlsRestricted),
									ServersRestricted:        c.Bool(FlagNameServersRestricted),
									BuildMode:                recipe.BuildMode(c.String(FlagNameBuildMode)),
									AutoUpgrade:              c.Bool(FlagNameAutoUpgrade),
									FailFast:                 c.Bool(FlagNameFailFast),
									PauseOnFailure:           c.Bool(FlagPauseOnFailure),
									OnReady:                  onReady,
									OOBMgmtIface:             c.String(FlagOOBMgmtIface),
									CollectShowTech:          c.Bool(FlagNameCollectShowTech),
									VPCMode:                  vpcapi.VPCMode(handleL2VNI(c.String(FlagNameVPCMode))),
									ReleaseTestRegexes:       c.StringSlice(FlagReleaseTestRegexes),
									ReleaseTestRegexesInvert: c.Bool(FlagReleaseTestRegexesInvert),
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
						Flags:  flatten(defaultFlags, accessNameFlags),
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
						Flags:  flatten(defaultFlags, accessNameFlags),
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
						Flags:  flatten(defaultFlags, accessNameFlags),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABSerialLog(ctx, workDir, cacheDir, accessName, c.Args().Slice()); err != nil {
								return fmt.Errorf("serial log: %w", err)
							}

							return nil
						},
					},
					{
						Name:   "show-tech",
						Usage:  "collect diagnostic information from all VLAB devices",
						Flags:  flatten(defaultFlags, showTechConsoleFlags),
						Before: before(false),
						Action: func(_ *cli.Context) error {
							if err := hhfab.DoShowTech(ctx, workDir, cacheDir); err != nil {
								return fmt.Errorf("ssh: %w", err)
							}

							return nil
						},
					},
					{
						Name:    "setup-vpcs",
						Aliases: []string{"vpcs"},
						Usage:   "setup VPCs and VPCAttachments for all servers and configure networking on them",
						Flags: flatten(defaultFlags, accessNameFlags, []cli.Flag{
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
							&cli.StringFlag{
								Name:    FlagHashPolicy,
								Aliases: []string{"hash"},
								Usage:   "xmit_hash_policy for bond interfaces on servers [layer2|layer2+3|layer3+4|encap2+3|encap3+4|vlan+srcmac]",
								Value:   hhfab.HashPolicyL2And3,
							},
							&cli.StringFlag{
								Name:    FlagNameVPCMode,
								Aliases: []string{"mode"},
								Usage:   "VPC mode: empty (l2vni) by default or l3vni, etc",
							},
							&cli.BoolFlag{
								Name:    "keep-peerings",
								Aliases: []string{"peerings"},
								Usage:   "Do not delete all VPC, External and Gateway peerings before enforcing VPCs",
							},
							&cli.BoolFlag{
								Name:    "host-bgp",
								Aliases: []string{"hostbgp"},
								Usage:   "Configure the first subnet of each VPC as a host-bgp subnet",
							},
						}),
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
								HashPolicy:        c.String(FlagHashPolicy),
								VPCMode:           vpcapi.VPCMode(handleL2VNI(c.String(FlagNameVPCMode))),
								KeepPeerings:      c.Bool("keep-peerings"),
								HostBGPSubnet:     c.Bool("host-bgp"),
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
							1+2:gw -- same as above but using gateway peering, only valid if gateway is present
							demo-1+demo-2 -- VPC peering between vpc-demo-1 and vpc-demo-2
							1+2:r -- remote VPC peering between vpc-01 and vpc-02 on switch group if only one switch group is present
							1+2:r=border -- remote VPC peering between vpc-01 and vpc-02 on switch group named border
							1+2:remote=border -- same as above

							External Peerings:

							1~as5835 -- external peering for vpc-01 with External as5835
							1~as5835:gw -- same as above but using gateway peering, only valid if gateway is present
							1~ -- external peering for vpc-1 with external if only one external is present for ipv4 namespace of vpc-01, allowing
								default subnet and any route from external
							1~:subnets=default@prefixes=0.0.0.0/0 -- external peering for vpc-1 with auth external with default vpc subnet and
								default route from external permitted
							1~as5835:s=default:p=default:gw -- same as above but via the gateway
							1~as5835:s=default,other:p=1.0.0.1/32_le32_ge32,22.22.22.0/24 -- two explicit prefixes allowed from the external,
								provided the external it is advertising them they will be imported and exposed to the VPC
						`, "							", "")),
						Flags: flatten(defaultFlags, accessNameFlags, []cli.Flag{
							&cli.BoolFlag{
								Name:    "wait-switches-ready",
								Aliases: []string{"wait"},
								Usage:   "wait for switches to be ready before and after configuring peerings",
								Value:   true,
							},
						}),
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
						Usage:   "test connectivity between servers",
						Flags: flatten(defaultFlags, accessNameFlags, []cli.Flag{
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
								Value: 8200,
							},
							&cli.IntFlag{
								Name:  "curls",
								Usage: "number of curl tests to run for each server to test external connectivity (0 to disable)",
								Value: 3,
							},
							&cli.StringSliceFlag{
								Name:    "source",
								Aliases: []string{"src"},
								Usage:   "server to use as source for connectivity tests (default: all servers)",
							},
							&cli.StringSliceFlag{
								Name:    "destination",
								Aliases: []string{"dst"},
								Usage:   "server to use as destination for connectivity tests (default: all servers)",
							},
							&cli.UintFlag{
								Name:  "dscp",
								Usage: "DSCP value to use for iperf3 tests (0 to disable DSCP)",
								Value: 0,
							},
							&cli.UintFlag{
								Name:  "tos",
								Usage: "TOS value to use for iperf3 tests (0 to disable TOS)",
								Value: 0,
							},
							&cli.BoolFlag{
								Name:    "all-servers",
								Aliases: []string{"all"},
								Usage:   "requires all servers to be attached to a VPC",
								Value:   false,
							},
						}),
						Before: before(false),
						Action: func(c *cli.Context) error {
							cliDSCP := c.Uint("dscp")
							if cliDSCP > 63 {
								return fmt.Errorf("dscp value must be between 0 and 63, got %d", cliDSCP) //nolint:goerr113
							}
							cliTOS := c.Uint("tos")
							if cliTOS > 255 {
								return fmt.Errorf("tos value must be between 0 and 255, got %d", cliTOS) //nolint:goerr113
							}
							if err := hhfab.DoVLABTestConnectivity(ctx, workDir, cacheDir, hhfab.TestConnectivityOpts{
								WaitSwitchesReady: c.Bool("wait-switches-ready"),
								PingsCount:        c.Int("pings"),
								IPerfsSeconds:     c.Int("iperfs"),
								IPerfsMinSpeed:    c.Float64("iperfs-speed"),
								CurlsCount:        c.Int("curls"),
								Sources:           c.StringSlice("source"),
								Destinations:      c.StringSlice("destination"),
								IPerfsDSCP:        uint8(cliDSCP),
								IPerfsTOS:         uint8(cliTOS),
								RequireAllServers: c.Bool("all-servers"),
							}); err != nil {
								return fmt.Errorf("test-connectivity: %w", err)
							}

							return nil
						},
					},
					{
						Name:    "wait-switches",
						Aliases: []string{"wait"},
						Usage:   "wait for all switches to be ready",
						Flags:   defaultFlags,
						Before:  before(false),
						Action: func(_ *cli.Context) error {
							if err := hhfab.DoVLABWait(ctx, workDir, cacheDir); err != nil {
								return fmt.Errorf("wait: %w", err)
							}

							return nil
						},
					},
					{
						Name:    "inspect-switches",
						Aliases: []string{"inspect"},
						Usage:   "wait for ready and inspect all switches",
						Flags: flatten(defaultFlags, []cli.Flag{
							&cli.IntFlag{
								Name:    "wait-applied-for",
								Aliases: []string{"wait", "w"},
								Usage:   "wait for switches being applied for this duration in seconds (0 to only wait for ready)",
								Value:   15,
							},
							&cli.BoolFlag{
								Name:  "strict",
								Usage: "fail if any switch is not ready or not inspected",
								Value: true,
							},
						}),
						Before: before(false),
						Action: func(c *cli.Context) error {
							if err := hhfab.DoVLABInspect(ctx, workDir, cacheDir, hhfab.InspectOpts{
								WaitAppliedFor: time.Duration(c.Int64("wait-applied-for")) * time.Second,
								Strict:         c.Bool("strict"),
							}); err != nil {
								return fmt.Errorf("inspect: %w", err)
							}

							return nil
						},
					},
					{
						Name:  "release-test",
						Usage: "run release tests on current VLAB instance",
						Flags: flatten(defaultFlags, []cli.Flag{
							&cli.StringSliceFlag{
								Name:    FlagRegEx,
								Aliases: []string{"r"},
								Usage:   "run only tests matched by regular expression. can be repeated",
							},
							&cli.BoolFlag{
								Name:    FlagInvertRegex,
								Aliases: []string{"i"},
								Usage:   "invert regex match",
							},
							&cli.StringFlag{
								Name:  FlagResultsFile,
								Usage: "path to a file to export test results to in JUnit XML format",
							},
							&cli.BoolFlag{
								Name:    FlagExtended,
								Aliases: []string{"e"},
								Usage:   "run extended tests",
							},
							&cli.BoolFlag{
								Name:    FlagNameFailFast,
								Aliases: []string{"f"},
								Usage:   "stop testing on first failure",
							},
							&cli.BoolFlag{
								Name:    FlagPauseOnFailure,
								Aliases: []string{"p"},
								Usage:   "pause testing on each scenario failure (for troubleshooting)",
							},
							&cli.StringFlag{
								Name:    FlagHashPolicy,
								Aliases: []string{"hash"},
								Usage:   "xmit_hash_policy for bond interfaces on servers [layer2|layer2+3|layer3+4|encap2+3|encap3+4|vlan+srcmac]",
								Value:   hhfab.HashPolicyL2And3,
							},
							&cli.StringFlag{
								Name:    FlagNameVPCMode,
								Aliases: []string{"mode"},
								Usage:   "VPC mode: empty (l2vni) by default or l3vni, etc",
							},
							&cli.BoolFlag{
								Name:    FlagListTests,
								Aliases: []string{"list", "l"},
								Usage:   "list all available tests and exit",
							},
						}),
						Before: before(false),
						Action: func(c *cli.Context) error {
							opts := hhfab.ReleaseTestOpts{
								Regexes:        c.StringSlice(FlagRegEx),
								InvertRegex:    c.Bool(FlagInvertRegex),
								ResultsFile:    c.String(FlagResultsFile),
								Extended:       c.Bool(FlagExtended),
								FailFast:       c.Bool(FlagNameFailFast),
								PauseOnFailure: c.Bool(FlagPauseOnFailure),
								HashPolicy:     c.String(FlagHashPolicy),
								VPCMode:        vpcapi.VPCMode(handleL2VNI(c.String(FlagNameVPCMode))),
								ListTests:      c.Bool(FlagListTests),
							}
							if err := hhfab.DoVLABReleaseTest(ctx, workDir, cacheDir, opts); err != nil {
								return fmt.Errorf("release-test: %w", err)
							}

							return nil
						},
					},
					{
						Name:  "switch",
						Usage: "manage switch reinstall or power",
						Flags: flatten(defaultFlags, accessNameFlags),
						Subcommands: []*cli.Command{
							{
								Name:  "reinstall",
								Usage: "reboot/reset and reinstall NOS on switches (if no switches specified, all switches will be reinstalled)",
								Flags: flatten(pduFlags, []cli.Flag{
									&cli.StringSliceFlag{
										Name:    "name",
										Aliases: []string{"n"},
										Usage:   "switch name to reinstall",
									},
									&cli.BoolFlag{
										Name:    "wait-ready",
										Aliases: []string{"w"},
										Usage:   "wait until switch(es) are Fabric-ready",
									},
									&cli.StringFlag{
										Name:    "mode",
										Aliases: []string{"m"},
										Usage:   "restart mode: " + strings.Join(reinstallModes, ", "),
										Value:   string(hhfab.ReinstallModeHardReset),
									},
									&cli.StringFlag{
										Name:    "switch-username",
										Usage:   "switch username to attempt a reboot (" + string(hhfab.ReinstallModeReboot) + " mode only, prompted for if empty)",
										EnvVars: []string{"HHFAB_VLAB_REINSTALL_SWITCH_USERNAME"},
									},
									&cli.StringFlag{
										Name:    "switch-password",
										Usage:   "switch password to attempt a reboot (" + string(hhfab.ReinstallModeReboot) + " mode only, prompted for if empty)",
										EnvVars: []string{"HHFAB_VLAB_REINSTALL_SWITCH_PASSWORD"},
									},
									verboseFlag,
									yesFlag,
								}),
								Before: before(false),
								Action: func(c *cli.Context) error {
									mode := c.String("mode")
									if !slices.Contains(reinstallModes, mode) {
										return fmt.Errorf("invalid mode: %s", mode) //nolint:goerr113
									}

									if err := yesCheck(c); err != nil {
										return err
									}

									username := c.String("switch-username")
									password := c.String("switch-password")
									if mode == string(hhfab.ReinstallModeReboot) {
										if username == "" {
											fmt.Print("Enter username: ")
											if _, err := fmt.Scanln(&username); err != nil {
												return fmt.Errorf("failed to read username: %w", err)
											}
										}

										if password == "" {
											fmt.Print("Enter password: ")
											bytePassword, err := term.ReadPassword(syscall.Stdin)
											if err != nil {
												return fmt.Errorf("failed to read password: %w", err)
											}
											password = string(bytePassword)
											fmt.Println()
										}

										if username == "" || password == "" {
											return fmt.Errorf("credentials required for reboot mode") //nolint:goerr113
										}
									}

									if mode == string(hhfab.ReinstallModeHardReset) && (c.String("pdu-username") == "" || c.String("pdu-password") == "") {
										return fmt.Errorf("PDU credentials required for hard reset mode") //nolint:goerr113
									}

									opts := hhfab.SwitchReinstallOpts{
										Switches:       c.StringSlice("name"),
										Mode:           hhfab.SwitchReinstallMode(mode),
										SwitchUsername: username,
										SwitchPassword: password,
										PDUUsername:    c.String("pdu-username"),
										PDUPassword:    c.String("pdu-password"),
										WaitReady:      c.Bool("wait-ready"),
									}

									if err := hhfab.DoSwitchReinstall(ctx, workDir, cacheDir, opts); err != nil {
										return fmt.Errorf("reinstall failed: %w", err)
									}

									return nil
								},
							},
							{
								Name:  "power",
								Usage: "manage switch power state using the PDU (if no switches specified, all switches will be affected)",
								Flags: flatten(pduFlags, []cli.Flag{
									&cli.StringSliceFlag{
										Name:    "name",
										Aliases: []string{"n"},
										Usage:   "switch name to manage power",
									},
									&cli.StringFlag{
										Name:    "action",
										Aliases: []string{"a"},
										Usage:   "power action: one of " + strings.Join(powerActions, ", "),
										Value:   string(pdu.ActionCycle),
									},
									verboseFlag,
									yesFlag,
								}),
								Before: before(false),
								Action: func(c *cli.Context) error {
									action := strings.ToLower(c.String("action"))
									if !slices.Contains(powerActions, action) {
										return fmt.Errorf("invalid action: %s", action) //nolint:goerr113
									}

									if err := yesCheck(c); err != nil {
										return err
									}

									opts := hhfab.SwitchPowerOpts{
										Switches:    c.StringSlice("name"),
										Action:      pdu.Action(action),
										PDUUsername: c.String("pdu-username"),
										PDUPassword: c.String("pdu-password"),
									}

									if err := hhfab.DoSwitchPower(ctx, workDir, cacheDir, opts); err != nil {
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
						Name:  "setup-oob-mgmt-net",
						Usage: "setup bridge to be OOB Mgmt Network with tap devices and interfaces",
						Flags: flatten(defaultFlags, []cli.Flag{
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
							&cli.StringSliceFlag{
								Name:     FlagNameIface,
								Usage:    "list of interfaces to include into the bridge",
								Required: false,
							},
						}),
						Before: before(true),
						Action: func(c *cli.Context) error {
							if err := hhfab.SetupOOBMgmtNet(ctx, c.Int(FlagNameCount), c.StringSlice(FlagNameIface)); err != nil {
								return fmt.Errorf("preparing oob mgmt net: %w", err)
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

func flatten[T any, Slice ~[]T](collection ...Slice) Slice {
	return lo.Flatten(collection)
}

func handleL2VNI(in string) string {
	if in == "l2vni" {
		return ""
	}

	return in
}
