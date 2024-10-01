package comp

import (
	"fmt"
	"maps"
	"net/netip"
	"strings"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp/k3s"
)

const (
	RegistryPort   = 31000
	RegistryPrefix = "githedgehog"
	OCISchema      = "oci://"

	BootstrapImageRepo = "ghcr.io"
	BootstrapStatic    = "https://%{KUBERNETES_API}%/static"
)

type OCIArtifacts map[string]meta.Version

type ListOCIArtifacts func(f fabapi.Fabricator) (OCIArtifacts, error)

func CollectArtifacts(cfg fabapi.Fabricator, lists ...ListOCIArtifacts) (OCIArtifacts, error) {
	res := OCIArtifacts{}

	for _, list := range lists {
		arts, err := list(cfg)
		if err != nil {
			return nil, fmt.Errorf("listing artifacts: %w", err)
		}

		maps.Copy(res, arts)
	}

	return res, nil
}

func RegistryURL(cfg fabapi.Fabricator) (string, error) {
	controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return "", fmt.Errorf("parsing control VIP: %w", err)
	}

	return netip.AddrPortFrom(controlVIP.Addr(), RegistryPort).String(), nil
}

func ImageURL(cfg fabapi.Fabricator, name string) (string, error) {
	// TODO custom build image archives so we don't need to custom handle it here?
	if cfg.Status.IsBootstrap {
		return JoinURLParts(BootstrapImageRepo, RegistryPrefix, name), nil
	}

	regURL, err := RegistryURL(cfg)
	if err != nil {
		return "", fmt.Errorf("getting registry URL: %w", err)
	}

	return JoinURLParts(regURL, RegistryPrefix, name), nil
}

func ChartURL(cfg fabapi.Fabricator, name, bootstrap string) (string, error) {
	if cfg.Status.IsBootstrap {
		if len(bootstrap) == 0 {
			return "", fmt.Errorf("bootstrap chart name is required") //nolint:goerr113
		}

		return JoinURLParts(BootstrapStatic, k3s.BootstrapChartsPrefix, bootstrap), nil
	}

	regURL, err := RegistryURL(cfg)
	if err != nil {
		return "", fmt.Errorf("getting registry URL: %w", err)
	}

	return OCISchema + JoinURLParts(regURL, RegistryPrefix, name), nil
}

func JoinURLParts(parts ...string) string {
	for i, part := range parts {
		parts[i] = strings.Trim(part, "/")
	}

	return strings.Join(parts, "/")
}
