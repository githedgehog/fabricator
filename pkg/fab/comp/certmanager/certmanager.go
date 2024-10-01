package certmanager

import (
	_ "embed"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/api/meta"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartRef                = "fabricator/charts/cert-manager"
	ControllerImageRef      = "fabricator/cert-manager-controller"
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

var _ comp.KubeInstall = Install

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
		comp.NewHelmChart(cfg, "cert-manager", ChartRef, version, AirgapChartName, false, values),
	}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	version := string(Version(cfg))

	return comp.OCIArtifacts{
		ChartRef:                version,
		ControllerImageRef:      version,
		WebhookImageRef:         version,
		CAInjectorImageRef:      version,
		ACMESolverImageRef:      version,
		StartupAPICheckImageRef: version,
	}, nil
}
