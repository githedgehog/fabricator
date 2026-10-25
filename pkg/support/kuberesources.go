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
	"slices"
	"strconv"
	"strings"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	helmapi "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/samber/lo"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	dhcpapi "go.githedgehog.com/fabric/api/dhcp/v1beta1"
	fcintapi "go.githedgehog.com/fabric/api/fcint/v1alpha1"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	gwintapi "go.githedgehog.com/fabric/api/gwint/v1alpha1"
	fmeta "go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/libmeta/pkg/alloy"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	metricsapi "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

const (
	RedactedValue = "SUPPORT-DUMP-REDACTED"
)

// redactedAnnotations are removed from every collected object, as they hold a copy of the object's content and so
// bypass the per-kind redactors: kubectl's one is the plaintext object as last applied, wrangler's one (used by k3s
// and its helm-controller) is the object with every string cut to 64 characters, so base64 Secret data shorter than
// that survives in full
var redactedAnnotations = []string{
	"kubectl.kubernetes.io/last-applied-configuration",
	"objectset.rio.cattle.io/applied",
}

var schemeBuilders = []func(*runtime.Scheme) error{
	corev1.AddToScheme, appsv1.AddToScheme, rbacv1.AddToScheme, metricsapi.AddToScheme, apiextv1.AddToScheme,
	batchv1.AddToScheme, coordinationv1.AddToScheme, discoveryv1.AddToScheme, admissionregv1.AddToScheme,
	storagev1.AddToScheme, schedulingv1.AddToScheme,
	helmapi.AddToScheme,
	cmapi.AddToScheme, cmmeta.AddToScheme,
	wiringapi.AddToScheme, vpcapi.AddToScheme, dhcpapi.AddToScheme, agentapi.AddToScheme,
	fabapi.AddToScheme,
	gwapi.AddToScheme, gwintapi.AddToScheme,
	fcintapi.AddToScheme,
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
	batchv1.SchemeGroupVersion.WithKind(""),
	coordinationv1.SchemeGroupVersion.WithKind(""),
	discoveryv1.SchemeGroupVersion.WithKind(""),
	admissionregv1.SchemeGroupVersion.WithKind(""),
	storagev1.SchemeGroupVersion.WithKind(""),
	schedulingv1.SchemeGroupVersion.WithKind(""),
	helmapi.SchemeGroupVersion.WithKind(""),
	cmapi.SchemeGroupVersion.WithKind(""),
	fabapi.GroupVersion.WithKind(""),
	wiringapi.GroupVersion.WithKind(""),
	vpcapi.GroupVersion.WithKind(""),
	dhcpapi.GroupVersion.WithKind(""),
	agentapi.GroupVersion.WithKind(""),
	gwapi.GroupVersion.WithKind(""),
	gwintapi.GroupVersion.WithKind(""),
	fcintapi.GroupVersion.WithKind(""),
}

type kubeResourceRedactorFunc func(kclient.Object)

var kubeResourceRedactors = map[schema.GroupVersionKind]kubeResourceRedactorFunc{
	corev1.SchemeGroupVersion.WithKind("Secret"): func(obj kclient.Object) {
		secret := obj.(*corev1.Secret)
		for k := range secret.Data {
			secret.Data[k] = []byte(RedactedValue)
		}
	},
	corev1.SchemeGroupVersion.WithKind("ConfigMap"): func(obj kclient.Object) {
		cm := obj.(*corev1.ConfigMap)

		// all values, as rendered configs could hold credentials, e.g. the fabric controller and alloy ones
		// TODO make it more granular, e.g. by parsing the known configs and redacting them by type
		for k := range cm.Data {
			cm.Data[k] = RedactedValue
		}
		for k := range cm.BinaryData {
			cm.BinaryData[k] = []byte(RedactedValue)
		}
	},
	// values hold the rendered component configs, e.g. alloy one has the target credentials
	helmapi.SchemeGroupVersion.WithKind("HelmChart"): func(obj kclient.Object) {
		chart := obj.(*helmapi.HelmChart)

		if chart.Spec.ValuesContent != "" {
			chart.Spec.ValuesContent = RedactedValue
		}
		if chart.Spec.Values != nil {
			chart.Spec.Values = redactedJSON()
		}
		for k := range chart.Spec.Set {
			chart.Spec.Set[k] = intstr.FromString(RedactedValue)
		}
	},
	helmapi.SchemeGroupVersion.WithKind("HelmChartConfig"): func(obj kclient.Object) {
		chartCfg := obj.(*helmapi.HelmChartConfig)

		if chartCfg.Spec.ValuesContent != "" {
			chartCfg.Spec.ValuesContent = RedactedValue
		}
		if chartCfg.Spec.Values != nil {
			chartCfg.Spec.Values = redactedJSON()
		}
	},
}

func redactedJSON() *apiextv1.JSON {
	return &apiextv1.JSON{Raw: []byte(strconv.Quote(RedactedValue))}
}

type typeRedactor struct {
	typ    reflect.Type
	redact func(reflect.Value)
}

func redactType[T any](redact func(*T)) typeRedactor {
	return typeRedactor{
		typ: reflect.TypeFor[T](),
		redact: func(v reflect.Value) {
			if val, ok := reflect.TypeAssert[*T](v); ok {
				redact(val)
			}
		},
	}
}

// typeRedactors redact the types holding credentials wherever they show up in a collected object, as most of them
// are copied around, e.g. the alloy config goes from the Fabricator through the fabric controller config into every
// Agent. Empty values are kept as is, so it's still visible whether a credential is set.
var typeRedactors = lo.SliceToMap([]typeRedactor{
	redactType(func(target *alloy.Target) {
		if target.BasicAuth != nil {
			redactString(&target.BasicAuth.Password)
		}
		redactString(&target.BearerToken)
	}),
	redactType(func(scrape *alloy.Scrape) {
		for k := range scrape.HTTPHeaders {
			scrape.HTTPHeaders[k] = RedactedValue
		}
	}),
	redactType(func(target *fmeta.AlloyTarget) {
		redactString(&target.BasicAuth.Password)
		redactString(&target.BearerToken)
	}),
	redactType(func(user *fmeta.UserCreds) {
		redactString(&user.Password)
		redactStrings(user.SSHKeys)
	}),
	redactType(func(cfg *fabapi.ControlConfig) {
		redactString(&cfg.JoinToken)
	}),
	redactType(func(user *fabapi.ControlUser) {
		redactString(&user.PasswordHash)
		redactStrings(user.AuthorizedKeys)
	}),
	redactType(func(user *fabapi.SwitchUser) {
		redactString(&user.PasswordHash)
		redactStrings(user.AuthorizedKeys)
	}),
	redactType(func(upstream *fabapi.ControlConfigRegistryUpstream) {
		redactString(&upstream.Password)
	}),
	redactType(func(version *agentapi.AgentVersion) {
		redactString(&version.Password)
	}),
	redactType(func(user *agentapi.UserCreds) {
		redactString(&user.Password)
		redactStrings(user.SSHKeys)
	}),
	redactType(func(keystore *cmapi.JKSKeystore) {
		if keystore.Password != nil {
			redactString(keystore.Password)
		}
	}),
	redactType(func(keystore *cmapi.PKCS12Keystore) {
		if keystore.Password != nil {
			redactString(keystore.Password)
		}
	}),
}, func(r typeRedactor) (reflect.Type, func(reflect.Value)) {
	return r.typ, r.redact
})

func redactString(value *string) {
	if *value != "" {
		*value = RedactedValue
	}
}

func redactStrings(values []string) {
	for idx := range values {
		redactString(&values[idx])
	}
}

// mayHoldStruct reports whether a value of the type can have a struct inside. The ones that can't, e.g. Secret data
// bytes or labels, are skipped by the walk, as there is nothing to redact in them and going through them value by
// value is slow.
func mayHoldStruct(typ reflect.Type) bool {
	switch typ.Kind() { //nolint:exhaustive // the rest are basic values
	case reflect.Struct, reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}

// redactTypes runs the type redactors on v and on everything reachable from it through exported fields, pointers,
// slices, arrays and map values
func redactTypes(v reflect.Value) {
	switch v.Kind() { //nolint:exhaustive // only the kinds that can hold a struct
	case reflect.Pointer:
		if !v.IsNil() {
			redactTypes(v.Elem())
		}
	case reflect.Struct:
		if redact, ok := typeRedactors[v.Type()]; ok && v.CanAddr() {
			redact(v.Addr())
		}
		for idx := range v.NumField() {
			if v.Type().Field(idx).IsExported() {
				redactTypes(v.Field(idx))
			}
		}
	case reflect.Slice, reflect.Array:
		if !mayHoldStruct(v.Type().Elem()) {
			return
		}
		for idx := range v.Len() {
			redactTypes(v.Index(idx))
		}
	case reflect.Map:
		if !mayHoldStruct(v.Type().Elem()) {
			return
		}
		// map values can't be changed in place, so they're redacted as a copy put back under the same key
		iter := v.MapRange()
		for iter.Next() {
			val := reflect.New(v.Type().Elem()).Elem()
			val.Set(iter.Value())
			redactTypes(val)
			v.SetMapIndex(iter.Key(), val)
		}
	}
}

func (c collector) collectKubeResources(ctx context.Context, dump *Dump) error {
	kube, err := kubeutil.NewClient(ctx, c.kubeconfigPath, schemeBuilders...)
	if err != nil {
		return fmt.Errorf("creating kube client: %w", err)
	}

	resources := &bytes.Buffer{}
	if err := c.collectKubeObjects(ctx, kube, kube.Scheme(), kubeResourceGVKs, resources); err != nil {
		return fmt.Errorf("collecting kube objects: %w", err)
	}

	dump.Resources = resources.String()

	return nil
}

// matchesGVK reports whether gvk is selected by any of withGVKs, the empty fields of which match anything, or by
// none of them being set at all
func matchesGVK(gvk schema.GroupVersionKind, withGVKs []schema.GroupVersionKind) bool {
	if len(withGVKs) == 0 {
		return true
	}

	return slices.ContainsFunc(withGVKs, func(with schema.GroupVersionKind) bool {
		return (with.Group == "" || with.Group == gvk.Group) &&
			(with.Version == "" || with.Version == gvk.Version) &&
			(with.Kind == "" || with.Kind == gvk.Kind)
	})
}

func (c collector) collectKubeObjects(ctx context.Context, kube kclient.Reader, scheme *runtime.Scheme,
	withGVKs []schema.GroupVersionKind, w io.Writer,
) error {
	kubeObjListType := reflect.TypeFor[kclient.ObjectList]()
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

		if !matchesGVK(gvk, withGVKs) {
			continue
		}

		if !c.quiet {
			slog.Debug("Collecting Kube resource", "gvk", gvk.String())
		}

		objListType, ok := scheme.AllKnownTypes()[schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind + "List",
		}]
		if !ok {
			continue
		}

		if !reflect.PointerTo(objListType).Implements(kubeObjListType) {
			return fmt.Errorf("list type for %s doesn't implement ObjectList", gvk.String()) //nolint:err113
		}

		objListValue := reflect.New(objListType)
		objList := objListValue.Interface().(kclient.ObjectList)

		if err := retry.OnError(longBackoff, func(error) bool { return ctx.Err() == nil }, func() error {
			if err := kube.List(ctx, objList); err != nil {
				if kmeta.IsNoMatchError(err) {
					return nil
				}

				return fmt.Errorf("listing %s: %w", gvk.String(), err)
			}

			return nil
		}); err != nil {
			return fmt.Errorf("retrying: %w", err)
		}

		items := objListValue.Elem().FieldByName("Items")
		if items.Kind() != reflect.Slice {
			return fmt.Errorf("items field is not a slice in %s", gvk.String()) //nolint:err113
		}

		itemsLen := items.Len()
		for idx := range itemsLen {
			itemValue, ok := reflect.TypeAssert[kclient.Object](items.Index(idx).Addr())
			if !ok {
				return fmt.Errorf("item %d of %s list is not a client object", idx, gvk.String()) //nolint:err113
			}

			itemValue.GetObjectKind().SetGroupVersionKind(gvk)

			redactKubeObject(itemValue, gvk)

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

func redactKubeObject(obj kclient.Object, gvk schema.GroupVersionKind) {
	annotations := obj.GetAnnotations()
	for _, key := range redactedAnnotations {
		delete(annotations, key)
	}

	redactTypes(reflect.ValueOf(obj))

	if redactor, ok := kubeResourceRedactors[gvk]; ok {
		redactor(obj)
	}
}

func printKubeObject(obj kclient.Object, w io.Writer) error {
	obj.SetManagedFields(nil)

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
