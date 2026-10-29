// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package sshutil_test

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/crypto/ssh"
)

// silentListener accepts connections and never answers, like a slirp hostfwd port in front of a hung VM.
func silentListener(t *testing.T) sshutil.Remote {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		conns := []net.Conn{}
		defer func() {
			for _, conn := range conns {
				_ = conn.Close()
			}
		}()

		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conns = append(conns, conn)
		}
	}()

	addr := l.Addr().(*net.TCPAddr) //nolint:forcetypeassert

	return sshutil.Remote{User: "core", Host: "127.0.0.1", Port: uint(addr.Port)}
}

// echoServer is an SSH server accepting any credentials that echoes exec'd commands back and forwards direct-tcpip.
func echoServer(t *testing.T) sshutil.Remote {
	t.Helper()

	_, hostKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	require.NoError(t, err)

	config := &ssh.ServerConfig{
		PasswordCallback:  func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return &ssh.Permissions{}, nil },
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return &ssh.Permissions{}, nil },
	}
	config.AddHostKey(hostSigner)

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serveEcho(conn, config)
		}
	}()

	addr := l.Addr().(*net.TCPAddr) //nolint:forcetypeassert

	return sshutil.Remote{User: "core", Host: "127.0.0.1", Port: uint(addr.Port)}
}

func serveEcho(conn net.Conn, config *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, reqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer ch.Close()
				for req := range reqs {
					if req.Type != "exec" {
						_ = req.Reply(false, nil)

						continue
					}
					exec := struct{ Command string }{}
					_ = ssh.Unmarshal(req.Payload, &exec)
					_ = req.Reply(true, nil)
					_, _ = io.WriteString(ch, exec.Command+"\n")
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))

					return
				}
			}()
		case "direct-tcpip":
			fwd := struct {
				Host     string
				Port     uint32
				OrigHost string
				OrigPort uint32
			}{}
			_ = ssh.Unmarshal(nc.ExtraData(), &fwd)
			target, err := (&net.Dialer{}).Dial("tcp", net.JoinHostPort(fwd.Host, strconv.FormatUint(uint64(fwd.Port), 10)))
			if err != nil {
				_ = nc.Reject(ssh.ConnectionFailed, err.Error())

				continue
			}
			ch, reqs, err := nc.Accept()
			if err != nil {
				_ = target.Close()

				continue
			}
			go ssh.DiscardRequests(reqs)
			go func() {
				_, _ = io.Copy(ch, target)
				_ = ch.Close()
			}()
			go func() {
				_, _ = io.Copy(target, ch)
				_ = target.Close()
			}()
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "")
		}
	}
}

func clientKey(t *testing.T) string {
	t.Helper()

	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(key, "")
	require.NoError(t, err)

	return string(pem.EncodeToMemory(block))
}

func TestRun(t *testing.T) {
	t.Parallel()

	cfg := &sshutil.Config{Remote: echoServer(t), Password: "x"}

	out, _, err := cfg.Run(t.Context(), "hedgehog")
	require.NoError(t, err)
	require.Equal(t, "hedgehog\n", out)
}

func TestRunThroughProxy(t *testing.T) {
	t.Parallel()

	proxy := echoServer(t)
	cfg := &sshutil.Config{Remote: echoServer(t), Proxy: &proxy, SSHKey: clientKey(t)}

	out, _, err := cfg.Run(t.Context(), "hedgehog")
	require.NoError(t, err)
	require.Equal(t, "hedgehog\n", out)
}

func TestRunHonoursContextDuringHandshake(t *testing.T) {
	t.Parallel()

	cfg := &sshutil.Config{Remote: silentListener(t), Password: "x"}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := cfg.Run(ctx, "true")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestRunHonoursSSHTimeoutThroughProxy(t *testing.T) {
	t.Parallel()

	proxy := silentListener(t)
	cfg := &sshutil.Config{Remote: sshutil.Remote{User: "core", Host: "10.0.0.1", Port: 22}, Proxy: &proxy, Password: "x", SSHTimeout: 200 * time.Millisecond}

	start := time.Now()
	_, _, err := cfg.Run(t.Context(), "true")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestNewSftpHonoursContextDuringHandshake(t *testing.T) {
	t.Parallel()

	cfg := &sshutil.Config{Remote: silentListener(t), Password: "x"}

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, cleanup, err := cfg.NewSftp(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, cleanup)
	require.Less(t, time.Since(start), 5*time.Second)
}
