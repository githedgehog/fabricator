package computil

import (
	"context"
	"fmt"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	helmapi "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	coreapi "k8s.io/api/core/v1"
	metaapi "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TODO some default namespace const
const Namespace = "fab"

// TODO local test with the fake client incl components
func HelmChart(name string, spec helmapi.HelmChartSpec) client.Object {
	return &helmapi.HelmChart{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: helmapi.SchemeGroupVersion.String(),
			Kind:       "HelmChart",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		Spec: spec,
	}
}

func Certificate(name string, spec cmapi.CertificateSpec) client.Object {
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

func Secret(name string, typ coreapi.SecretType, data map[string]string) client.Object {
	return &coreapi.Secret{
		TypeMeta: metaapi.TypeMeta{
			APIVersion: coreapi.SchemeGroupVersion.String(),
			Kind:       "Secret",
		},
		ObjectMeta: metaapi.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
		},
		Type:       typ,
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

var ErrUnsupportedKind = fmt.Errorf("unsupported kind")

func CreateOrUpdate(ctx context.Context, kube client.Client, obj client.Object) (ctrlutil.OperationResult, error) {
	var res ctrlutil.OperationResult
	var err error

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
