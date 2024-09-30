package comp

import (
	"fmt"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
)

const (
	RegistryPort   = 31000
	RegistryPrefix = "githedgehog"
	OCISchema      = "oci://"

	BootstrapImageRepo = "ghcr.io"
	BootstrapStatic    = "https://%{KUBERNETES_API}%/static"
)

func RegistryURL(cfg fabapi.Fabricator) string {
	return fmt.Sprintf("%s:%d", cfg.Spec.Config.Control.VIP, RegistryPort)
}

func ImageURL(cfg fabapi.Fabricator, name string) string {
	// TODO custom build image archives so we don't need to custom handle it here?
	if cfg.Status.IsBootstrap {
		return joinURLParts(BootstrapImageRepo, RegistryPrefix, name)
	}

	return joinURLParts(RegistryURL(cfg), RegistryPrefix, name)
}

// TODO change API for proper err handling
func ChartURL(cfg fabapi.Fabricator, name, bootstrap string) string {
	if cfg.Status.IsBootstrap {
		if len(bootstrap) == 0 {
			return "<missing bootstrap chart>"
		}

		return joinURLParts(BootstrapStatic, k3s.BootstrapChartsPrefix, bootstrap)
	}

	return OCISchema + joinURLParts(RegistryURL(cfg), RegistryPrefix, name)
}

func joinURLParts(parts ...string) string {
	for i, part := range parts {
		parts[i] = strings.Trim(part, "/")
	}

	return strings.Join(parts, "/")
}
