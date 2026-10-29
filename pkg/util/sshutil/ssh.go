// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package sshutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

var ErrTimeout = fmt.Errorf("timeout")

type Remote struct {
	User string
	Host string
	Port uint
}

type Config struct {
	Remote Remote
	Proxy  *Remote

	SSHKey   string
	Password string
	// SSHTimeout bounds connecting: dials, handshakes and session or sftp setup
	SSHTimeout time.Duration
}

func (c *Config) init() error {
	if c.SSHTimeout == 0 {
		c.SSHTimeout = 60 * time.Second
	}

	return nil
}

func (c *Config) Wait(ctx context.Context) error {
	if err := c.init(); err != nil {
		return fmt.Errorf("initializing ssh config: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled: %w", ctx.Err())
		case <-time.After(5 * time.Second):
			runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			outStr, _, err := c.Run(runCtx, "echo hedgehog")
			cancel()
			if err != nil {
				continue
			}

			if outStr != "hedgehog\n" {
				slog.Warn("unexpected wait response", "value", outStr)

				continue
			}

			return nil
		}
	}
}

func (c *Config) Run(ctx context.Context, cmd string) (string, string, error) {
	if err := c.init(); err != nil {
		return "", "", fmt.Errorf("initializing ssh config: %w", err)
	}

	outStr, errStr, err := runContext(ctx, c, cmd)
	if err != nil {
		return outStr, errStr, fmt.Errorf("running command: %w", err)
	}

	return outStr, errStr, nil
}

// ExitStatus reports the remote process's exit code from an error returned by
// Run, if the command actually started on the remote end and exited. It
// returns false for connect/session-setup failures and context
// cancellation/timeout, where the command may never have run.
func ExitStatus(err error) (int, bool) {
	if exitErr, ok := errors.AsType[*ssh.ExitError](err); ok {
		return exitErr.ExitStatus(), true
	}

	return 0, false
}

func (c *Config) StreamLog(ctx context.Context, cmd string, logName string, log func(msg string, args ...any)) error {
	if err := c.init(); err != nil {
		return fmt.Errorf("initializing ssh config: %w", err)
	}

	stdoutCh, stderrCh, errCh, err := streamContext(ctx, c, cmd)
	if err != nil {
		return fmt.Errorf("streaming command: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled: %w", ctx.Err())
		case err := <-errCh:
			if err != nil {
				return fmt.Errorf("streaming command: %w", err)
			}

			return nil
		case line, ok := <-stdoutCh:
			if !ok {
				stdoutCh = nil
			}
			if line != "" {
				log(logName + ": " + line)
			}
		case line, ok := <-stderrCh:
			if !ok {
				stderrCh = nil
			}
			if line != "" {
				log(logName + ": " + line)
			}
		}
	}
}

func (c *Config) NewSftp(ctx context.Context) (*sftp.Client, func() error, error) {
	if err := c.init(); err != nil {
		return nil, nil, fmt.Errorf("initializing ssh config: %w", err)
	}

	var sftpClient *sftp.Client
	client, err := c.connect(ctx, func(client *ssh.Client) error {
		var err error
		if sftpClient, err = sftp.NewClient(client); err != nil {
			return fmt.Errorf("creating new sftp client: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connecting: %w", err)
	}

	return sftpClient, client.Close, nil
}

func UploadPathWith(ftp *sftp.Client, localPath string, remotePath string) error {
	local, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("opening local file: %w", err)
	}
	defer local.Close()

	remote, err := ftp.Create(remotePath)
	if err != nil {
		return fmt.Errorf("creating remote file: %w", err)
	}
	defer remote.Close()

	if _, err := io.Copy(remote, local); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}

	return nil
}

func (c *Config) UploadPath(ctx context.Context, localPath string, remotePath string) error {
	ftp, cleanup, err := c.NewSftp(ctx)
	if cleanup != nil {
		defer cleanup() //nolint:errcheck
	}
	if err != nil {
		return fmt.Errorf("creating sftp: %w", err)
	}
	defer ftp.Close()

	return UploadPathWith(ftp, localPath, remotePath)
}

func DownloadPathWith(ftp *sftp.Client, remotePath string, localPath string) error {
	local, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("creating local file: %w", err)
	}
	defer local.Close()

	remote, err := ftp.Open(remotePath)
	if err != nil {
		return fmt.Errorf("opening remote file: %w", err)
	}
	defer remote.Close()

	if _, err = io.Copy(local, remote); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}

	if err := local.Sync(); err != nil {
		return fmt.Errorf("syncing local file: %w", local.Sync())
	}

	return nil
}

func (c *Config) DownloadPath(ctx context.Context, remotePath string, localPath string) error {
	ftp, cleanup, err := c.NewSftp(ctx)
	if cleanup != nil {
		defer cleanup() //nolint:errcheck
	}
	if err != nil {
		return fmt.Errorf("creating sftp: %w", err)
	}
	defer ftp.Close()

	return DownloadPathWith(ftp, remotePath, localPath)
}
