// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

// This file is https://github.com/appleboy/easyssh-proxy adjusted to accept context.Context instead of timeout.
// It's a minimal modification and cleanup to the original code to keep the overall logic and structure intact.
package sshutil

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/appleboy/easyssh-proxy"
)

// streamContext starts a command on a remote server using the provided SSH configuration and context.
// It returns channels for stdout, stderr, and error/completion notifications.
func streamContext(ctx context.Context, ssh *easyssh.MakeConfig, command string) (<-chan string, <-chan string, <-chan error, error) {
	stdoutChan := make(chan string)
	stderrChan := make(chan string)
	errChan := make(chan error)

	session, client, err := ssh.Connect()
	if err != nil {
		return stdoutChan, stderrChan, errChan, fmt.Errorf("connecting: %w", err)
	}

	outReader, err := session.StdoutPipe()
	if err != nil {
		client.Close()
		session.Close()

		return stdoutChan, stderrChan, errChan, fmt.Errorf("opening stdout pipe: %w", err)
	}
	errReader, err := session.StderrPipe()
	if err != nil {
		client.Close()
		session.Close()

		return stdoutChan, stderrChan, errChan, fmt.Errorf("opening stderr pipe: %w", err)
	}

	err = session.Start(command)
	if err != nil {
		client.Close()
		session.Close()

		return stdoutChan, stderrChan, errChan, fmt.Errorf("starting command: %w", err)
	}

	stdoutReader := io.MultiReader(outReader)
	stderrReader := io.MultiReader(errReader)
	stdoutScanner := bufio.NewScanner(stdoutReader)
	stderrScanner := bufio.NewScanner(stderrReader)

	go func() {
		defer close(errChan)
		defer client.Close()
		defer session.Close()

		res := make(chan struct{}, 1)
		resWg := sync.WaitGroup{}

		resWg.Go(func() {
			defer close(stdoutChan)
			for stdoutScanner.Scan() {
				stdoutChan <- stdoutScanner.Text()
			}
		})

		resWg.Go(func() {
			defer close(stderrChan)
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
func runContext(ctx context.Context, ssh *easyssh.MakeConfig, command string) (string, string, error) { //
	stdoutChan, stderrChan, errChan, err := streamContext(ctx, ssh, command)
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
