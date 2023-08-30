package main

import (
	"log"
	"os"

	cli "github.com/urfave/cli/v2"
	"go.githedgehog.com/fabricator/pkg/vlab"
)

var version = "(devel)"

func main() {
	cli.VersionFlag.(*cli.BoolFlag).Aliases = []string{"V"}

	app := &cli.App{
		Name:                   "hhv",
		Usage:                  "hedgehog vlab",
		Version:                version,
		Suggest:                true,
		UseShortOptionHandling: true,
		Commands: []*cli.Command{
			{
				Name:    "server",
				Aliases: []string{"s"},
				Usage:   "vlab server commands",
				Subcommands: []*cli.Command{
					{
						Name: "up",
						// Aliases: []string{"up"},
						Usage: "start vlab server",

						Action: func(cCtx *cli.Context) error {
							log.Println("Starting VLAB server...")

							err := vlab.StartServer("-") // TODO: make configurable
							if err != nil {
								return err
							}

							return vlab.StartGRPCServer("127.0.0.1:5000") // TODO: make configurable
						},
					},
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal("Failed with error: ", err)
	}
}
