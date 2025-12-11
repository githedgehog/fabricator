// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package fab

import (
	"context"
	"crypto/rand"
	_ "embed"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
)

// TODO more comments, instructions on how to generate password hashes, etc.

//go:embed initconfig.tmpl.yaml
var initConfigTmpl string

const (
	DevAdminPasswordHash = "$5$8nAYPGcl4l6G7Av1$Qi4/gnM0yPtGv9kjpMh78NuNSfQWy7vR1rulHpurL36" //nolint:gosec
	DevSSHKey            = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGpF2+9I1Nj4BcN7y6DjzTbq1VcUYIRGyfzId5ZoBEFj"
)

type InitConfigInput struct {
	FabricMode                meta.FabricMode
	TLSSAN                    []string
	DefaultPasswordHash       string
	DefaultAuthorizedKeys     []string
	Dev                       bool
	IncludeONIE               bool
	IncludeCLS                bool
	RegUpstream               *fabapi.ControlConfigRegistryUpstream
	ControlNodeManagementLink string
	GatewayNodeManagementLink string
	Gateway                   bool
	Gateways                  int
	JoinToken                 string
	SaveJoinToken             bool
	O11yDefaults              fabapi.ObservabilityDefaults
	O11yLabels                map[string]string
}

func InitConfig(ctx context.Context, in InitConfigInput) ([]byte, error) {
	if in.Gateways > 0 {
		in.Gateway = true
	}
	if in.Gateway {
		if in.Gateways == 0 {
			in.Gateways = 1
		}
	}
	if in.Gateways < 0 {
		return nil, fmt.Errorf("invalid number of gateways: %d", in.Gateways) //nolint:err113
	}

	if in.Dev {
		if in.DefaultPasswordHash != "" {
			return nil, fmt.Errorf("dev mode overrides default password hash") //nolint:goerr113
		}

		in.DefaultPasswordHash = DevAdminPasswordHash
		in.DefaultAuthorizedKeys = append(in.DefaultAuthorizedKeys, DevSSHKey)

		if in.Gateway && in.JoinToken == "" {
			in.JoinToken = rand.Text()
			in.SaveJoinToken = true
		}
	}

	if in.DefaultPasswordHash != "" && !strings.HasPrefix(in.DefaultPasswordHash, "$5$") {
		return nil, fmt.Errorf("default password hash must start with $5$: %q", in.DefaultPasswordHash) //nolint:goerr113
	}

	if !in.SaveJoinToken {
		if in.JoinToken != "" {
			slog.Warn("Join token is specified by flag env var, but is not saved to the config, use --save-join-token flag to save it")
		}

		in.JoinToken = ""
	}

	if !slices.Contains(fabapi.ObservabilityDefaultsList, in.O11yDefaults) {
		return nil, fmt.Errorf("invalid observability defaults mode: %s", in.O11yDefaults) //nolint:err113
	}

	data, err := tmplutil.FromTemplate("initconfig", initConfigTmpl, in)
	if err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}

	l := apiutil.NewLoader()
	if err := l.LoadAdd(ctx, apiutil.FabricatorGVKs, []byte(data)); err != nil {
		return nil, fmt.Errorf("loading generated: %w", err)
	}

	return []byte(data), nil
}
