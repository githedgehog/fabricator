// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bmc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrNoLocalAddr = errors.New("no local address in the BMC subnet")

// LocalAddrFor returns the local address in the BMC subnet, the BMC fetches the media from it
func LocalAddrFor(ctx context.Context, endpoint string) (netip.Addr, error) {
	return localAddrFor(ctx, endpoint, interfacePrefixes)
}

func localAddrFor(ctx context.Context, endpoint string, prefixes func() ([]netip.Prefix, error)) (netip.Addr, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parsing BMC endpoint %q: %w", endpoint, err)
	}

	host := u.Hostname()
	bmcs := []netip.Addr{}
	if bmc, err := netip.ParseAddr(host); err == nil {
		bmcs = append(bmcs, bmc)
	} else {
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("resolving BMC host %q: %w", host, err)
		}
		if len(addrs) == 0 {
			return netip.Addr{}, fmt.Errorf("resolving BMC host %q: no addresses", host) //nolint:err113
		}
		bmcs = addrs
	}

	local, err := prefixes()
	if err != nil {
		return netip.Addr{}, err
	}

	// any of the BMC addresses, the most specific subnet wins
	var found netip.Prefix
	for _, bmc := range bmcs {
		for _, prefix := range local {
			if prefix.Contains(bmc.Unmap()) && (!found.IsValid() || prefix.Bits() > found.Bits()) {
				found = prefix
			}
		}
	}
	if !found.IsValid() {
		return netip.Addr{}, fmt.Errorf("%s: %w", host, ErrNoLocalAddr)
	}

	return found.Addr(), nil
}

func interfacePrefixes() ([]netip.Prefix, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("listing interface addresses: %w", err)
	}

	prefixes := []netip.Prefix{}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		ip, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}

		ones, bits := ipNet.Mask.Size()
		if ip.Is4In6() && bits == 128 {
			ones -= 96
		}

		prefixes = append(prefixes, netip.PrefixFrom(ip.Unmap(), ones))
	}

	return prefixes, nil
}

// MediaPort is 80 as some BMCs (e.g. AMI) ignore the port in the media URL
const MediaPort = 80

func MediaURL(addr netip.Addr, name string) string {
	host := addr.String()
	if addr.Is6() {
		host = "[" + host + "]"
	}

	return "http://" + host + "/" + name
}

// healthPath answers with the server ID to tell it apart from anything else on the port
const healthPath = "_hhfab"

// mediaClient ignores the proxy from the environment, same as the BMC
var mediaClient = &http.Client{Transport: func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert
	transport.Proxy = nil

	return transport
}()}

// Serving checks the media server with the ID answers at the root URL, e.g. http://127.0.0.1/
func Serving(ctx context.Context, rootURL, id string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rootURL+healthPath, nil)
	if err != nil {
		return false
	}

	resp, err := mediaClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))

	return err == nil && resp.StatusCode == http.StatusOK && string(body) == id
}

func CheckMedia(ctx context.Context, mediaURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, mediaURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := mediaClient.Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", mediaURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("requesting %s: unexpected status %s", mediaURL, resp.Status) //nolint:err113
	}

	return nil
}

// Serve serves only the files (URL path to local path) on all IPv4 addresses until stopped; the returned context is
// done when the server stops, with the error as its cause if it failed
func Serve(ctx context.Context, port uint16, id string, files map[string]string) (uint16, context.Context, func(), error) {
	addr := netip.AddrPortFrom(netip.IPv4Unspecified(), port)
	// tcp would listen on IPv6 too, where the switches' ONIE probes the neighbors for its installer
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", addr.String())
	if err != nil {
		return 0, nil, nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		addr = tcpAddr.AddrPort()
	}

	srv := &http.Server{
		Handler:           mediaHandler(id, files),
		ReadHeaderTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithCancelCause(ctx)
	context.AfterFunc(ctx, func() {
		_ = srv.Close()
	})

	go func() {
		if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Media server failed", "addr", addr, "err", err)
			cancel(fmt.Errorf("serving media on %s: %w", addr, err))
		}
	}()

	return addr.Port(), ctx, func() { cancel(nil) }, nil
}

// readsFileEdges returns if the range reads the first and the last byte of the file
func readsFileEdges(rangeHeader string, size int64) (bool, bool) {
	ranges, ok := strings.CutPrefix(rangeHeader, "bytes=")
	if !ok {
		return true, true
	}

	first, last := false, false
	for rng := range strings.SplitSeq(ranges, ",") {
		start, end, ok := strings.Cut(strings.TrimSpace(rng), "-")
		if !ok {
			continue
		}

		// suffix range, e.g. -500 for the last 500 bytes
		if start == "" {
			last = true

			continue
		}

		first = first || start == "0"
		if endByte, err := strconv.ParseInt(end, 10, 64); end == "" || err == nil && endByte >= size-1 {
			last = true
		}
	}

	return first, last
}

func mediaHandler(id string, files map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == healthPath && r.Method == http.MethodGet {
			_, _ = io.WriteString(w, id)

			return
		}

		local, ok := files[name]
		if !ok || r.Method != http.MethodGet && r.Method != http.MethodHead {
			slog.Debug("Media server request rejected", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
			http.NotFound(w, r)

			return
		}

		f, err := os.Open(local)
		if errors.Is(err, os.ErrNotExist) {
			slog.Debug("Media server request for a missing file", "remote", r.RemoteAddr, "path", r.URL.Path, "file", local)
			http.NotFound(w, r)

			return
		}
		if err != nil {
			slog.Warn("Media server failed to open file", "path", local, "err", err)
			http.Error(w, "unavailable", http.StatusInternalServerError)

			return
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil {
			slog.Warn("Media server failed to stat file", "path", local, "err", err)
			http.Error(w, "unavailable", http.StatusInternalServerError)

			return
		}

		// BMCs read in small ranges, so only the first and the last byte reads are logged
		if r.Method == http.MethodGet {
			first, last := readsFileEdges(r.Header.Get("Range"), stat.Size())
			if first {
				slog.Info("Media read from the start", "remote", r.RemoteAddr, "file", name)
			}
			if last {
				slog.Info("Media read to the end", "remote", r.RemoteAddr, "file", name)
			}
		}

		http.ServeContent(w, r, name, stat.ModTime(), f)
	})
}
