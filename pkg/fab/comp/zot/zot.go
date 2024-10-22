// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package zot

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sethvargo/go-password/password"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"golang.org/x/crypto/bcrypt"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartRef               = "fabricator/charts/zot"
	ImageRef               = "fabricator/zot"
	AirgapRef              = "fabricator/zot-airgap"
	AirgapImageName        = "zot-airgap-images-amd64.tar.gz"
	AirgapChartName        = "zot-chart.tgz"
	Port                   = 31000
	ServiceName            = "registry"
	TLSSecret              = "registry-tls"
	HtpasswdSecret         = "registry-htpasswd"
	UpstreamSecret         = "registry-upstream"
	UpstreamCredentialsKey = "credentials.json"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.Zot
}

//go:embed values.tmpl.yaml
var valuesTmpl string

//go:embed config.tmpl.json
var configTmpl string

var _ comp.KubeInstall = Install

func ImageURL(cfg fabapi.Fabricator) (string, error) {
	repo, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return "", fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}

	return repo, nil
}

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	version := string(Version(cfg))

	upstream := !cfg.Spec.Config.Registry.IsAirgap() && cfg.Spec.Config.Registry.Upstream != nil
	configOpts := map[string]any{
		"Upstream": upstream,
	}
	if upstream {
		configOpts["UpstreamURL"] = cfg.Spec.Config.Registry.Upstream.Repo
		configOpts["UpstreamPrefix"] = ""

		prefix := strings.Trim(cfg.Spec.Config.Registry.Upstream.Prefix, "/")
		if prefix != "" {
			prefix = "/" + prefix
		}
		configOpts["UpstreamPrefix"] = prefix + "/**"
		configOpts["UpstreamTLSVerify"] = strconv.FormatBool(!cfg.Spec.Config.Registry.Upstream.NoTLSVerify)
	}

	config, err := tmplutil.FromTemplate("config", configTmpl, configOpts)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	repo, err := ImageURL(cfg)

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo":           repo,
		"Tag":            version,
		"Port":           Port,
		"Config":         config,
		"HtpasswdSecret": HtpasswdSecret,
		"UpstreamSecret": UpstreamSecret,
		"Upstream":       upstream,
		"TLSSecret":      TLSSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	releaseName := "zot"

	controlVIP, err := cfg.Spec.Config.Control.VIP.Parse()
	if err != nil {
		return nil, fmt.Errorf("parsing control VIP: %w", err)
	}

	helmChart, err := comp.NewHelmChart(cfg, releaseName, ChartRef, version, AirgapChartName, false, values)
	if err != nil {
		return nil, fmt.Errorf("creating Helm chart: %w", err)
	}

	creds := map[string]any{}
	if upstream && cfg.Spec.Config.Registry.Upstream.Username != "" && cfg.Spec.Config.Registry.Upstream.Password != "" {
		creds[cfg.Spec.Config.Registry.Upstream.Repo] = map[string]string{
			"username": cfg.Spec.Config.Registry.Upstream.Username,
			"password": cfg.Spec.Config.Registry.Upstream.Password,
		}
	}
	upstreamCreds, err := json.Marshal(creds)
	if err != nil {
		return nil, fmt.Errorf("marshaling upstream credentials: %w", err)
	}

	return []client.Object{
		comp.NewCertificate("registry", comp.CertificateSpec{
			DNSNames:    []string{fmt.Sprintf("%s.%s.svc.%s", ServiceName, comp.FabNamespace, comp.ClusterDomain)},
			IPAddresses: []string{controlVIP.Addr().String()},
			IssuerRef:   comp.NewIssuerRef(comp.FabCAIssuer),
			SecretName:  TLSSecret,
		}),
		comp.NewSecret(UpstreamSecret, comp.SecretTypeOpaque, map[string]string{
			UpstreamCredentialsKey: string(upstreamCreds),
		}),
		helmChart,
		comp.NewService(ServiceName, comp.ServiceSpec{
			Selector: map[string]string{
				"app.kubernetes.io/instance": releaseName,
				"app.kubernetes.io/name":     releaseName,
			},
			Ports: []comp.ServicePort{
				{
					Name:       "zot",
					Port:       5000,
					TargetPort: intstr.FromString("zot"),
					Protocol:   comp.ProtocolTCP,
				},
			},
		}),
	}, nil
}

func NewUsers() (map[string]string, error) {
	gen, err := password.NewGenerator(&password.GeneratorInput{
		Symbols: "~!@#$%^&*()_+`-={}|[]\\:<>?,./",
	})
	if err != nil {
		return nil, fmt.Errorf("creating password generator: %w", err)
	}

	users := map[string]string{}
	for _, user := range []string{comp.RegistryUserAdmin, comp.RegistryUserWriter, comp.RegistryUserReader} {
		passwd, err := gen.Generate(32, 10, 0, false, true)
		if err != nil {
			return nil, fmt.Errorf("generating password: %w", err)
		}

		users[user] = passwd
	}

	return users, nil
}

func InstallUsers(users map[string]string) comp.KubeInstall {
	return func(cfg fabapi.Fabricator) ([]client.Object, error) {
		objs := []client.Object{}
		htpasswd := ""

		regURL, err := comp.RegistryURL(cfg)
		if err != nil {
			return nil, fmt.Errorf("getting registry URL: %w", err)
		}

		for user, passwd := range users {
			hash, err := bcrypt.GenerateFromPassword([]byte(passwd), bcrypt.DefaultCost)
			if err != nil {
				return nil, fmt.Errorf("hashing password: %w", err)
			}
			htpasswd += fmt.Sprintf("%s:%s\n", user, hash)

			dockerSecret := comp.RegistryUserSecretPrefix + user + comp.RegistryUserSecretDockerSuffix
			dockerCfg := map[string]any{
				"auths": map[string]any{
					regURL: map[string]string{
						"username": user,
						"password": passwd,
					},
				},
			}

			dockerCfgBytes, err := json.Marshal(dockerCfg)
			if err != nil {
				return nil, fmt.Errorf("marshaling Docker config: %w", err)
			}

			objs = append(objs,
				comp.NewSecret(comp.RegistryUserSecretPrefix+user, comp.SecretTypeBasicAuth, map[string]string{
					comp.BasicAuthUsernameKey: user,
					comp.BasicAuthPasswordKey: passwd,
				}),
				comp.NewSecret(dockerSecret, comp.SecretTypeDockerConfigJSON, map[string]string{
					comp.DockerConfigJSONKey: string(dockerCfgBytes),
				}),
			)
		}

		return append(objs, comp.NewSecret(HtpasswdSecret, comp.SecretTypeOpaque, map[string]string{
			"htpasswd": htpasswd,
		})), nil
	}
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	version := Version(cfg)

	return comp.OCIArtifacts{
		ChartRef: version,
		ImageRef: version,
	}, nil
}
