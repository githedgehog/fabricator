// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	helmapi "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/libmeta/pkg/alloy"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kyaml "sigs.k8s.io/yaml"
)

const (
	leakedValue = "leaked-secret"
	filledKey   = "name"
)

// fillStrings sets every string reachable from v through exported fields, pointers, slices and maps to leakedValue,
// so a test covers the fields a type gains later too. Map keys are set to filledKey, as they are names, not values.
func fillStrings(v reflect.Value) {
	fillStringsIn(v, map[reflect.Type]bool{})
}

// fillStringsIn fills v, skipping the structs already being filled on the way to it, so a recursive type, e.g. the CRD
// schema, is only filled at its first level, while the same type in a few places side by side is filled in all of
// them. That's enough for the tests, as the deeper levels have the same fields, which are redacted the same way.
func fillStringsIn(v reflect.Value, filling map[reflect.Type]bool) {
	switch v.Kind() { //nolint:exhaustive // only the kinds that can hold a string
	case reflect.String:
		v.SetString(leakedValue)
	case reflect.Pointer:
		if filling[v.Type().Elem()] {
			return
		}
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillStringsIn(v.Elem(), filling)
	case reflect.Struct:
		filling[v.Type()] = true
		defer delete(filling, v.Type())
		for idx := range v.NumField() {
			if v.Type().Field(idx).IsExported() {
				fillStringsIn(v.Field(idx), filling)
			}
		}
	case reflect.Slice:
		// raw or binary data, e.g. the managed fields of the embedded pod templates, has to stay valid as is
		if v.Type().Elem().Kind() == reflect.Uint8 || filling[v.Type().Elem()] {
			return
		}
		slice := reflect.MakeSlice(v.Type(), 1, 1)
		fillStringsIn(slice.Index(0), filling)
		v.Set(slice)
	case reflect.Map:
		if filling[v.Type().Elem()] {
			return
		}
		key := reflect.New(v.Type().Key()).Elem()
		if key.Kind() == reflect.String {
			key.SetString(filledKey)
		} else {
			fillStringsIn(key, filling)
		}
		val := reflect.New(v.Type().Elem()).Elem()
		fillStringsIn(val, filling)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, val)
		v.Set(m)
	}
}

// notThrough reports the paths that don't go through any of the keys
func notThrough(keys ...string) func(path []string) bool {
	return func(path []string) bool {
		return !slices.ContainsFunc(path, func(p string) bool { return slices.Contains(keys, p) })
	}
}

// leakedFields returns the sorted JSON paths of the values of obj that are still leakedValue, as is or base64 encoded
// like Secret data, the ones report returns true for
func leakedFields(t *testing.T, obj any, report func(path []string) bool) []string {
	t.Helper()

	leaked := []string{leakedValue, base64.StdEncoding.EncodeToString([]byte(leakedValue))}

	data, err := json.Marshal(obj)
	require.NoError(t, err)

	var parsed any
	require.NoError(t, json.Unmarshal(data, &parsed))

	res := []string{}
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		switch typed := v.(type) {
		case map[string]any:
			for key, val := range typed {
				walk(val, append(slices.Clone(path), key))
			}
		case []any:
			for idx, val := range typed {
				walk(val, append(slices.Clone(path), strconv.Itoa(idx)))
			}
		case string:
			if slices.Contains(leaked, typed) && report(path) {
				res = append(res, strings.Join(path, "."))
			}
		}
	}
	walk(parsed, nil)
	slices.Sort(res)

	return res
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	for _, builder := range schemeBuilders {
		require.NoError(t, builder(scheme))
	}

	return scheme
}

func TestCollectsAllOwnKinds(t *testing.T) {
	for gvk, objType := range newScheme(t).AllKnownTypes() {
		// skip the options/events every group gets registered with
		if !strings.HasSuffix(gvk.Group, ".githedgehog.com") || strings.HasPrefix(objType.PkgPath(), "k8s.io/apimachinery/") {
			continue
		}

		assert.True(t, matchesGVK(gvk, kubeResourceGVKs), "%s isn't collected", gvk)
	}
}

// credentialField matches the names of the fields that may hold a credential, so the ones a kind gains later fail
// the test below until they are either redacted or reviewed. No "session" as it's the BGP and MCLAG ones in our API.
var credentialField = regexp.MustCompile(
	`(?i)pass|pwd|secret|token|key|hash|cred|auth|bearer|pem|private|ssh|psk|header|cookie|salt|otp|license|kubeconfig`)

// notCredentialFields are the field names matching credentialField that never hold a credential, wherever they are
var notCredentialFields = []string{"username", "caPEM", "certPEM"}

// isNotCredentialField reports the fields that can't hold a credential themselves, wherever they are: the ones listed
// above, the references to a Secret or another object, which only have its name and key, and the pod templates
// cert-manager embeds for its solvers, which are core Kubernetes ones and so left out the same as the core kinds
func isNotCredentialField(name string) bool {
	return slices.Contains(notCredentialFields, name) ||
		strings.HasSuffix(name, "Ref") || strings.HasSuffix(name, "Name") || name == "podTemplate"
}

// issuerReviewedFields are the fields of the cert-manager Issuer and ClusterIssuer, which share their spec, matching
// credentialField that are reviewed to not hold a credential: references to the Secrets without the Ref suffix and
// the identifiers going with the secrets referenced next to them
var issuerReviewedFields = []string{
	"spec.acme.externalAccountBinding.keyAlgorithm",
	"spec.acme.externalAccountBinding.keyID",
	"spec.acme.solvers.0.dns01.route53.accessKeyID",
	"spec.vault.auth.appRole.path",
	"spec.vault.auth.appRole.roleId",
	"spec.vault.auth.aws.iamRoleArn",
	"spec.vault.auth.aws.mountPath",
	"spec.vault.auth.aws.region",
	"spec.vault.auth.aws.role",
	"spec.vault.auth.aws.vaultHeaderValue",
	"spec.vault.auth.clientCertificate.mountPath",
	"spec.vault.auth.clientCertificate.name",
	"spec.vault.auth.kubernetes.mountPath",
	"spec.vault.auth.kubernetes.role",
	"spec.venafi.ngts.tokenEndpoint",
	"status.acme.lastPrivateKeyHash",
}

// reviewedFields are the fields matching credentialField that are reviewed to not hold a credential, by kind
var reviewedFields = map[schema.GroupVersionKind][]string{
	cmapi.SchemeGroupVersion.WithKind(cmapi.IssuerKind):        issuerReviewedFields,
	cmapi.SchemeGroupVersion.WithKind(cmapi.ClusterIssuerKind): issuerReviewedFields,
	cmapi.SchemeGroupVersion.WithKind(cmapi.CertificateKind): {
		// the keystore and private key settings, e.g. its algorithm, not the key itself
		"spec.keystores.jks.alias",
		"spec.keystores.pkcs12.profile",
		"spec.privateKey.algorithm",
		"spec.privateKey.encoding",
		"spec.privateKey.rotationPolicy",
		"spec.secretTemplate.annotations.name",
		"spec.secretTemplate.labels.name",
	},
	helmapi.SchemeGroupVersion.WithKind("HelmChart"): {
		"spec.authSecret.name",
		"spec.dockerRegistrySecret.name",
		"spec.podSecurityContext.windowsOptions.gmsaCredentialSpec",
		"spec.securityContext.windowsOptions.gmsaCredentialSpec",
		"spec.valuesSecrets.0.keys.0",
		"spec.valuesSecrets.0.name",
	},
	helmapi.SchemeGroupVersion.WithKind("HelmChartConfig"): {
		"spec.valuesSecrets.0.keys.0",
		"spec.valuesSecrets.0.name",
	},
}

// isScannedForCredentials reports whether the kind is checked by the test below: the collected kinds of our own API
// groups and of the third-party ones with their own CRDs, i.e. cert-manager and helm-controller. The core Kubernetes
// kinds are left out, as their selector keys and Secret references match credentialField all over and would need a
// long list of reviewed fields. The list types and the options/events every group gets registered with aren't kinds
// to check at all.
func isScannedForCredentials(gvk schema.GroupVersionKind, objType reflect.Type) bool {
	switch {
	case !strings.HasSuffix(gvk.Group, ".githedgehog.com") && gvk.Group != "cert-manager.io" && gvk.Group != "helm.cattle.io":
		return false
	case !matchesGVK(gvk, kubeResourceGVKs):
		return false
	case strings.HasSuffix(gvk.Kind, "List"), strings.HasPrefix(objType.PkgPath(), "k8s.io/apimachinery/"):
		return false
	default:
		return true
	}
}

func TestRedactsCredentialFields(t *testing.T) {
	credentials := 0
	for gvk, objType := range newScheme(t).AllKnownTypes() {
		if !isScannedForCredentials(gvk, objType) {
			continue
		}

		candidate := func(path []string) bool {
			return slices.ContainsFunc(path, credentialField.MatchString) && !slices.ContainsFunc(path, isNotCredentialField)
		}
		report := func(path []string) bool {
			return candidate(path) && !slices.Contains(reviewedFields[gvk], strings.Join(path, "."))
		}

		objValue := reflect.New(objType)
		obj, ok := reflect.TypeAssert[kclient.Object](objValue)
		require.True(t, ok, "%s isn't a client object", gvk)

		// metadata is left out as its managed fields can't be filled with strings
		for _, field := range []string{"Spec", "Status"} {
			if fieldValue := objValue.Elem().FieldByName(field); fieldValue.IsValid() {
				fillStrings(fieldValue)
			}
		}
		credentials += len(leakedFields(t, obj, report))
		assert.Subset(t, leakedFields(t, obj, candidate), reviewedFields[gvk], "reviewed fields not in %s", gvk.Kind)

		redactKubeObject(obj, gvk)
		assert.Empty(t, leakedFields(t, obj, report), "unredacted credentials in %s", gvk.Kind)
	}
	require.NotZero(t, credentials, "kinds should have credentials before redaction")
}

// alloyNotSecret are the alloy target fields that aren't credentials and so stay in the dump
var alloyNotSecret = []string{"url", "username", "labels", "caPEM", "certPEM"}

func TestRedactAlloyTargets(t *testing.T) {
	// everything else in a target is a credential, so a new target kind or field fails here until it's redacted
	targets := alloy.Targets{}
	fillStrings(reflect.ValueOf(&targets).Elem())
	require.NotEmpty(t, leakedFields(t, targets, notThrough(alloyNotSecret...)), "targets should have credentials before redaction")

	redactTypes(reflect.ValueOf(&targets))
	require.Empty(t, leakedFields(t, targets, notThrough(alloyNotSecret...)))
}

// the fake client doesn't keep the managed fields, so they are checked on an object as is
func TestRedactKubeObjectDropsManagedFields(t *testing.T) {
	cm := &corev1.ConfigMap{
		Name: "cm", Namespace: comp.FabNamespace,
		ManagedFields: []kmetav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: kmetav1.ManagedFieldsOperationApply}},
	}

	redactKubeObject(cm, corev1.SchemeGroupVersion.WithKind("ConfigMap"))
	require.Empty(t, cm.ManagedFields)
}

func TestRedactKubeObjectDropsCRDSchemas(t *testing.T) {
	crd := &apiextv1.CustomResourceDefinition{Name: "fabricators.fabricator.githedgehog.com"}
	crd.Spec.Versions = []apiextv1.CustomResourceDefinitionVersion{{
		Name: "v1beta1", Served: true, Storage: true,
		Schema: &apiextv1.CustomResourceValidation{OpenAPIV3Schema: &apiextv1.JSONSchemaProps{Type: "object"}},
	}}

	redactKubeObject(crd, apiextv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	require.Equal(t, []apiextv1.CustomResourceDefinitionVersion{{Name: "v1beta1", Served: true, Storage: true}}, crd.Spec.Versions)
}

func TestCollectKubeObjectsRedacts(t *testing.T) {
	copyAnnotations := map[string]string{}
	for _, key := range redactedAnnotations {
		copyAnnotations[key] = leakedValue
	}

	fab := &fabapi.Fabricator{Name: "default", Namespace: comp.FabNamespace, Annotations: copyAnnotations}
	fab.Spec.Config.Registry.Upstream = &fabapi.ControlConfigRegistryUpstream{Username: "user", Password: leakedValue}
	fab.Spec.Config.Control.JoinToken = leakedValue
	fab.Spec.Config.Control.DefaultUser = fabapi.ControlUser{PasswordHash: leakedValue, AuthorizedKeys: []string{leakedValue}}
	fab.Spec.Config.Fabric.DefaultSwitchUsers = map[string]fabapi.SwitchUser{
		"admin": {PasswordHash: leakedValue, Role: "admin", AuthorizedKeys: []string{leakedValue}},
	}
	fillStrings(reflect.ValueOf(&fab.Spec.Config.Fabric.DefaultAlloyConfig.PrometheusTargets).Elem())
	fillStrings(reflect.ValueOf(&fab.Spec.Config.Fabric.DefaultAlloyConfig.LokiTargets).Elem())
	fillStrings(reflect.ValueOf(&fab.Spec.Config.Observability.Targets).Elem())

	agent := &agentapi.Agent{Name: "leaf-1", Namespace: kmetav1.NamespaceDefault, Annotations: copyAnnotations}
	agent.Spec.Version = agentapi.AgentVersion{Username: "user", Password: leakedValue}
	agent.Spec.Users = []agentapi.UserCreds{{Name: "admin", Password: leakedValue, Role: "admin", SSHKeys: []string{leakedValue}}}
	fillStrings(reflect.ValueOf(&agent.Spec.Alloy.PrometheusTargets).Elem())
	fillStrings(reflect.ValueOf(&agent.Spec.Alloy.LokiTargets).Elem())
	fillStrings(reflect.ValueOf(&agent.Spec.Config.Alloy.Targets).Elem())
	agent.Spec.Config.Alloy.Scrapes = map[string]alloy.Scrape{
		"remote": {Address: "127.0.0.1:9100", HTTPHeaders: map[string]string{"Authorization": leakedValue}},
	}

	objs := []kclient.Object{
		fab,
		agent,
		&corev1.Secret{
			Name: "secret", Namespace: comp.FabNamespace, Annotations: copyAnnotations,
			Data: map[string][]byte{"password": []byte(leakedValue)},
		},
		&corev1.ConfigMap{
			Name: "fabric-ctrl-config", Namespace: comp.FabNamespace,
			Data: map[string]string{"config.yaml": leakedValue},
		},
		&corev1.ConfigMap{
			Name: "alloy-ctrl", Namespace: comp.FabNamespace,
			Data: map[string]string{"config.alloy": leakedValue},
		},
		&corev1.ConfigMap{
			Name: "plain", Namespace: comp.FabNamespace, Annotations: copyAnnotations,
			Data:       map[string]string{"config.yaml": leakedValue},
			BinaryData: map[string][]byte{"config.bin": []byte(leakedValue)},
		},
		&helmapi.HelmChart{
			Name: "alloy-ctrl", Namespace: comp.FabNamespace,
			Spec: helmapi.HelmChartSpec{
				ValuesContent: leakedValue,
				Values:        &apiextv1.JSON{Raw: []byte(`{"password":"` + leakedValue + `"}`)},
				Set:           map[string]intstr.IntOrString{"password": intstr.FromString(leakedValue)},
			},
		},
		&helmapi.HelmChartConfig{
			Name: "alloy-ctrl", Namespace: comp.FabNamespace,
			Spec: helmapi.HelmChartConfigSpec{
				ValuesContent: leakedValue,
				Values:        &apiextv1.JSON{Raw: []byte(`{"password":"` + leakedValue + `"}`)},
			},
		},
	}

	scheme := newScheme(t)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	out := &bytes.Buffer{}
	require.NoError(t, collector{quiet: true}.collectKubeObjects(t.Context(), kube, scheme,
		kubeResourceGVKs, out))

	printed := []string{}
	for doc := range strings.SplitSeq(out.String(), "---\n") {
		parsed := map[string]any{}
		require.NoError(t, kyaml.Unmarshal([]byte(doc), &parsed))

		obj := &unstructured.Unstructured{Object: parsed}
		printed = append(printed, obj.GetKind()+"/"+obj.GetName())
		assert.Empty(t, leakedFields(t, parsed, notThrough(alloyNotSecret...)), "leaked in %s %s", obj.GetKind(), obj.GetName())
	}

	for _, obj := range objs {
		gvk, err := apiutil.GVKForObject(obj, scheme)
		require.NoError(t, err)
		require.Contains(t, printed, gvk.Kind+"/"+obj.GetName())
	}
}
