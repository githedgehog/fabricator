package zot

import (
	_ "embed"
	"fmt"

	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ChartName      = "fabricator/charts/zot"
	ImageName      = "fabricator/zot"
	Port           = 31000
	ServiceName    = "registry"
	TLSSecret      = "registry-tls"
	AdminUsername  = "admin"
	WriterUsername = "writer"
	ReaderUsername = "reader"
)

//go:embed values.tmpl.yaml
var valuesTmpl string

//go:embed config.tmpl.json
var configTmpl string

var (
	_ comp.KubeInstall = InstallCert
	_ comp.KubeInstall = Install
)

func InstallCert(cfg fabapi.Fabricator) ([]client.Object, error) {
	return []client.Object{
		comp.Certificate("registry", comp.CertificateSpec{
			DNSNames:    []string{fmt.Sprintf("%s.%s.svc.%s", ServiceName, comp.Namespace, comp.ClusterDomain)},
			IPAddresses: []string{string(cfg.Spec.Config.Control.VIP)},
			IssuerRef:   comp.IssuerRef(comp.FabCAIssuer),
			SecretName:  TLSSecret,
		}),
	}, nil
}

func Install(cfg fabapi.Fabricator) ([]client.Object, error) {
	version := string(cfg.Status.Versions.Platform.Zot)
	sync := false

	config, err := comp.FromTemplate("config", configTmpl, map[string]any{
		"Sync": sync,
	})
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	users := []string{
		AdminUsername + ":" + cfg.Spec.Config.Registry.AdminPasswordHash,
		WriterUsername + ":" + cfg.Spec.Config.Registry.WriterPasswordHash,
		ReaderUsername + ":" + cfg.Spec.Config.Registry.ReaderPasswordHash,
	}

	values, err := comp.FromTemplate("values", valuesTmpl, map[string]any{
		"Repo":      comp.ImageURL(cfg, ImageName),
		"Tag":       version,
		"Port":      Port,
		"Config":    config,
		"Users":     users,
		"Sync":      sync,
		"TLSSecret": TLSSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}

	releaseName := "zot"

	return []client.Object{
		comp.HelmChart(cfg, releaseName, ChartName, version, false, values),
		comp.Service(ServiceName, comp.ServiceSpec{
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
