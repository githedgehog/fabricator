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
	// SSHTimeout bounds connecting: dials, handshakes and session or sftp setup, 60s if not set
	SSHTimeout time.Duration
}

func (c *Config) timeout() time.Duration {
	if c.SSHTimeout == 0 {
		return 60 * time.Second
	}

	return c.SSHTimeout
}

func (c *Config) Wait(ctx context.Context) error {
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

// NewSftp returns an sftp client and a cleanup func closing its connection, both nil on error. sftp calls take no
// context, so the connection is closed once ctx is done to unblock them.
func (c *Config) NewSftp(ctx context.Context) (*sftp.Client, func() error, error) {
	var sftpClient *sftp.Client
	closeClient, err := c.connect(ctx, func(client *ssh.Client) error {
		var err error
		if sftpClient, err = sftp.NewClient(client); err != nil {
			return fmt.Errorf("creating new sftp client: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connecting: %w", err)
	}

	stop := context.AfterFunc(ctx, func() { _ = closeClient() })

	return sftpClient, func() error {
		stop()

		return closeClient()
	}, nil
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

// closeSftp closes the ssh connection first as closing sftp waits for its reader
func closeSftp(ftp *sftp.Client, cleanup func() error) {
	_ = cleanup()
	_ = ftp.Close()
}

func (c *Config) UploadPath(ctx context.Context, localPath string, remotePath string) error {
	ftp, cleanup, err := c.NewSftp(ctx)
	if err != nil {
		return fmt.Errorf("creating sftp: %w", err)
	}
	defer closeSftp(ftp, cleanup)

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
		return fmt.Errorf("syncing local file: %w", err)
	}

	return nil
}

func (c *Config) DownloadPath(ctx context.Context, remotePath string, localPath string) error {
	ftp, cleanup, err := c.NewSftp(ctx)
	if err != nil {
		return fmt.Errorf("creating sftp: %w", err)
	}
	defer closeSftp(ftp, cleanup)

	return DownloadPathWith(ftp, remotePath, localPath)
}
