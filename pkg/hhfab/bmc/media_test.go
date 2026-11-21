// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bmc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocalAddrFor(t *testing.T) {
	prefixes := func() ([]netip.Prefix, error) {
		return []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/8"),
			netip.MustParsePrefix("192.168.88.147/24"),
			netip.MustParsePrefix("10.0.0.10/8"),
			netip.MustParsePrefix("10.1.0.10/16"),
			netip.MustParsePrefix("fd00::10/64"),
		}, nil
	}

	for _, tt := range []struct {
		endpoint string
		want     string
		err      bool
	}{
		{endpoint: "https://192.168.88.10", want: "192.168.88.147"},
		{endpoint: "https://192.168.88.10:8443/redfish/v1", want: "192.168.88.147"},
		{endpoint: "https://10.2.0.1", want: "10.0.0.10"},
		{endpoint: "https://10.1.0.1", want: "10.1.0.10"},
		{endpoint: "https://[::ffff:192.168.88.10]", want: "192.168.88.147"},
		{endpoint: "https://[fd00::1]", want: "fd00::10"},
		// resolves to ::1 as well on most hosts, which has no subnet here
		{endpoint: "https://localhost", want: "127.0.0.1"},
		{endpoint: "https://192.0.2.1", err: true},
		{endpoint: "https://[fd01::1]", err: true},
		{endpoint: "://bad", err: true},
	} {
		t.Run(tt.endpoint, func(t *testing.T) {
			got, err := localAddrFor(t.Context(), tt.endpoint, prefixes)
			if tt.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, netip.MustParseAddr(tt.want), got)
		})
	}
}

func TestMediaURL(t *testing.T) {
	v4 := netip.MustParseAddr("192.168.88.147")
	v6 := netip.MustParseAddr("fd00::10")

	require.Equal(t, "http://192.168.88.147/node.iso", MediaURL(v4, "node.iso"))
	require.Equal(t, "http://[fd00::10]/node.iso", MediaURL(v6, "node.iso"))
}

func TestReadsFileEdges(t *testing.T) {
	for _, tt := range []struct {
		rng         string
		first, last bool
	}{
		{rng: "", first: true, last: true},
		{rng: "bytes=0-2047", first: true},
		{rng: "bytes=32768-34815"},
		{rng: "bytes=99000-99999", last: true},
		{rng: "bytes=99000-", last: true},
		{rng: "bytes=-500", last: true},
		{rng: "bytes=0-99,99900-99999", first: true, last: true},
	} {
		t.Run(tt.rng, func(t *testing.T) {
			first, last := readsFileEdges(tt.rng, 100000)
			require.Equal(t, tt.first, first, "first")
			require.Equal(t, tt.last, last, "last")
		})
	}
}

func TestMediaHandler(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "node.iso")
	require.NoError(t, os.WriteFile(local, []byte("0123456789"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret"), []byte("secret"), 0o600))

	srv := httptest.NewServer(mediaHandler("test-id", map[string]string{"node.iso": local, "missing.iso": filepath.Join(dir, "missing.iso")}))
	defer srv.Close()

	get := func(path, rng string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		require.NoError(t, err)
		if rng != "" {
			req.Header.Set("Range", rng)
		}

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return resp.StatusCode, string(body)
	}

	code, body := get("/node.iso", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "0123456789", body)

	code, body = get("/node.iso", "bytes=2-5")
	require.Equal(t, http.StatusPartialContent, code)
	require.Equal(t, "2345", body)

	for _, path := range []string{"/", "/secret", "/../secret", "/node.iso/", "/other.iso", "/missing.iso"} {
		code, _ = get(path, "")
		require.Equal(t, http.StatusNotFound, code, path)
	}

	code, body = get("/"+healthPath, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "test-id", body)
}

func TestServe(t *testing.T) {
	local := filepath.Join(t.TempDir(), "node.iso")
	require.NoError(t, os.WriteFile(local, []byte("iso"), 0o600))

	port, srvCtx, stop, err := Serve(t.Context(), 0, "test-id", map[string]string{"node.iso": local})
	require.NoError(t, err)
	require.NoError(t, srvCtx.Err())
	require.NotZero(t, port)
	base := "http://" + netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port).String()

	// no require inside, as Eventually runs it in another goroutine
	get := func() error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/node.iso", nil)
		if err != nil {
			return err //nolint:wrapcheck
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err //nolint:wrapcheck
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err //nolint:wrapcheck
		}
		if string(body) != "iso" {
			return fmt.Errorf("unexpected body %q", body) //nolint:err113
		}

		return nil
	}

	require.NoError(t, get())
	require.NoError(t, CheckMedia(t.Context(), base+"/node.iso"))
	require.Error(t, CheckMedia(t.Context(), base+"/other.iso"))
	require.True(t, Serving(t.Context(), base+"/", "test-id"))
	require.False(t, Serving(t.Context(), base+"/", "other-id"))

	stop()
	require.ErrorIs(t, srvCtx.Err(), context.Canceled)
	require.Eventually(t, func() bool { return get() != nil }, time.Second, 10*time.Millisecond)
	require.Error(t, CheckMedia(t.Context(), base+"/node.iso"))
	require.False(t, Serving(t.Context(), base+"/", "test-id"))
}
