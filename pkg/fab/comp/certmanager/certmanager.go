package certmanager

import (
	_ "embed"
	"fmt"
	"time"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartRef                = "fabricator/charts/cert-manager"
	ControllerImageRef      = "fabricator/cert-manager"
	WebhookImageRef         = "fabricator/cert-manager-webhook"
	CAInjectorImageRef      = "fabricator/cert-manager-cainjector"
	ACMESolverImageRef      = "fabricator/cert-manager-acmesolver"
	StartupAPICheckImageRef = "fabricator/cert-manager-startupapicheck"
	AirgapRef               = "fabricator/cert-manager-airgap"
	AirgapImageName         = "cert-manager-airgap-images-amd64.tar.gz"
	AirgapChartName         = "cert-manager-chart.tgz"
)

func Version(f fabapi.Fabricator) meta.Version {
	return f.Status.Versions.Platform.CertManager
}

//go:embed values.tmpl.yaml
var valuesTmpl string

var (
	_ comp.KubeInstall   = Install
	_ comp.KubeInstall   = InstallFabCA
	_ comp.ListArtifacts = Artifacts
)

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	version := string(cfg.Status.Versions.Platform.CertManager)

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"ControllerRepo":      comp.ImageURL(cfg, ControllerImageRef),
		"ControllerTag":       version,
		"WebhookRepo":         comp.ImageURL(cfg, WebhookImageRef),
		"WebhookTag":          version,
		"CAInjectorRepo":      comp.ImageURL(cfg, CAInjectorImageRef),
		"CAInjectorTag":       version,
		"ACMESolverRepo":      comp.ImageURL(cfg, ACMESolverImageRef),
		"ACMESolverTag":       version,
		"StartupAPICheckRepo": comp.ImageURL(cfg, StartupAPICheckImageRef),
		"StartupAPICheckTag":  version,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	return []client.Object{
		comp.HelmChart(cfg, "cert-manager", ChartRef, version, false, values),
	}, nil
}

func InstallFabCA(_ fabapi.Fabricator) ([]client.Object, error) {
	bootstrapIssuer := comp.FabCACertificate + "-bootstrap"

	return []client.Object{
		comp.Issuer(bootstrapIssuer, comp.IssuerSpec{
			IssuerConfig: comp.IssuerConfig{
				SelfSigned: &comp.SelfSignedIssuer{},
			},
		}),
		comp.Certificate(comp.FabCACertificate, comp.CertificateSpec{
			IsCA:        true,
			Duration:    comp.Duration(87500 * time.Hour), // 10y
			RenewBefore: comp.Duration(78840 * time.Hour), // 9y
			CommonName:  "hedgehog-" + comp.FabCAIssuer,
			SecretName:  comp.FabCASecret,
			PrivateKey: &comp.CertificatePrivateKey{
				Algorithm: "ECDSA",
				Size:      256,
			},
			IssuerRef: comp.IssuerRef(bootstrapIssuer),
		}),
		comp.Issuer(comp.FabCAIssuer, comp.IssuerSpec{
			IssuerConfig: comp.IssuerConfig{
				CA: &comp.CAIssuer{
					SecretName: comp.FabCASecret,
				},
			},
		}),
	}, nil
}

func Artifacts(cfg fabapi.Fabricator) (comp.Artifacts, error) {
	version := string(cfg.Status.Versions.Platform.CertManager)

	return comp.Artifacts{
		AirgapOCISync: []string{
			ChartRef + ":" + version,
			ControllerImageRef + ":" + version,
			WebhookImageRef + ":" + version,
			CAInjectorImageRef + ":" + version,
			ACMESolverImageRef + ":" + version,
			StartupAPICheckImageRef + ":" + version,
		},
		BootstrapImages: []string{
			"fabricator/cert-manager-airgap:" + version,
		},
		BootstrapCharts: []string{
			"fabricator/cert-manager-chart:" + version,
		},
	}, nil
}
