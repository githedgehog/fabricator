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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/hhfab"
	"golang.org/x/term"
)

const (
	ContextNameFlag = "context"
	CatGlobal       = "Global options:"
)

var ContextNameFlagAliases = []string{"c"}

var version = "(devel)"

func main() {
	if err := Run(context.Background()); err != nil {
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
		Category:    CatGlobal,
	}
	briefFlag := &cli.BoolFlag{
		Name:        "brief",
		Aliases:     []string{"b"},
		Usage:       "brief output (only warn and error)",
		EnvVars:     []string{"HHFAB_BRIEF"},
		Destination: &brief,
		Category:    CatGlobal,
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting user home dir: %w", err)
	}

	defaultBaseDir := filepath.Join(home, ".hhfab")
	var baseDir string
	baseDirFlag := &cli.StringFlag{
		Name:        "base-dir",
		Usage:       "use base dir `DIR` where all contexts are stored",
		EnvVars:     []string{"HHFAB_BASE_DIR"},
		Value:       defaultBaseDir,
		Destination: &baseDir,
		Category:    CatGlobal,
	}

	defaultCacheDir := filepath.Join(home, ".hhfab-cache")
	var cacheDir string
	cacheDirFlag := &cli.StringFlag{
		Name:        "cache-dir",
		Usage:       "use cache dir `DIR` for caching downloaded files",
		EnvVars:     []string{"HHFAB_CACHE_DIR"},
		Value:       defaultCacheDir,
		Destination: &cacheDir,
		Category:    CatGlobal,
	}

	contextNameValidator := func(_ *cli.Context, name string) error {
		if !hhfab.IsContextNameValid(name) {
			return fmt.Errorf("invalid context name: should be 3-12 chars long (a-z, 0-9 and -), start with a-z, end with a-z or 0-9") //nolint:goerr113
		}

		return nil
	}

	defaultContext := hhfab.DefaultContext
	var currentContext string
	contextFlag := &cli.StringFlag{
		Name:        ContextNameFlag,
		Aliases:     ContextNameFlagAliases,
		Usage:       "use context `NAME`",
		EnvVars:     []string{"HHFAB_CONTEXT"},
		Value:       defaultContext,
		Destination: &currentContext,
		Category:    CatGlobal,
		Action:      contextNameValidator,
	}

	yes := false
	yesFlag := &cli.BoolFlag{
		Name:        "yes",
		Aliases:     []string{"y"},
		Usage:       "assume yes to all prompts",
		EnvVars:     []string{"HHFAB_YES"},
		Destination: &yes,
	}

	var cfg *hhfab.Config

	before := func(isContext, checkYes, logOnly bool) cli.BeforeFunc {
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

			logW := os.Stdout
			logger := slog.New(
				tint.NewHandler(logW, &tint.Options{
					Level:      logLevel,
					TimeFormat: time.TimeOnly,
					NoColor:    !isatty.IsTerminal(logW.Fd()),
				}),
			)
			slog.SetDefault(logger)

			if logOnly {
				return nil
			}

			oldCfgInfo, err := os.Stat(filepath.Join(baseDir, "config.yaml"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("checking for old hhfab leftovers: %w", err)
			}
			if err == nil && oldCfgInfo.Size() > 0 {
				return fmt.Errorf("old hhfab leftovers found, please remove %q before continuing", baseDir) //nolint:goerr113
			}

			if checkYes && !yes {
				return fmt.Errorf("explicit confirmation required, use --yes to confirm") //nolint:goerr113
			}

			args := []any{
				"version", version,
			}

			if isContext {
				fileContext, err := hhfab.GetCurrentContext(baseDir)
				if err != nil {
					return err //nolint:wrapcheck
				}
				if fileContext != "" {
					currentContext = fileContext
				}

				if currentContext != defaultContext {
					args = append(args, "context", currentContext)
				}
			}

			if baseDir != defaultBaseDir {
				args = append(args, "base", baseDir)
			}

			if cacheDir != defaultCacheDir {
				args = append(args, "cache", cacheDir)
			}

			// if len(args) == 2 {
			// 	slog.Debug("Hedgehog Fabricator", args...)
			// } else {
			slog.Info("Hedgehog Fabricator", args...)
			// }

			cfg, err = hhfab.Load(version, baseDir, cacheDir, isContext, currentContext)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			return nil
		}
	}

	defaultFlags := []cli.Flag{
		baseDirFlag,
		cacheDirFlag,
		verboseFlag,
		briefFlag,
	}

	contextedFlags := append(defaultFlags, contextFlag)

	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}
	app := &cli.App{
		Name:  "hhfab",
		Usage: "hedgehog fabricator - build, install and run hedgehog",
		Description: `Create Hedgehog configurations, wiring diagram, build it into installer and run it in the VLAB (virtual lab):
	1.  Create a new context with 'hhfab create', to run a VLAB use '--vlab', to use default creds use '--dev' (unsafe)
	2a. If building for physical environment, use 'hhfab wiring sample' to generate sample wiring diagram
	2b. If building for VLAB, use 'hhfab wiring vlab' to generate VLAB wiring diagram
	3.  Validate configs and wiring with 'hhfab validate' at any time (optional)
	4.  If not done yet, provide registry credentials with 'hhfab registry login' (saved for all contexts)
	5.  Build Hedgehog installer with 'hhfab build'
	6.  Use 'hhfab vlab up' to run VLAB (will run build automatically if outdated)
		`,
		Version:                version,
		Suggest:                true,
		UseShortOptionHandling: true,
		EnableBashCompletion:   true,
		Commands: []*cli.Command{
			{
				Name:   "init",
				Hidden: true,
				Before: before(false, false, true),
				Action: func(_ *cli.Context) error {
					return fmt.Errorf("seems like you're trying to use an old hhfab with a new binary, please start with 'hhfab create'") //nolint:goerr113
				},
			},
			{
				Name:  "create",
				Usage: "create a new context for configuring and building hedgehog installer",
				Flags: append(defaultFlags,
					&cli.StringFlag{
						Name:        ContextNameFlag,
						Aliases:     ContextNameFlagAliases,
						Usage:       "create context `NAME`",
						Required:    true,
						Destination: &currentContext,
						Action:      contextNameValidator,
					},
					&cli.StringFlag{
						Name:    "registry-repo",
						Usage:   "download artifacts from `REPO`",
						EnvVars: []string{"HHFAB_REG_REPO"},
						Value:   hhfab.DefaultRepo,
					},
					&cli.StringFlag{
						Name:    "registry-prefix",
						Usage:   "prepend artifact names with `PREFIX`",
						EnvVars: []string{"HHFAB_REG_PREFIX"},
						Value:   hhfab.DefaultPrefix,
					},
					// TODO allow using existing config and wiring
				),
				Before: before(false, false, false),
				Action: func(c *cli.Context) error {
					if err := cfg.ContextCreate(ctx, currentContext, hhfab.ContextCreateConfig{
						RegistryConfig: hhfab.RegistryConfig{
							Repo:   c.String("registry-repo"),
							Prefix: c.String("registry-prefix"),
						},
					}); err != nil {
						return fmt.Errorf("creating context: %w", err)
					}

					return nil
				},
			},
			{
				Name:  "delete",
				Usage: "delete context and all its data",
				Flags: append(defaultFlags,
					&cli.StringFlag{
						Name:        ContextNameFlag,
						Aliases:     ContextNameFlagAliases,
						Usage:       "delete context `NAME`",
						Required:    true,
						Destination: &currentContext,
						Action:      contextNameValidator,
					},
					yesFlag,
				),
				Before: before(false, false, false),
				Action: func(_ *cli.Context) error {
					if err := cfg.ContextDelete(ctx, currentContext); err != nil {
						return fmt.Errorf("deleting context: %w", err)
					}

					return nil
				},
			},
			{
				Name:   "list",
				Usage:  "list contexts",
				Flags:  defaultFlags,
				Before: before(false, false, false),
				Action: func(_ *cli.Context) error {
					if err := cfg.ContextList(ctx); err != nil {
						return fmt.Errorf("listing contexts: %w", err)
					}

					return nil
				},
			},
			{
				Name:  "use",
				Usage: "set the current context",
				Flags: append(defaultFlags,
					&cli.StringFlag{
						Name:        ContextNameFlag,
						Aliases:     ContextNameFlagAliases,
						Usage:       "set current context to `NAME`",
						Required:    true,
						Destination: &currentContext,
						Action:      contextNameValidator,
					},
				),
				Before: before(true, false, false),
				Action: func(_ *cli.Context) error {
					if err := cfg.ContextUse(ctx, currentContext); err != nil {
						return fmt.Errorf("using context: %w", err)
					}

					return nil
				},
			},
			{
				Name:    "registry",
				Aliases: []string{"reg"},
				Usage:   "manage registry credentials",
				Subcommands: []*cli.Command{
					{
						Name:  "login",
						Usage: "login to the registry (credentials stored for all contexts)",
						Flags: append(defaultFlags,
							&cli.StringFlag{
								Name:    "repo",
								Aliases: []string{"r"},
								Usage:   "login to the `REPO`",
								Value:   "ghcr.io",
							},
							&cli.StringFlag{
								Name:    "username",
								Aliases: []string{"u"},
								Usage:   "use `USERNAME`, if not specified, will be prompted",
							},
							&cli.StringFlag{
								Name:    "password",
								Aliases: []string{"p"},
								Usage:   "use `PASSWORD`, if not specified, will be prompted",
							},
						),
						Before: func(c *cli.Context) error {
							if err := before(false, false, false)(c); err != nil {
								return err
							}

							slog.Info("Logging in to the registry: " + c.String("repo"))

							if c.String("username") == "" {
								fmt.Print("Enter username: ")
								username, err := readInput(false)
								if err != nil {
									return err
								}
								if err := c.Set("username", username); err != nil {
									return fmt.Errorf("setting username: %w", err)
								}
							}

							if c.String("password") == "" {
								fmt.Print("Enter password: ")
								password, err := readInput(true)
								if err != nil {
									return err
								}
								if err := c.Set("password", password); err != nil {
									return fmt.Errorf("setting password: %w", err)
								}
							}

							return nil
						},
						Action: func(c *cli.Context) error {
							repo := c.String("repo")
							if err := cfg.Login(ctx, repo, c.String("username"), c.String("password")); err != nil {
								return fmt.Errorf("logging in to %q: %w", repo, err)
							}

							return nil
						},
					},
					{
						Name:  "logout",
						Usage: "log out of the registry (credentials stored for all contexts)",
						Flags: append(defaultFlags,
							&cli.StringFlag{
								Name:     "repo",
								Aliases:  []string{"r"},
								Usage:    "logout of the `REPO`",
								Required: true,
							},
						),
						Before: before(false, false, false),
						Action: func(c *cli.Context) error {
							repo := c.String("repo")
							if err := cfg.Logout(ctx, repo); err != nil {
								return fmt.Errorf("logging out of %q: %w", repo, err)
							}

							return nil
						},
					},
				},
			},
			{
				Name:        "wiring",
				Usage:       "generate wiring",
				Subcommands: []*cli.Command{
					// TODO
					// sample -t collapsed-core-mclag, spine-leaf-mclag, spine-leaf-eslag, spine-leaf standalone, etc
					// vlab spine-leaf --... --... --...
				},
			},
			{
				Name:   "validate",
				Usage:  "validate configs and wiring",
				Flags:  contextedFlags,
				Before: before(true, false, false),
				Action: func(_ *cli.Context) error {
					slog.Info("Validating configs and wiring") // TODO move to impl
					// TODO
					panic("not implemented")
				},
			},
			{
				Name:   "build",
				Usage:  "build hedgehog installer",
				Flags:  contextedFlags,
				Before: before(true, false, false),
				Action: func(_ *cli.Context) error {
					// TODO
					panic("not implemented")
				},
			},
			{
				Name:        "vlab",
				Usage:       "use virtual lab",
				Subcommands: []*cli.Command{
					// TODO
					// up
					// recreate --all / --name
					// reset/cleanup? like remove taps, kill stale vms
				},
			},
			// TODO
			// {
			// 	Name:        "completion",
			// 	Usage:       "generate shell completion code for specified shell",
			// 	Subcommands: []*cli.Command{},
			// },
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

func readInput(sensitive bool) (string, error) {
	fd := int(os.Stdin.Fd()) //nolint:gosec
	if sensitive && term.IsTerminal(fd) {
		bytes, err := term.ReadPassword(fd)
		defer fmt.Println()
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}

		return string(bytes), nil
	}

	input := ""
	_, err := fmt.Scanln(&input)
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}

	return input, nil
}
