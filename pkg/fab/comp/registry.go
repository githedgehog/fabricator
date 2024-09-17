package comp

import (
	"fmt"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
)

const (
	RegistryPort   = 31000
	RegistryPrefix = "githedgehog"
	OCISchema      = "oci://"

	BootstrapImageRepo    = "ghcr.io"
	BootstrapStatic       = "https://%{KUBERNETES_API}%/static"
	BootstrapStaticPrefix = "fab"
	BootstrapStaticCharts = BootstrapStatic + BootstrapStaticPrefix
)

func RegistryURL(cfg fabapi.Fabricator) string {
	return fmt.Sprintf("%s:%d", cfg.Spec.Config.Control.VIP, RegistryPort)
}

func ImageURL(cfg fabapi.Fabricator, name string) string {
	// TODO custom build image archives so we don't need to custom handle it here?
	if cfg.Spec.IsBootstrap {
		return joinURLParts(BootstrapImageRepo, RegistryPrefix, name)
	}

	return joinURLParts(RegistryURL(cfg), RegistryPrefix, name)
}

func ChartURL(cfg fabapi.Fabricator, name, ver string) string {
	if cfg.Spec.IsBootstrap {
		ver = strings.TrimPrefix(ver, "v")

		if strings.Contains(name, "/") {
			name = name[strings.LastIndex(name, "/")+1:]
		}

		return joinURLParts(BootstrapStaticCharts, fmt.Sprintf("%s-%s.tgz", name, ver))
	}

	return OCISchema + joinURLParts(RegistryURL(cfg), RegistryPrefix, name)
}

func joinURLParts(parts ...string) string {
	for i, part := range parts {
		parts[i] = strings.Trim(part, "/")
	}

	return strings.Join(parts, "/")
}
