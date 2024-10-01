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
	version := string(Version(cfg))

	controllerRepo, err := comp.ImageURL(cfg, ControllerImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ControllerImageRef, err)
	}

	webhookRepo, err := comp.ImageURL(cfg, WebhookImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", WebhookImageRef, err)
	}

	caInjectorRepo, err := comp.ImageURL(cfg, CAInjectorImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", CAInjectorImageRef, err)
	}

	acmeSolverRepo, err := comp.ImageURL(cfg, ACMESolverImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", ACMESolverImageRef, err)
	}

	startupAPICheckRepo, err := comp.ImageURL(cfg, StartupAPICheckImageRef)
	if err != nil {
		return nil, fmt.Errorf("getting image URL for %q: %w", StartupAPICheckImageRef, err)
	}

	values, err := tmplutil.FromTemplate("values", valuesTmpl, map[string]any{
		"ControllerRepo":      controllerRepo,
		"ControllerTag":       version,
		"WebhookRepo":         webhookRepo,
		"WebhookTag":          version,
		"CAInjectorRepo":      caInjectorRepo,
		"CAInjectorTag":       version,
		"ACMESolverRepo":      acmeSolverRepo,
		"ACMESolverTag":       version,
		"StartupAPICheckRepo": startupAPICheckRepo,
		"StartupAPICheckTag":  version,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	helmChart, err := comp.NewHelmChart(cfg, "cert-manager", ChartRef, version, AirgapChartName, false, values)
	if err != nil {
		return nil, fmt.Errorf("creating Helm chart: %w", err)
	}

	return []client.Object{helmChart}, nil
}

var _ comp.ListOCIArtifacts = Artifacts

func Artifacts(cfg fabapi.Fabricator) (comp.OCIArtifacts, error) {
	return comp.OCIArtifacts{
		ChartRef:                Version(cfg),
		ControllerImageRef:      Version(cfg),
		WebhookImageRef:         Version(cfg),
		CAInjectorImageRef:      Version(cfg),
		ACMESolverImageRef:      Version(cfg),
		StartupAPICheckImageRef: Version(cfg),
	}, nil
}
