// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// This file started as https://github.com/appleboy/easyssh-proxy adjusted to accept context.Context instead of timeout.
// Connecting is done directly with golang.org/x/crypto/ssh so the context bounds the whole setup, handshake included.
package sshutil

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

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
// bound the dials, handshakes and setup; the connection is closed to abort them once either expires. It returns a func
// closing the client and the proxy connection.
func (c *Config) connect(ctx context.Context, setup func(client *ssh.Client) error) (func() error, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	config, err := c.clientConfig(c.Remote.User, c.Password)
	if err != nil {
		return nil, err
	}

	var proxy *ssh.Client
	if c.Proxy != nil {
		proxyConfig, err := c.clientConfig(c.Proxy.User, "")
		if err != nil {
			return nil, err
		}

		proxy, err = connectVia(ctx, nil, *c.Proxy, proxyConfig, nil)
		if err != nil {
			return nil, fmt.Errorf("connecting to proxy: %w", err)
		}
	}

	client, err := connectVia(ctx, proxy, c.Remote, config, setup)
	if err != nil {
		if proxy != nil {
			_ = proxy.Close()
		}

		return nil, err
	}

	if proxy == nil {
		return client.Close, nil
	}

	return func() error {
		return errors.Join(client.Close(), proxy.Close())
	}, nil
}

// connectVia dials the remote, through the proxy if not nil, and runs the SSH handshake and setup on the connection,
// closing it if ctx is done first. A connection through the proxy is a channel, closing it doesn't unblock reads if the
// proxy stops answering, so the proxy is closed as well.
func connectVia(ctx context.Context, proxy *ssh.Client, remote Remote, config *ssh.ClientConfig, setup func(client *ssh.Client) error) (*ssh.Client, error) {
	addr := net.JoinHostPort(remote.Host, strconv.FormatUint(uint64(remote.Port), 10))

	dial := (&net.Dialer{}).DialContext
	if proxy != nil {
		dial = proxy.DialContext
	}

	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		if proxy != nil {
			_ = proxy.Close()
		}
	})

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
	// buffered as the reader may be gone on cancellation
	errChan := make(chan error, 1)

	var session *ssh.Session
	var outReader, errReader io.Reader
	closeClient, err := c.connect(ctx, func(client *ssh.Client) error {
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

	// reads lines with no length limit, a bufio.Scanner stops on a long one and the remote then blocks writing
	readLines := func(r io.Reader, out chan<- string) {
		defer close(out)

		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				select {
				case out <- strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	go func() {
		defer close(errChan)
		// closing the client first unblocks everything, session close then has nothing to send
		defer session.Close()
		defer closeClient() //nolint:errcheck

		res := make(chan error, 1)
		resWg := sync.WaitGroup{}

		resWg.Go(func() { readLines(outReader, stdoutChan) })
		resWg.Go(func() { readLines(errReader, stderrChan) })

		go func() {
			resWg.Wait()
			// the remote may close its output and keep running, closing the client on cancellation unblocks it
			res <- session.Wait()
		}()

		select {
		case <-ctx.Done():
			errChan <- fmt.Errorf("cancelled: %w", ctx.Err())
		case err := <-res:
			errChan <- err
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
