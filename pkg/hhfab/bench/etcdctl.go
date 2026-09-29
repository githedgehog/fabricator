// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"context"
	"fmt"
	"strings"
)

// Where k3s keeps the etcd client certificates, and where etcd listens. The
// client port is bound to localhost on the control node, so anything talking to
// it has to run there.
const (
	etcdCertsDir = "/var/lib/rancher/k3s/server/tls/etcd"
	etcdEndpoint = "https://127.0.0.1:2379"
)

// etcdctlPaths are probed in order. The control node image ships etcdctl, but
// not always on PATH, and `k3s etcdctl` exists only on some k3s versions - so
// look rather than assume either way.
var etcdctlPaths = []string{
	"/usr/bin/etcdctl",
	"/usr/local/bin/etcdctl",
	"/opt/bin/etcdctl",
}

// findEtcdctl returns the path to etcdctl on the control node, or an empty
// string if there is none.
func findEtcdctl(ctx context.Context, run Runner) string {
	if out, err := run(ctx, "command -v etcdctl 2>/dev/null || true"); err == nil {
		if path := strings.TrimSpace(out); path != "" {
			return path
		}
	}

	for _, path := range etcdctlPaths {
		cmd := fmt.Sprintf("test -x %s && echo %s || true", path, path)
		if out, err := run(ctx, cmd); err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out)
		}
	}

	return ""
}

// etcdctl runs an etcdctl subcommand against the local etcd. sudo is required
// because the client key is root-only.
func etcdctl(ctx context.Context, run Runner, bin, args string) (string, error) {
	cmd := fmt.Sprintf(
		"sudo %s --endpoints=%s --cacert=%s/server-ca.crt --cert=%s/client.crt --key=%s/client.key %s 2>&1",
		bin, etcdEndpoint, etcdCertsDir, etcdCertsDir, etcdCertsDir, args)

	out, err := run(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("running etcdctl %s: %w", args, err)
	}

	return strings.TrimSpace(out), nil
}
