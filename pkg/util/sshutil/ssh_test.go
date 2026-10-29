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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
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

// hangCommand makes echoServer close the command output and keep the session running.
const hangCommand = "hang"

// stallConn stops reading once stalled, like a jump host that hangs after accepting a forward.
type stallConn struct {
	net.Conn
	stalled atomic.Bool
	done    <-chan struct{}
}

func (c *stallConn) Read(b []byte) (int, error) {
	if c.stalled.Load() {
		<-c.done

		return 0, io.EOF
	}

	return c.Conn.Read(b) //nolint:wrapcheck
}

// echoServer is an SSH server accepting any credentials that echoes exec'd commands back and forwards direct-tcpip,
// or stalls on the forward if stall is set.
func echoServer(t *testing.T, stall bool) sshutil.Remote {
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
			go serveEcho(&stallConn{Conn: conn, done: t.Context().Done()}, config, stall)
		}
	}()

	addr := l.Addr().(*net.TCPAddr) //nolint:forcetypeassert

	return sshutil.Remote{User: "core", Host: "127.0.0.1", Port: uint(addr.Port)}
}

func serveEcho(conn *stallConn, config *ssh.ServerConfig, stall bool) {
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
					switch req.Type {
					case "exec":
						exec := struct{ Command string }{}
						_ = ssh.Unmarshal(req.Payload, &exec)
						_ = req.Reply(true, nil)
						_, _ = io.WriteString(ch, exec.Command+"\n")
						if exec.Command == hangCommand {
							// closes the output and keeps running
							_ = ch.CloseWrite()
							<-conn.done

							return
						}
						_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))

						return
					case "subsystem":
						_ = req.Reply(true, nil)
						_ = sftp.NewRequestServer(ch, sftp.InMemHandler()).Serve()

						return
					default:
						_ = req.Reply(false, nil)
					}
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
			if stall {
				conn.stalled.Store(true)

				continue
			}
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

	cfg := &sshutil.Config{Remote: echoServer(t, false), Password: "x"}

	out, _, err := cfg.Run(t.Context(), "hedgehog")
	require.NoError(t, err)
	require.Equal(t, "hedgehog\n", out)
}

func TestRunThroughProxy(t *testing.T) {
	t.Parallel()

	proxy := echoServer(t, false)
	cfg := &sshutil.Config{Remote: echoServer(t, false), Proxy: &proxy, SSHKey: clientKey(t)}

	out, _, err := cfg.Run(t.Context(), "hedgehog")
	require.NoError(t, err)
	require.Equal(t, "hedgehog\n", out)
}

func TestRunLongLine(t *testing.T) {
	t.Parallel()

	cfg := &sshutil.Config{Remote: echoServer(t, false), Password: "x"}

	line := strings.Repeat("x", 100*1024)
	out, _, err := cfg.Run(t.Context(), line)
	require.NoError(t, err)
	require.Equal(t, line+"\n", out)
}

func TestRunHonoursContextWhenCommandClosesOutput(t *testing.T) {
	t.Parallel()

	cfg := &sshutil.Config{Remote: echoServer(t, false), Password: "x"}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := cfg.Run(ctx, hangCommand)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestSftp(t *testing.T) {
	t.Parallel()

	cfg := &sshutil.Config{Remote: echoServer(t, false), Password: "x"}

	ftp, cleanup, err := cfg.NewSftp(t.Context())
	require.NoError(t, err)
	defer cleanup() //nolint:errcheck

	f, err := ftp.Create("/marker")
	require.NoError(t, err)
	_, err = f.Write([]byte("hedgehog"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	f, err = ftp.Open("/marker")
	require.NoError(t, err)
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "hedgehog", string(data))
}

func TestSftpClosedOnCancel(t *testing.T) {
	t.Parallel()

	cfg := &sshutil.Config{Remote: echoServer(t, false), Password: "x"}

	ctx, cancel := context.WithCancel(t.Context())
	ftp, cleanup, err := cfg.NewSftp(ctx)
	require.NoError(t, err)
	defer cleanup() //nolint:errcheck

	_, err = ftp.Stat("/")
	require.NoError(t, err)

	cancel()
	require.Eventually(t, func() bool {
		_, err := ftp.Stat("/")

		return err != nil
	}, 5*time.Second, 50*time.Millisecond)
}

func TestRunHonoursSSHTimeoutWhenProxyStalls(t *testing.T) {
	t.Parallel()

	proxy := echoServer(t, true)
	cfg := &sshutil.Config{Remote: echoServer(t, false), Proxy: &proxy, SSHKey: clientKey(t), SSHTimeout: 200 * time.Millisecond}

	start := time.Now()
	_, _, err := cfg.Run(t.Context(), "hedgehog")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
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
