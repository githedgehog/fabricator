// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// This file started as https://github.com/appleboy/easyssh-proxy adjusted to accept context.Context instead of timeout.
// Connecting is done directly with golang.org/x/crypto/ssh so the context bounds the whole setup, handshake included.
package sshutil

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"golang.org/x/crypto/ssh"
)

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// clientConfig returns the SSH client config to log in as user with the configured key and password.
func (c *Config) clientConfig(user, password string) (*ssh.ClientConfig, error) {
	auth := []ssh.AuthMethod{}
	if password != "" {
		auth = append(auth, ssh.Password(password))
	}
	if c.SSHKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(c.SSHKey))
		if err != nil {
			return nil, fmt.Errorf("parsing ssh key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}

	return &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // VLAB VMs and switches get new host keys on every install
	}, nil
}

// connect connects to the remote, through the proxy if set, and runs setup on the new client. The context and SSH timeout
// bound the dials, handshakes and setup; the connection is closed to abort them once either expires.
func (c *Config) connect(ctx context.Context, setup func(client *ssh.Client) error) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, c.SSHTimeout)
	defer cancel()

	config, err := c.clientConfig(c.Remote.User, c.Password)
	if err != nil {
		return nil, err
	}

	dial := (&net.Dialer{}).DialContext
	var proxy *ssh.Client
	if c.Proxy != nil {
		proxyConfig, err := c.clientConfig(c.Proxy.User, "")
		if err != nil {
			return nil, err
		}

		proxy, err = connectVia(ctx, dial, *c.Proxy, proxyConfig, nil)
		if err != nil {
			return nil, fmt.Errorf("connecting to proxy: %w", err)
		}
		dial = proxy.DialContext
	}

	client, err := connectVia(ctx, dial, c.Remote, config, setup)
	if err != nil {
		if proxy != nil {
			_ = proxy.Close()
		}

		return nil, err
	}

	if proxy != nil {
		go func() {
			_ = client.Wait()
			_ = proxy.Close()
		}()
	}

	return client, nil
}

// connectVia dials the remote and runs the SSH handshake and setup on the connection, closing it if ctx is done first.
func connectVia(ctx context.Context, dial dialFunc, remote Remote, config *ssh.ClientConfig, setup func(client *ssh.Client) error) (*ssh.Client, error) {
	addr := net.JoinHostPort(remote.Host, strconv.FormatUint(uint64(remote.Port), 10))

	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })

	var client *ssh.Client
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		err = fmt.Errorf("handshake with %s: %w", addr, err)
	} else {
		client = ssh.NewClient(sshConn, chans, reqs)
		if setup != nil {
			err = setup(client)
		}
	}

	// ctx expired while connecting and the connection is already closed, whatever the result was
	if !stop() {
		err = fmt.Errorf("%s: %w", addr, ctx.Err())
	}
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	return client, nil
}

// streamContext starts a command on a remote server using the provided SSH configuration and context.
// It returns channels for stdout, stderr, and error/completion notifications.
func streamContext(ctx context.Context, c *Config, command string) (<-chan string, <-chan string, <-chan error, error) {
	stdoutChan := make(chan string)
	stderrChan := make(chan string)
	errChan := make(chan error)

	var session *ssh.Session
	var outReader, errReader io.Reader
	client, err := c.connect(ctx, func(client *ssh.Client) error {
		var err error
		if session, err = client.NewSession(); err != nil {
			return fmt.Errorf("opening session: %w", err)
		}
		if outReader, err = session.StdoutPipe(); err != nil {
			return fmt.Errorf("opening stdout pipe: %w", err)
		}
		if errReader, err = session.StderrPipe(); err != nil {
			return fmt.Errorf("opening stderr pipe: %w", err)
		}
		if err := session.Start(command); err != nil {
			return fmt.Errorf("starting command: %w", err)
		}

		return nil
	})
	if err != nil {
		return stdoutChan, stderrChan, errChan, fmt.Errorf("connecting: %w", err)
	}

	go func() {
		defer close(errChan)
		defer client.Close()
		defer session.Close()

		res := make(chan struct{}, 1)
		resWg := sync.WaitGroup{}

		resWg.Go(func() {
			defer close(stdoutChan)

			stdoutScanner := bufio.NewScanner(outReader)
			for stdoutScanner.Scan() {
				stdoutChan <- stdoutScanner.Text()
			}
		})

		resWg.Go(func() {
			defer close(stderrChan)

			stderrScanner := bufio.NewScanner(errReader)
			for stderrScanner.Scan() {
				stderrChan <- stderrScanner.Text()
			}
		})

		go func() {
			resWg.Wait()
			// close all of our open resources
			res <- struct{}{}
		}()

		select {
		case <-ctx.Done():
			errChan <- fmt.Errorf("cancelled: %w", ctx.Err())
		case <-res:
			errChan <- session.Wait()
		}
	}()

	return stdoutChan, stderrChan, errChan, nil
}

// runContext starts a command on a remote server using the provided SSH configuration and context.
// It returns the concatenated output and error strings, along with any errors encountered.
func runContext(ctx context.Context, c *Config, command string) (string, string, error) { //
	stdoutChan, stderrChan, errChan, err := streamContext(ctx, c, command)
	if err != nil {
		return "", "", err
	}
	// read from the output channel until the done signal is passed
	outStr, errStr := "", ""
loop:
	for {
		select {
		case err = <-errChan:
			break loop
		case outline, ok := <-stdoutChan:
			if !ok {
				stdoutChan = nil
			}
			if outline != "" {
				outStr += outline + "\n"
			}
		case errline, ok := <-stderrChan:
			if !ok {
				stderrChan = nil
			}
			if errline != "" {
				errStr += errline + "\n"
			}
		}
	}

	return outStr, errStr, err
}
