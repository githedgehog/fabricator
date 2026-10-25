// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

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
	"k8s.io/apimachinery/pkg/util/intstr"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kyaml "sigs.k8s.io/yaml"
)

const leakedValue = "leaked-secret"

// fillStrings sets every string reachable from v through exported fields, pointers, slices and maps to leakedValue,
// so a test covers the fields a type gains later too. Map keys get it as well, but they are names, not values.
func fillStrings(v reflect.Value) {
	switch v.Kind() { //nolint:exhaustive // only the kinds that can hold a string
	case reflect.String:
		v.SetString(leakedValue)
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillStrings(v.Elem())
	case reflect.Struct:
		for idx := range v.NumField() {
			if v.Type().Field(idx).IsExported() {
				fillStrings(v.Field(idx))
			}
		}
	case reflect.Slice:
		slice := reflect.MakeSlice(v.Type(), 1, 1)
		fillStrings(slice.Index(0))
		v.Set(slice)
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillStrings(key)
		val := reflect.New(v.Type().Elem()).Elem()
		fillStrings(val)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, val)
		v.Set(m)
	}
}

// leakedFields returns the sorted JSON paths of the values of obj that are still leakedValue, as is or base64 encoded
// like Secret data, skipping the ones that go through any of the allowed keys
func leakedFields(t *testing.T, obj any, allowed ...string) []string {
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
			if slices.Contains(leaked, typed) && !slices.ContainsFunc(path, func(p string) bool { return slices.Contains(allowed, p) }) {
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

// alloyNotSecret are the alloy target fields that aren't credentials and so stay in the dump
var alloyNotSecret = []string{"url", "username", "labels", "caPEM", "certPEM"}

func TestRedactAlloyTargets(t *testing.T) {
	// everything else in a target is a credential, so a new target kind or field fails here until it's redacted
	targets := alloy.Targets{}
	fillStrings(reflect.ValueOf(&targets).Elem())
	require.NotEmpty(t, leakedFields(t, targets, alloyNotSecret...), "targets should have credentials before redaction")

	redactAlloyTargets(&targets)
	require.Empty(t, leakedFields(t, targets, alloyNotSecret...))
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
	fillStrings(reflect.ValueOf(&fab.Spec.Config.Fabric.DefaultAlloyConfig).Elem())
	fillStrings(reflect.ValueOf(&fab.Spec.Config.Observability.Targets).Elem())

	agent := &agentapi.Agent{Name: "leaf-1", Namespace: kmetav1.NamespaceDefault, Annotations: copyAnnotations}
	agent.Spec.Version = agentapi.AgentVersion{Username: "user", Password: leakedValue}
	agent.Spec.Users = []agentapi.UserCreds{{Name: "admin", Password: leakedValue, Role: "admin", SSHKeys: []string{leakedValue}}}
	fillStrings(reflect.ValueOf(&agent.Spec.Alloy).Elem())
	fillStrings(reflect.ValueOf(&agent.Spec.Config.Alloy.Targets).Elem())

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
		&corev1.ConfigMap{Name: "plain", Namespace: comp.FabNamespace, Annotations: copyAnnotations},
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
		kubeResourceGVKs, kubeResourceRedactors, out))

	printed := []string{}
	for doc := range strings.SplitSeq(out.String(), "---\n") {
		parsed := map[string]any{}
		require.NoError(t, kyaml.Unmarshal([]byte(doc), &parsed))

		obj := &unstructured.Unstructured{Object: parsed}
		printed = append(printed, obj.GetKind()+"/"+obj.GetName())
		assert.Empty(t, leakedFields(t, parsed, alloyNotSecret...), "leaked in %s %s", obj.GetKind(), obj.GetName())
	}

	for _, obj := range objs {
		gvk, err := apiutil.GVKForObject(obj, scheme)
		require.NoError(t, err)
		require.Contains(t, printed, gvk.Kind+"/"+obj.GetName())
	}
}
