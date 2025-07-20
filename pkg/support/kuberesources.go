// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"

	"github.com/samber/lo"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	gwintapi "go.githedgehog.com/gateway/api/gwint/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metricsapi "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
	kyaml "sigs.k8s.io/yaml"
)

const (
	RedactedValue = "SUPPORT-DUMP-REDACTED"
)

var schemeBuilders = []*scheme.Builder{
	comp.CoreAPISchemeBuilder, comp.AppsAPISchemeBuilder, comp.RBACAPISchemeBuilder, comp.MetricsSchemeBuilder, comp.APIExtSchemeBuilder,
	comp.HelmAPISchemeBuilder,
	comp.CMApiSchemeBuilder, comp.CMMetaSchemeBuilder,
	wiringapi.SchemeBuilder, vpcapi.SchemeBuilder, dhcpapi.SchemeBuilder, agentapi.SchemeBuilder,
	fabapi.SchemeBuilder,
	gwapi.SchemeBuilder, gwintapi.SchemeBuilder,
}

var kubeResourceGVKs = []schema.GroupVersionKind{
	rbacv1.SchemeGroupVersion.WithKind(""),
	corev1.SchemeGroupVersion.WithKind("Node"),
	corev1.SchemeGroupVersion.WithKind("Namespace"),
	corev1.SchemeGroupVersion.WithKind("ServiceAccount"),
	corev1.SchemeGroupVersion.WithKind("Event"),
	corev1.SchemeGroupVersion.WithKind("Pod"),
	corev1.SchemeGroupVersion.WithKind("ConfigMap"),
	corev1.SchemeGroupVersion.WithKind("Secret"),
	corev1.SchemeGroupVersion.WithKind("Service"),
	corev1.SchemeGroupVersion.WithKind("Endpoints"),
	corev1.SchemeGroupVersion.WithKind("PersistentVolume"),
	corev1.SchemeGroupVersion.WithKind("PersistentVolumeClaim"),
	appsv1.SchemeGroupVersion.WithKind(""),
	apiextv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"),
	metricsapi.SchemeGroupVersion.WithKind(""),
	fabapi.GroupVersion.WithKind(""),
	wiringapi.GroupVersion.WithKind(""),
	vpcapi.GroupVersion.WithKind(""),
	dhcpapi.GroupVersion.WithKind(""),
	agentapi.GroupVersion.WithKind(""),
	gwapi.GroupVersion.WithKind(""),
	gwintapi.GroupVersion.WithKind(""),
}

type kubeResourceRedactorFunc func(kclient.Object)

var kubeResourceRedactors = map[schema.GroupVersionKind]kubeResourceRedactorFunc{
	corev1.SchemeGroupVersion.WithKind("Secret"): func(obj kclient.Object) {
		secret := obj.(*corev1.Secret)
		for k := range secret.Data {
			secret.Data[k] = []byte(RedactedValue)
		}
	},
	fabapi.GroupVersion.WithKind(fabapi.KindFabricator): func(obj kclient.Object) {
		fab := obj.(*fabapi.Fabricator)

		if fab.Spec.Config.Registry.Upstream != nil {
			fab.Spec.Config.Registry.Upstream.Password = RedactedValue
		}

		redactAlloyConfig(&fab.Spec.Config.Fabric.DefaultAlloyConfig)

		fab.Spec.Config.Control.DefaultUser.PasswordHash = RedactedValue
		fab.Spec.Config.Control.DefaultUser.AuthorizedKeys = lo.Map(fab.Spec.Config.Control.DefaultUser.AuthorizedKeys, func(_ string, _ int) string {
			return RedactedValue
		})

		for name, user := range fab.Spec.Config.Fabric.DefaultSwitchUsers {
			user.PasswordHash = RedactedValue
			user.AuthorizedKeys = lo.Map(user.AuthorizedKeys, func(_ string, _ int) string {
				return RedactedValue
			})
			fab.Spec.Config.Fabric.DefaultSwitchUsers[name] = user
		}

		if fab.Spec.Config.Control.JoinToken != "" {
			fab.Spec.Config.Control.JoinToken = RedactedValue
		}
	},
	agentapi.GroupVersion.WithKind(agentapi.KindAgent): func(obj kclient.Object) {
		agent := obj.(*agentapi.Agent)

		agent.Spec.Version.Password = RedactedValue
		redactAlloyConfig(&agent.Spec.Alloy)

		for name, user := range agent.Spec.Users {
			user.Password = RedactedValue
			user.SSHKeys = lo.Map(user.SSHKeys, func(_ string, _ int) string {
				return RedactedValue
			})
			agent.Spec.Users[name] = user
		}
	},
}

func redactAlloyConfig(alloy *meta.AlloyConfig) {
	for name, target := range alloy.PrometheusTargets {
		redactAlloyTarget(&target.AlloyTarget)
		alloy.PrometheusTargets[name] = target
	}
	for name, target := range alloy.LokiTargets {
		redactAlloyTarget(&target.AlloyTarget)
		alloy.LokiTargets[name] = target
	}
}

func redactAlloyTarget(target *meta.AlloyTarget) {
	if target.BasicAuth.Password != "" {
		target.BasicAuth.Password = RedactedValue
	}
	if target.BearerToken != "" {
		target.BearerToken = RedactedValue
	}
}

func collectKubeResources(ctx context.Context, dump *Dump) error {
	kube, err := kubeutil.NewClient(ctx, "", schemeBuilders...)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}

	resources := &bytes.Buffer{}
	if err := collectKubeObjects(ctx, kube, kube.Scheme(),
		kubeResourceGVKs, kubeResourceRedactors, resources); err != nil {
		return fmt.Errorf("collecting kube objects: %w", err)
	}

	dump.Resources = resources.String()

	return nil
}

func collectKubeObjects(ctx context.Context, kube kclient.Reader, scheme *runtime.Scheme,
	withGVKs []schema.GroupVersionKind, redactors map[schema.GroupVersionKind]kubeResourceRedactorFunc,
	w io.Writer,
) error {
	kubeObjListType := reflect.TypeOf((*kclient.ObjectList)(nil)).Elem()
	objs := 0

	for gvk, objType := range scheme.AllKnownTypes() {
		// skip deprecated resources
		if gvk.Kind == "Endpoints" {
			continue
		}

		// skip list types
		if reflect.PointerTo(objType).Implements(kubeObjListType) {
			continue
		}

		// skip options/events
		if strings.HasPrefix(objType.PkgPath(), "k8s.io/apimachinery/pkg/apis/meta") {
			continue
		}

		ok := len(withGVKs) == 0
		for _, withGVK := range withGVKs {
			if withGVK.Group != "" && withGVK.Group != gvk.Group {
				continue
			}
			if withGVK.Version != "" && withGVK.Version != gvk.Version {
				continue
			}
			if withGVK.Kind != "" && withGVK.Kind != gvk.Kind {
				continue
			}

			ok = true

			break
		}
		if !ok {
			continue
		}

		slog.Debug("Collecting Kube resource", "gvk", gvk.String())

		objListType, ok := scheme.AllKnownTypes()[schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind + "List",
		}]
		if !ok {
			continue
		}

		if !reflect.PointerTo(objListType).Implements(kubeObjListType) {
			return fmt.Errorf("list type for %s doesn't implement ObjectList", gvk.String()) //nolint:goerr113
		}

		objListValue := reflect.New(objListType)
		objList := objListValue.Interface().(kclient.ObjectList)

		if err := kube.List(ctx, objList); err != nil {
			if kmeta.IsNoMatchError(err) {
				slog.Debug("Skipping Kube resource, no match", "gvk", gvk.String(), "err", err)

				continue
			}

			if kapierrors.IsServiceUnavailable(err) {
				slog.Warn("Skipping Kube resource, service unavailable; please try re-running later", "gvk", gvk.String(), "err", err)

				continue
			}

			return fmt.Errorf("listing %s: %w", gvk.String(), err)
		}

		items := objListValue.Elem().FieldByName("Items")
		if items.Kind() != reflect.Slice {
			return fmt.Errorf("items field is not a slice in %s", gvk.String()) //nolint:goerr113
		}

		itemsLen := items.Len()
		for idx := 0; idx < itemsLen; idx++ {
			itemValue, ok := items.Index(idx).Addr().Interface().(kclient.Object)
			if !ok {
				return fmt.Errorf("item %d of %s list is not a client object", idx, gvk.String()) //nolint:goerr113
			}

			itemValue.GetObjectKind().SetGroupVersionKind(gvk)

			if redactors != nil {
				if redactor, ok := redactors[gvk]; ok {
					redactor(itemValue)
				}
			}

			if objs > 0 {
				if _, err := fmt.Fprintf(w, "---\n"); err != nil {
					return fmt.Errorf("writing separator: %w", err)
				}
			}
			objs++

			if err := printKubeObject(itemValue, w); err != nil {
				return fmt.Errorf("printing item %d of %s: %w", idx, gvk.String(), err)
			}
		}
	}

	return nil
}

func printKubeObject(obj kclient.Object, w io.Writer) error {
	obj.SetManagedFields(nil)
	delete(obj.GetAnnotations(), "kubectl.kubernetes.io/last-applied-configuration")

	buf, err := kyaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	_, err = w.Write(buf)
	if err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	return nil
}
