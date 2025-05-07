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

	"go.githedgehog.com/fabricator/pkg/version"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Collect(ctx context.Context, name string) (*Dump, error) {
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

	if err := collectKubeResources(ctx, dump); err != nil {
		return nil, fmt.Errorf("collecting kube resources: %w", err)
	}

	if err := collectPodLogs(ctx, dump); err != nil {
		return nil, fmt.Errorf("collecting pod logs: %w", err)
	}

	return dump, nil
}
