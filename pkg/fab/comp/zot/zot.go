package zot

import (
	_ "embed"
	"fmt"

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
	ChartRef        = "fabricator/charts/zot"
	ImageRef        = "fabricator/zot"
	AirgapRef       = "fabricator/zot-airgap"
	AirgapImageName = "zot-airgap-images-amd64.tar.gz"
	AirgapChartName = "zot-chart.tgz"
	Port            = 31000
	ServiceName     = "registry"
	TLSSecret       = "registry-tls"
	HtpasswdSecret  = "registry-htpasswd"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.Zot
}

//go:embed values.tmpl.yaml
var valuesTmpl string

//go:embed config.tmpl.json
var configTmpl string

var _ comp.KubeInstall = Install

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	version := string(cfg.Status.Versions.Platform.Zot)
	sync := false

	config, err := tmplutil.FromTemplate("config", configTmpl, map[string]any{
		"Sync": sync,
	})
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	repo, err := comp.ImageURL(cfg, ImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ImageRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo":           repo,
		"Tag":            version,
		"Port":           Port,
		"Config":         config,
		"HtpasswdSecret": HtpasswdSecret,
		"Sync":           sync,
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

	return []client.Object{
		comp.NewCertificate("registry", comp.CertificateSpec{
			DNSNames:    []string{fmt.Sprintf("%s.%s.svc.%s", ServiceName, comp.FabNamespace, comp.ClusterDomain)},
			IPAddresses: []string{controlVIP.Addr().String()},
			IssuerRef:   comp.NewIssuerRef(comp.FabCAIssuer),
			SecretName:  TLSSecret,
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
	users := map[string]string{}
	for _, user := range []string{comp.RegistryUserAdmin, comp.RegistryUserWriter, comp.RegistryUserReader} {
		passwd, err := password.Generate(32, 10, 10, false, true)
		if err != nil {
			return nil, fmt.Errorf("generating password: %w", err)
		}

		users[user] = passwd
	}

	return users, nil
}

func InstallUsers(users map[string]string) comp.KubeInstall {
	return func(_ fabapi.Fabricator) ([]client.Object, error) {
		objs := []client.Object{}

		htpasswd := ""
		for user, passwd := range users {
			hash, err := bcrypt.GenerateFromPassword([]byte(passwd), bcrypt.DefaultCost)
			if err != nil {
				return nil, fmt.Errorf("hashing password: %w", err)
			}
			htpasswd += fmt.Sprintf("%s:%s\n", user, hash)

			objs = append(objs, comp.NewSecret(comp.RegistryUserSecretPrefix+user, comp.SecretTypeBasicAuth, map[string]string{
				comp.BasicAuthUsernameKey: user,
				comp.BasicAuthPasswordKey: passwd,
			}))
		}

		return append(objs, comp.NewSecret(HtpasswdSecret, comp.SecretTypeOpaque, map[string]string{
			"htpasswd": htpasswd,
		})), nil
	}
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	version := string(Version(cfg))

	return comp.OCIArtifacts{
		ChartRef: version,
		ImageRef: version,
	}, nil
}
