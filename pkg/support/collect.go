// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"time"

	"go.githedgehog.com/fabricator/pkg/version"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	githubActionsValue = "true"
)

var longBackoff = wait.Backoff{
	Steps:    11,
	Duration: 100 * time.Millisecond,
	Factor:   2.0,
	Jitter:   0,
}

type collector struct {
	kubeconfigPath string
	quiet          bool
}

func Collect(ctx context.Context, name, kubeconfigPath string, quiet bool) (*Dump, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	hostname, err := os.Hostname()
	if err != nil {
		slog.Warn("Can't get hostname, skipping", "err", err)
	}

	username := ""
	{
		user, err := user.Current()
		if err != nil {
			slog.Warn("Can't get current user, skipping", "err", err)
		} else {
			username = user.Username
		}
	}

	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("Can't read /etc/os-release, skipping", "err", err)
	}

	dump := &Dump{
		DumpVersion: DumpVersion{
			Version:       CurrentVersion.String(),
			parsedVersion: CurrentVersion,
		},
		Name: name,
		CreatedBy: DumpCreator{
			Hostname:   hostname,
			Username:   username,
			OSRelease:  string(osRelease),
			CtlVersion: version.Version,
		},
		CreatedAt: kmetav1.Now(),
	}

	c := collector{
		kubeconfigPath: kubeconfigPath,
		quiet:          quiet || os.Getenv("GITHUB_ACTIONS") == githubActionsValue,
	}

	if err := c.collectKubeResources(ctx, dump); err != nil {
		return nil, fmt.Errorf("collecting kube resources: %w", err)
	}

	if err := c.collectPodLogs(ctx, dump); err != nil {
		return nil, fmt.Errorf("collecting pod logs: %w", err)
	}

	if err := c.collectGatewayInsights(ctx, dump); err != nil {
		return nil, fmt.Errorf("collecting gateway insights: %w", err)
	}

	return dump, nil
}
