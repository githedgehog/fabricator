// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"go.githedgehog.com/fabricator/pkg/fab/comp/certmanager"
)

const (
	// DockerConfigDir is passed as DOCKER_CONFIG by the toolbox to pull its image
	DockerConfigDir  = "/etc/hedgehog/docker"
	DockerConfigPath = DockerConfigDir + "/config.json"
	DockerCertsDir   = "/etc/docker/certs.d"
)

// EnforceDockerConfig lets docker pull from the control node registry, so the toolbox can pull its image from there:
// the docker config with the registry credentials is readable by the docker group and the registries get the Fab CA,
// which dockerd reads on every pull unlike the system ones.
func EnforceDockerConfig(cfg []byte) error {
	group, err := user.LookupGroup("docker")
	if err != nil {
		return fmt.Errorf("looking up docker group: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parsing docker group id %q: %w", group.Gid, err)
	}

	parsed := struct {
		Auths map[string]any `json:"auths"`
	}{}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		return fmt.Errorf("parsing docker config: %w", err)
	}

	if err := os.MkdirAll(DockerConfigDir, 0o755); err != nil {
		return fmt.Errorf("creating docker config dir: %w", err)
	}
	changed, err := enforceFile(DockerConfigPath, cfg, 0o640)
	if err != nil {
		return fmt.Errorf("enforcing docker config: %w", err)
	}
	if err := os.Chown(DockerConfigPath, 0, gid); err != nil {
		return fmt.Errorf("chown docker config: %w", err)
	}
	if changed {
		slog.Info("Docker config updated", "path", DockerConfigPath)
	}

	for host := range parsed.Auths {
		dir := filepath.Join(DockerCertsDir, host)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating docker certs dir: %w", err)
		}

		link := filepath.Join(dir, "ca.crt")
		if target, err := os.Readlink(link); err == nil && target == certmanager.FabCAPath {
			continue
		}
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing docker registry CA: %w", err)
		}
		if err := os.Symlink(certmanager.FabCAPath, link); err != nil {
			return fmt.Errorf("linking docker registry CA: %w", err)
		}
		slog.Info("Docker registry CA linked", "path", link)
	}

	return nil
}
