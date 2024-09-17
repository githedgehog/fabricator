package comp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	helmapi "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	coreapi "k8s.io/api/core/v1"
	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	ClusterDomain        = "cluster.local"
	Namespace            = "fab"
	RegPrefix            = "githedgehog"
	FabCAIssuer          = "fab-ca"
	FabCACertificate     = FabCAIssuer
	FabCASecret          = FabCAIssuer
	FabCAConfigMap       = FabCAIssuer
	RegistryAdminSecret  = "registry-admin"
	RegistryWriterSecret = "registry-writer"
	RegistryReaderSecret = "registry-reader" // TODO secret type should be "kubernetes.io/basic-auth"
)

// TODO local test with the fake client incl components

type (
	KubeInstall func(cfg fabapi.Fabricator) ([]client.Object, error)

	LocalObjectReference  = coreapi.LocalObjectReference
	CMObjectReference     = cmmeta.ObjectReference
	IssuerSpec            = cmapi.IssuerSpec
	IssuerConfig          = cmapi.IssuerConfig
	SelfSignedIssuer      = cmapi.SelfSignedIssuer
	CAIssuer              = cmapi.CAIssuer
	CertificateSpec       = cmapi.CertificateSpec
	CertificatePrivateKey = cmapi.CertificatePrivateKey
	ServiceSpec           = coreapi.ServiceSpec
	ServicePort           = coreapi.ServicePort
)

const (
	ServiceTypeClusterIP    = coreapi.ServiceTypeClusterIP
	ServiceTypeNodePort     = coreapi.ServiceTypeNodePort
	ServiceTypeLoadBalancer = coreapi.ServiceTypeLoadBalancer
	ServiceTypeExternalName = coreapi.ServiceTypeExternalName
)

const (
	ProtocolTCP  = coreapi.ProtocolTCP
	ProtocolUDP  = coreapi.ProtocolUDP
	ProtocolSCTP = coreapi.ProtocolSCTP
)

var ErrUnsupportedKind = fmt.Errorf("unsupported kind")

func EnforceKubeInstall(ctx context.Context, kube client.Client, cfg fabapi.Fabricator, depls ...KubeInstall) error {
	// TODO support retries and backoff: https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/client-go/util/retry/util.go#L103

	for _, depl := range depls {
		objs, err := depl(cfg)
		if err != nil {
			return err
		}

		for _, obj := range objs {
			res, err := CreateOrUpdate(ctx, kube, obj)
			if err != nil {
				return fmt.Errorf("creating or updating %s %s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
			}

			// TODO log if something changed?
			slog.Debug("Enforced", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName(), "result", res)
		}
	}

	return nil
}

func Duration(d time.Duration) *metaapi.Duration {
	return &metaapi.Duration{Duration: d}
}

func HelmChart(cfg fabapi.Fabricator, name, chart, version string, abortOnFail bool, values string) client.Object {
	failurePolicy := ""
	if abortOnFail {
		failurePolicy = "abort"
	}

	var auth, ca *LocalObjectReference

	if !cfg.Spec.IsBootstrap {
		auth = &LocalObjectReference{
			Name: RegistryReaderSecret,
		}

		ca = &LocalObjectReference{
			Name: FabCAConfigMap,
		}
	}

	return &helmapi.HelmChart{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: helmapi.SchemeGroupVersion.String(),
			Kind:       "HelmChart",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		Spec: helmapi.HelmChartSpec{
			Chart:           ChartURL(cfg, chart, version),
			Version:         version,
			TargetNamespace: Namespace,
			CreateNamespace: true,
			FailurePolicy:   failurePolicy,
			AuthSecret:      auth,
			// AuthPassCredentials:  true, // TODO do we need it?
			// DockerRegistrySecret: &comp.LocalObjectReference{}, // TODO it maybe required for OCI registry
			RepoCAConfigMap: ca,
			ValuesContent:   values,
		},
	}
}

func Issuer(name string, spec cmapi.IssuerSpec) client.Object {
	return &cmapi.Issuer{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: cmapi.SchemeGroupVersion.String(),
			Kind:       "Issuer",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		Spec: spec,
	}
}

func Certificate(name string, spec CertificateSpec) client.Object {
	return &cmapi.Certificate{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: cmapi.SchemeGroupVersion.String(),
			Kind:       "Certificate",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		Spec: spec,
	}
}

func IssuerRef(name string) CMObjectReference {
	return CMObjectReference{
		Group: "cert-manager.io",
		Kind:  "Issuer",
		Name:  name,
	}
}

func Secret(name string, data map[string]string) client.Object {
	// TODO base64 encode data and Data instead of StringData so DeepEqual works correctly

	return &coreapi.Secret{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Secret",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		StringData: data,
	}
}

func ConfigMap(name string, data map[string]string) client.Object {
	return &coreapi.ConfigMap{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "ConfigMap",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		Data: data,
	}
}

func Service(name string, spec coreapi.ServiceSpec) client.Object {
	return &coreapi.Service{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Service",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		Spec: spec,
	}
}

func CreateOrUpdate(ctx context.Context, kube client.Client, obj client.Object) (ctrlutil.OperationResult, error) {
	var res ctrlutil.OperationResult
	var err error

	// TODO inject fabricator gen and/or hash labels

	switch obj := obj.(type) {
	case *helmapi.HelmChart:
		tmp := &helmapi.HelmChart{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Spec = obj.Spec

			return nil
		})
	case *cmapi.Certificate:
		tmp := &cmapi.Certificate{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Spec = obj.Spec

			return nil
		})
	case *coreapi.Secret:
		tmp := &coreapi.Secret{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Data = obj.Data
			tmp.StringData = obj.StringData
			tmp.Type = obj.Type

			return nil
		})
	case *coreapi.ConfigMap:
		tmp := &coreapi.ConfigMap{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Data = obj.Data

			return nil
		})
	default:
		return ctrlutil.OperationResultNone, fmt.Errorf("%T: %w", obj, ErrUnsupportedKind)
	}

	if err != nil {
		return ctrlutil.OperationResultNone, fmt.Errorf("creating or updating object: %w", err)
	}

	return res, nil
}
