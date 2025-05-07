// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package comp

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	helmapi "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	appsapi "k8s.io/api/apps/v1"
	coreapi "k8s.io/api/core/v1"
	rbacapi "k8s.io/api/rbac/v1"
	apiextapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	metricsapi "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const (
	ClusterDomain              = "cluster.local"
	FabName                    = fabapi.FabName
	FabNamespace               = fabapi.FabNamespace
	RegPrefix                  = "githedgehog"
	FabCAIssuer                = "fab-ca"
	FabCASecret                = FabCAIssuer
	FabCAConfigMap             = FabCAIssuer // changing name will break fabric manager
	FabCAConfigMapKey          = "ca.crt"
	FabNodeRegistriesSecret    = "fab-node-registries" //nolint:gosec
	FabNodeRegistriesSecretKey = "registries.yaml"     //nolint:gosec

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
	KubeInstall func(cfg fabapi.Fabricator) ([]kclient.Object, error)

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
	NodeReady             = coreapi.NodeReady
	DeploymentAvailable   = appsapi.DeploymentAvailable
	DeploymentProgressing = appsapi.DeploymentProgressing
	IssuerConditionReady  = cmapi.IssuerConditionReady
	ConditionTrue         = coreapi.ConditionTrue
	ConditionFalse        = coreapi.ConditionFalse
	CMConditionTrue       = cmmeta.ConditionTrue
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
	RBACAPISchemeBuilder = &scheme.Builder{
		GroupVersion:  rbacapi.SchemeGroupVersion,
		SchemeBuilder: rbacapi.SchemeBuilder,
	}
	MetricsSchemeBuilder = &scheme.Builder{
		GroupVersion:  metricsapi.SchemeGroupVersion,
		SchemeBuilder: metricsapi.SchemeBuilder,
	}
	APIExtSchemeBuilder = &scheme.Builder{
		GroupVersion:  apiextapi.SchemeGroupVersion,
		SchemeBuilder: apiextapi.SchemeBuilder,
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

func EnforceKubeInstall(ctx context.Context, kube kclient.Client, cfg fabapi.Fabricator, depls ...KubeInstall) error {
	for _, depl := range depls {
		objs, err := depl(cfg)
		if err != nil {
			return err
		}

		for _, obj := range objs {
			kind := obj.GetObjectKind().GroupVersionKind().Kind
			name := obj.GetName()

			var res ctrlutil.OperationResult
			var err error

			backoff := wait.Backoff{
				Steps:    17,
				Duration: 500 * time.Millisecond,
				Factor:   1.5,
				Jitter:   0.1,
			}

			if !cfg.Status.IsInstall {
				backoff.Steps = 1
			}

			attempt := 0
			if err := retry.OnError(backoff, func(error) bool {
				return !kapierrors.IsConflict(err)
			}, func() error {
				if attempt > 0 {
					slog.Debug("Retrying create or update", "kind", kind, "name", name, "attempt", attempt)
				}

				attempt++

				res, err = CreateOrUpdate(ctx, kube, obj)
				if err != nil {
					return fmt.Errorf("creating or updating %s %s: %w", kind, name, err)
				}

				return nil
			}); err != nil {
				return fmt.Errorf("retrying create or update %s/%s: %w", kind, name, err)
			}

			slog.Debug("Enforced", "kind", kind, "name", name, "result", res)
		}
	}

	return nil
}

func Duration(d time.Duration) *kmetav1.Duration {
	return &kmetav1.Duration{Duration: d}
}

func NewHelmChart(cfg fabapi.Fabricator, name, chart, version, bootstrapChart string, abortOnFail bool, values string) (kclient.Object, error) {
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
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: helmapi.SchemeGroupVersion.String(),
			Kind:       "HelmChart",
		},
		ObjectMeta: kmetav1.ObjectMeta{
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

func NewIssuer(name string, spec cmapi.IssuerSpec) kclient.Object {
	return &cmapi.Issuer{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: cmapi.SchemeGroupVersion.String(),
			Kind:       "Issuer",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Spec: spec,
	}
}

func NewCertificate(name string, spec CertificateSpec) kclient.Object {
	return &cmapi.Certificate{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: cmapi.SchemeGroupVersion.String(),
			Kind:       "Certificate",
		},
		ObjectMeta: kmetav1.ObjectMeta{
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

func NewNamespace(name string) kclient.Object {
	return &coreapi.Namespace{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Namespace",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name: name,
		},
	}
}

func NewSecret(name string, t SecretType, data map[string]string) kclient.Object {
	// TODO base64 encode data and Data instead of StringData so DeepEqual works correctly

	return &coreapi.Secret{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Secret",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		StringData: data,
		Type:       t,
	}
}

func NewConfigMap(name string, data map[string]string) kclient.Object {
	return &coreapi.ConfigMap{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "ConfigMap",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Data: data,
	}
}

func NewService(name string, spec coreapi.ServiceSpec) kclient.Object {
	return &coreapi.Service{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Service",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Spec: spec,
	}
}

func NewDHCPSubnet(name string, spec dhcpapi.DHCPSubnetSpec) kclient.Object {
	return &dhcpapi.DHCPSubnet{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: dhcpapi.GroupVersion.String(),
			Kind:       "DHCPSubnet",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: kmetav1.NamespaceDefault,
		},
		Spec: spec,
	}
}

func NewDaemonSet(name string, spec appsapi.DaemonSetSpec) kclient.Object {
	return &appsapi.DaemonSet{
		TypeMeta: kmetav1.TypeMeta{
			APIVersion: appsapi.SchemeGroupVersion.String(),
			Kind:       "DaemonSet",
		},
		ObjectMeta: kmetav1.ObjectMeta{
			Name:      name,
			Namespace: FabNamespace,
		},
		Spec: spec,
	}
}

func CreateOrUpdate(ctx context.Context, kube kclient.Client, obj kclient.Object) (ctrlutil.OperationResult, error) {
	var res ctrlutil.OperationResult
	var err error

	// TODO inject fabricator gen and/or hash labels

	obj.SetGeneration(0)
	obj.SetResourceVersion("")

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
	case *appsapi.DaemonSet:
		tmp := &appsapi.DaemonSet{ObjectMeta: obj.ObjectMeta}
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
	case *fabapi.Fabricator:
		tmp := &fabapi.Fabricator{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Spec = obj.Spec

			return nil
		})
	case *fabapi.ControlNode:
		tmp := &fabapi.ControlNode{ObjectMeta: obj.ObjectMeta}
		res, err = ctrlutil.CreateOrUpdate(ctx, kube, tmp, func() error {
			tmp.Spec = obj.Spec

			return nil
		})
	case *fabapi.FabNode:
		tmp := &fabapi.FabNode{ObjectMeta: obj.ObjectMeta}
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

type KubeStatus func(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator) (fabapi.ComponentStatus, error)

func GetDeploymentStatus(name, container, image string) KubeStatus {
	return func(ctx context.Context, kube kclient.Reader, _ fabapi.Fabricator) (fabapi.ComponentStatus, error) {
		obj := &Deployment{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: FabNamespace}, obj); err != nil {
			if kapierrors.IsNotFound(err) {
				return fabapi.CompStatusNotFound, nil
			}

			return fabapi.CompStatusUnknown, fmt.Errorf("getting deployment %q: %w", name, err)
		}

		upToDate := false
		for _, cont := range obj.Spec.Template.Spec.Containers {
			if cont.Name == container && cont.Image == image {
				upToDate = true

				break
			}
		}

		if upToDate && obj.Status.UpdatedReplicas >= 1 {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == DeploymentProgressing && cond.Status == ConditionTrue {
					return fabapi.CompStatusReady, nil
				}
			}
		}

		return fabapi.CompStatusPending, nil
	}
}

func GetDaemonSetStatus(name, container, image string) KubeStatus {
	return func(ctx context.Context, kube kclient.Reader, _ fabapi.Fabricator) (fabapi.ComponentStatus, error) {
		obj := &appsapi.DaemonSet{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: FabNamespace}, obj); err != nil {
			if kapierrors.IsNotFound(err) {
				return fabapi.CompStatusNotFound, nil
			}

			return fabapi.CompStatusUnknown, fmt.Errorf("getting daemonset %q: %w", name, err)
		}

		upToDate := false
		for _, cont := range obj.Spec.Template.Spec.InitContainers {
			if cont.Name == container && cont.Image == image {
				upToDate = true

				break
			}
		}
		for _, cont := range obj.Spec.Template.Spec.Containers {
			if cont.Name == container && cont.Image == image {
				upToDate = true

				break
			}
		}

		if upToDate && obj.Status.ObservedGeneration == obj.Generation && obj.Status.UpdatedNumberScheduled == obj.Status.DesiredNumberScheduled {
			return fabapi.CompStatusReady, nil
		}

		return fabapi.CompStatusPending, nil
	}
}

func GetCRDStatus(name, version string) KubeStatus {
	return func(ctx context.Context, kube kclient.Reader, _ fabapi.Fabricator) (fabapi.ComponentStatus, error) {
		obj := &apiextapi.CustomResourceDefinition{}
		if err := kube.Get(ctx, kclient.ObjectKey{Name: name, Namespace: kmetav1.NamespaceDefault}, obj); err != nil {
			if kapierrors.IsNotFound(err) {
				return fabapi.CompStatusNotFound, nil
			}

			return fabapi.CompStatusUnknown, fmt.Errorf("getting crd %q: %w", name, err)
		}

		versionOk := false
		for _, v := range obj.Spec.Versions {
			if v.Name == version {
				versionOk = true
			}
		}

		if versionOk {
			for _, cond := range obj.Status.Conditions {
				if cond.Type == apiextapi.Established && cond.Status == apiextapi.ConditionTrue {
					return fabapi.CompStatusReady, nil
				}
			}
		}

		return fabapi.CompStatusPending, nil
	}
}

func MergeKubeStatuses(ctx context.Context, kube kclient.Reader, cfg fabapi.Fabricator, kubeStatus ...KubeStatus) (fabapi.ComponentStatus, error) {
	status := fabapi.CompStatusReady

	for _, statusFunc := range kubeStatus {
		kStatus, kErr := statusFunc(ctx, kube, cfg)
		if kErr != nil {
			return fabapi.CompStatusUnknown, kErr
		}

		status = minStatus(status, kStatus)
	}

	return status, nil
}

func minStatus(a, b fabapi.ComponentStatus) fabapi.ComponentStatus {
	aIdx := slices.Index(fabapi.ComponentStatuses, a)
	bIdx := slices.Index(fabapi.ComponentStatuses, b)

	if aIdx < bIdx {
		return a
	}

	return b
}
