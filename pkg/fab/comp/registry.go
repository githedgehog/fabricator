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

type OCIArtifacts map[string]string

type ListOCIArtifacts func(f fabapi.Fabricator) (OCIArtifacts, error)

func RegistryURL(cfg fabapi.Fabricator) (string, error) {
	controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return "", fmt.Errorf("parsing control VIP: %w", err)
	}

	return fmt.Sprintf("%s:%d", controlVIP.Addr().String(), RegistryPort), nil
}

func ImageURL(cfg fabapi.Fabricator, name string) (string, error) {
	// TODO custom build image archives so we don't need to custom handle it here?
	if cfg.Status.IsBootstrap {
		return joinURLParts(BootstrapImageRepo, RegistryPrefix, name), nil
	}

	regURL, err := RegistryURL(cfg)
	if err != nil {
		return "", fmt.Errorf("getting registry URL: %w", err)
	}

	return joinURLParts(regURL, RegistryPrefix, name), nil
}

func ChartURL(cfg fabapi.Fabricator, name, bootstrap string) (string, error) {
	if cfg.Status.IsBootstrap {
		if len(bootstrap) == 0 {
			return "", fmt.Errorf("bootstrap chart name is required")
		}

		return joinURLParts(BootstrapStatic, k3s.BootstrapChartsPrefix, bootstrap), nil
	}

	regURL, err := RegistryURL(cfg)
	if err != nil {
		return "", fmt.Errorf("getting registry URL: %w", err)
	}

	return OCISchema + joinURLParts(regURL, RegistryPrefix, name), nil
}

func joinURLParts(parts ...string) string {
	for i, part := range parts {
		parts[i] = strings.Trim(part, "/")
	}

	return strings.Join(parts, "/")
}
