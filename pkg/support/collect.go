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
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	utilexec "k8s.io/client-go/util/exec"
	"k8s.io/streaming/pkg/httpstream"
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

// retriable reports whether a failed call may succeed on a retry, so the permanent errors, e.g. the missing
// permissions or a command that ran and failed, fail right away instead of after all the retries
func retriable(ctx context.Context) func(error) bool {
	return func(err error) bool {
		// the exec upgrade failure doesn't unwrap to the api error it carries, e.g. the pod not found once recreated
		var upgradeErr *httpstream.UpgradeFailureError
		if errors.As(err, &upgradeErr) && upgradeErr.Cause != nil {
			err = upgradeErr.Cause
		}

		var exitErr utilexec.ExitError

		return ctx.Err() == nil &&
			!kapierrors.IsForbidden(err) && !kapierrors.IsUnauthorized(err) && !kapierrors.IsInvalid(err) &&
			!kapierrors.IsBadRequest(err) && !kapierrors.IsNotFound(err) && !kapierrors.IsMethodNotSupported(err) &&
			!errors.As(err, &exitErr)
	}
}

type collector struct {
	kubeconfigPath string
	quiet          bool
}

func Collect(ctx context.Context, name, kubeconfigPath string, quiet bool) (*Dump, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	hostname, err := os.Hostname()
	if err != nil {
		slog.Warn("Can't get hostname, skipping", "err", err)
	}

	username := ""
	{
		current, err := user.Current()
		if err != nil {
			slog.Warn("Can't get current user, skipping", "err", err)
		} else {
			username = current.Username
		}
	}

	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("Can't read /etc/os-release, skipping", "err", err)
	}

	dump := &Dump{
		Version:       CurrentVersion.String(),
		parsedVersion: CurrentVersion,
		Name:          name,
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
