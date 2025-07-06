// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package sshutil

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/appleboy/easyssh-proxy"
	"github.com/pkg/sftp"
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

	SSHKey     string
	SSHKeyPath string
	SSHTimeout time.Duration

	ssh *easyssh.MakeConfig
}

func (c *Config) init() error {
	if c.SSHTimeout == 0 {
		c.SSHTimeout = 60 * time.Second
	}

	if c.ssh == nil {
		c.ssh = &easyssh.MakeConfig{
			User:    c.Remote.User,
			Server:  c.Remote.Host,
			Port:    fmt.Sprintf("%d", c.Remote.Port),
			Key:     c.SSHKey,
			KeyPath: c.SSHKeyPath,
			Timeout: c.SSHTimeout,
		}

		if c.Proxy != nil {
			c.ssh.Proxy = easyssh.DefaultConfig{
				User:    c.Proxy.User,
				Server:  c.Proxy.Host,
				Port:    fmt.Sprintf("%d", c.Proxy.Port),
				Key:     c.SSHKey,
				KeyPath: c.SSHKeyPath,
				Timeout: c.SSHTimeout,
			}
		}
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
			outStr, _, err := c.Run("echo hedgehog", 30*time.Second)
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

func (c *Config) Run(cmd string, timeout ...time.Duration) (string, string, error) {
	if err := c.init(); err != nil {
		return "", "", fmt.Errorf("initializing ssh config: %w", err)
	}

	if len(timeout) == 0 {
		timeout = append(timeout, 60*time.Second)
	}

	outStr, errStr, isTimeout, err := c.ssh.Run(cmd, timeout...)
	if err != nil {
		if isTimeout {
			return outStr, errStr, fmt.Errorf("timeout running command: %w", ErrTimeout)
		}

		return outStr, errStr, fmt.Errorf("running command: %w", err)
	}

	return outStr, errStr, nil
}

func (c *Config) StreamLog(ctx context.Context, cmd string, logName string, log func(msg string, args ...any), timeout ...time.Duration) error {
	if err := c.init(); err != nil {
		return fmt.Errorf("initializing ssh config: %w", err)
	}

	if len(timeout) == 0 {
		timeout = append(timeout, 60*time.Second)
	}

	stdoutCh, stderrCh, doneCh, errCh, err := c.ssh.Stream(cmd, timeout...)
	if err != nil {
		return fmt.Errorf("streaming command: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled: %w", ctx.Err())
		case isTimeout := <-doneCh:
			if isTimeout {
				return fmt.Errorf("timeout streaming command: %w", ErrTimeout)
			}

			return nil
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

func (c *Config) NewSftp() (*sftp.Client, func() error, error) {
	if err := c.init(); err != nil {
		return nil, nil, fmt.Errorf("initializing ssh config: %w", err)
	}

	session, client, err := c.ssh.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("connecting: %w", err)
	}
	_ = session.Close()

	cleanup := func() error {
		return client.Close()
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, cleanup, fmt.Errorf("creating new sftp client: %w", err)
	}

	return sftpClient, cleanup, nil
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

func (c *Config) UploadPath(localPath string, remotePath string) error {
	ftp, cleanup, err := c.NewSftp()
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

func (c *Config) DownloadPath(remotePath string, localPath string) error {
	ftp, cleanup, err := c.NewSftp()
	if cleanup != nil {
		defer cleanup() //nolint:errcheck
	}
	if err != nil {
		return fmt.Errorf("creating sftp: %w", err)
	}
	defer ftp.Close()

	return DownloadPathWith(ftp, remotePath, localPath)
}
