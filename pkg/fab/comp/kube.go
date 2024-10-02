package comp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	helmapi "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1alpha2"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	appsapi "k8s.io/api/apps/v1"
	coreapi "k8s.io/api/core/v1"
	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const (
	ClusterDomain  = "cluster.local"
	FabName        = "default"
	FabNamespace   = "fab"
	RegPrefix      = "githedgehog"
	FabCAIssuer    = "fab-ca"
	FabCASecret    = FabCAIssuer
	FabCAConfigMap = FabCAIssuer // changing name will break fabric manager

	RegistryUserAdmin              = "admin"
	RegistryUserWriter             = "writer"
	RegistryUserReader             = "reader"
	RegistryUserSecretPrefix       = "registry-user-"
	RegistryUserSecretDockerSuffix = "-docker"
	RegistryUserAdminSecret        = RegistryUserSecretPrefix + RegistryUserAdmin
	RegistryUserWriterSecret       = RegistryUserSecretPrefix + RegistryUserWriter
	RegistryUserReaderSecret       = RegistryUserSecretPrefix + RegistryUserReader
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
	Node                  = coreapi.Node
	Deployment            = appsapi.Deployment
	Issuer                = cmapi.Issuer
	SecretType            = coreapi.SecretType
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

const (
	NodeReady            = coreapi.NodeReady
	DeploymentAvailable  = appsapi.DeploymentAvailable
	IssuerConditionReady = cmapi.IssuerConditionReady
	ConditionTrue        = coreapi.ConditionTrue
	CMConditionTrue      = cmmeta.ConditionTrue
)

const (
	SecretTypeOpaque           SecretType = coreapi.SecretTypeOpaque
	SecretTypeBasicAuth        SecretType = coreapi.SecretTypeBasicAuth
	SecretTypeDockerConfigJSON SecretType = coreapi.SecretTypeDockerConfigJson
	BasicAuthUsernameKey                  = coreapi.BasicAuthUsernameKey
	BasicAuthPasswordKey                  = coreapi.BasicAuthPasswordKey
	DockerConfigJSONKey                   = coreapi.DockerConfigJsonKey
)

var (
	CoreAPISchemeBuilder = &scheme.Builder{
		GroupVersion:  coreapi.SchemeGroupVersion,
		SchemeBuilder: coreapi.SchemeBuilder,
	}
	AppsAPISchemeBuilder = &scheme.Builder{
		GroupVersion:  appsapi.SchemeGroupVersion,
		SchemeBuilder: appsapi.SchemeBuilder,
	}
	HelmAPISchemeBuilder = &scheme.Builder{
		GroupVersion:  helmapi.SchemeGroupVersion,
		SchemeBuilder: helmapi.SchemeBuilder,
	}
	CMApiSchemeBuilder = &scheme.Builder{
		GroupVersion:  cmapi.SchemeGroupVersion,
		SchemeBuilder: cmapi.SchemeBuilder,
	}
	CMMetaSchemeBuilder = &scheme.Builder{
		GroupVersion:  cmmeta.SchemeGroupVersion,
		SchemeBuilder: cmmeta.SchemeBuilder,
	}
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

func NewHelmChart(cfg fabapi.Fabricator, name, chart, version, bootstrapChart string, abortOnFail bool, values string) (client.Object, error) {
	failurePolicy := ""
	if abortOnFail {
		failurePolicy = "abort"
	}

	var auth, ca *LocalObjectReference
	if !cfg.Status.IsBootstrap {
		auth = &LocalObjectReference{
			Name: RegistryUserReaderSecret + RegistryUserSecretDockerSuffix,
		}
		ca = &LocalObjectReference{
			Name: FabCAConfigMap,
		}
	}

	chartURL, err := ChartURL(cfg, chart, bootstrapChart)
	if err != nil {
		return nil, fmt.Errorf("getting chart URL: %w", err)
	}

	return &helmapi.HelmChart{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: helmapi.SchemeGroupVersion.String(),
			Kind:       "HelmChart",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Spec: helmapi.HelmChartSpec{
			Chart:                chartURL,
			Version:              version,
			TargetNamespace:      FabNamespace,
			CreateNamespace:      true,
			FailurePolicy:        failurePolicy,
			DockerRegistrySecret: auth,
			RepoCAConfigMap:      ca,
			ValuesContent:        values,
		},
	}, nil
}

func NewIssuer(name string, spec cmapi.IssuerSpec) client.Object {
	return &cmapi.Issuer{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: cmapi.SchemeGroupVersion.String(),
			Kind:       "Issuer",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Spec: spec,
	}
}

func NewCertificate(name string, spec CertificateSpec) client.Object {
	return &cmapi.Certificate{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: cmapi.SchemeGroupVersion.String(),
			Kind:       "Certificate",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Spec: spec,
	}
}

func NewIssuerRef(name string) CMObjectReference {
	return CMObjectReference{
		Group: "cert-manager.io",
		Kind:  "Issuer",
		Name:  name,
	}
}

func NewNamespace(name string) client.Object {
	return &coreapi.Namespace{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Namespace",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name: name,
		},
	}
}

func NewSecret(name string, t SecretType, data map[string]string) client.Object {
	// TODO base64 encode data and Data instead of StringData so DeepEqual works correctly

	return &coreapi.Secret{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Secret",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		StringData: data,
		Type:       t,
	}
}

func NewConfigMap(name string, data map[string]string) client.Object {
	return &coreapi.ConfigMap{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "ConfigMap",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Data: data,
	}
}

func NewService(name string, spec coreapi.ServiceSpec) client.Object {
	return &coreapi.Service{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Service",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Spec: spec,
	}
}

func NewDHCPSubnet(name string, spec dhcpapi.DHCPSubnetSpec) client.Object {
	return &dhcpapi.DHCPSubnet{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: dhcpapi.GroupVersion.String(),
			Kind:       "DHCPSubnet",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: metaapi.NamespaceDefault,
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
	case *cmapi.Issuer:
		tmp := &cmapi.Issuer{ObjectMeta: obj.ObjectMeta}
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
	case *coreapi.Service:
		tmp := &coreapi.Service{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Spec = obj.Spec

			return nil
		})
	case *dhcpapi.DHCPSubnet:
		tmp := &dhcpapi.DHCPSubnet{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Spec = obj.Spec

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
