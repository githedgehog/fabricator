package certmanager

import (
	_ "embed"
	"fmt"
	"time"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartName                = "fabricator/charts/cert-manager"
	ControllerImageName      = "fabricator/cert-manager"
	WebhookImageName         = "fabricator/cert-manager-webhook"
	CAInjectorImageName      = "fabricator/cert-manager-cainjector"
	ACMESolverImageName      = "fabricator/cert-manager-acmesolver"
	StartupAPICheckImageName = "fabricator/cert-manager-startupapicheck"
)

//go:embed values.tmpl.yaml
var valuesTmpl string

var (
	_ comp.KubeInstall = Install
	_ comp.KubeInstall = InstallFabCA
)

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	version := string(cfg.Status.Versions.Platform.CertManager)

	values, err := comp.FromTemplate("values", valuesTmpl, map[string]any{
		"ControllerRepo":      comp.ImageURL(cfg, ControllerImageName),
		"ControllerTag":       version,
		"WebhookRepo":         comp.ImageURL(cfg, WebhookImageName),
		"WebhookTag":          version,
		"CAInjectorRepo":      comp.ImageURL(cfg, CAInjectorImageName),
		"CAInjectorTag":       version,
		"ACMESolverRepo":      comp.ImageURL(cfg, ACMESolverImageName),
		"ACMESolverTag":       version,
		"StartupAPICheckRepo": comp.ImageURL(cfg, StartupAPICheckImageName),
		"StartupAPICheckTag":  version,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	return []client.Object{
		comp.HelmChart(cfg, "cert-manager", ChartName, version, false, values),
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
