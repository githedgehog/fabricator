// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"

	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kyaml "sigs.k8s.io/yaml"
)

// TODO cleanup
func dumpObjects(ctx context.Context, kube kclient.Reader, scheme *runtime.Scheme, w io.Writer, withListGVKs ...schema.GroupVersionKind) error {
	objListType := reflect.TypeOf((*kclient.ObjectList)(nil)).Elem()

	objs := 0

	for gvk, t := range scheme.AllKnownTypes() {
		// if gvk.Group != fabapi.GroupVersion.Group || gvk.Version != fabapi.GroupVersion.Version {
		// 	continue
		// }

		ok := len(withListGVKs) == 0
		for _, withGVK := range withListGVKs {
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

		// skip options/events
		if strings.HasPrefix(t.PkgPath(), "k8s.io/apimachinery/pkg/apis/meta") {
			continue
		}

		if !reflect.PointerTo(t).Implements(objListType) {
			continue
		}

		slog.Debug("Dumping resource type", "gvk", gvk.String()) // , "type", t, "pkg", t.PkgPath())

		objListValue := reflect.New(t)
		objList, ok := objListValue.Interface().(kclient.ObjectList)
		if !ok {
			return fmt.Errorf("doesn't implement object list: %s", gvk.String())
		}
		if err := kube.List(ctx, objList); err != nil {
			return fmt.Errorf("listing %s: %w", gvk.String(), err)
		}

		items := objListValue.Elem().FieldByName("Items")
		if items.Kind() != reflect.Slice {
			return fmt.Errorf("items field is not a slice in %s", gvk.String())
		}

		itemsLen := items.Len()

		if itemsLen > 0 {
			if _, err := fmt.Fprintf(w, "#\n# %s\n#\n", gvk); err != nil {
				return fmt.Errorf("writing gvk comment: %w", err)
			}
		}

		for i := 0; i < itemsLen; i++ {
			if objs > 0 {
				if _, err := fmt.Fprintf(w, "---\n"); err != nil {
					return fmt.Errorf("writing separator: %w", err)
				}
			}
			objs++

			itemValue, ok := items.Index(i).Addr().Interface().(kclient.Object)
			if !ok {
				return fmt.Errorf("item %d of %s is not a client object", i, gvk.String()) // TODO
			}

			if itemValue.GetObjectKind().GroupVersionKind().Kind == "" {
				kind := strings.TrimSuffix(gvk.Kind, "List")                                     // TODO
				itemValue.GetObjectKind().SetGroupVersionKind(gvk.GroupVersion().WithKind(kind)) // TODO may be missing
			}

			if err := printObject(itemValue, w, true); err != nil {
				return fmt.Errorf("printing item %d of %s: %w", i, gvk.String(), err)
			}
		}
	}

	return nil
}

type printObj struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Meta       printObjMeta `json:"metadata,omitempty"`
	Data       any          `json:"data,omitempty"`
	Spec       any          `json:"spec,omitempty"`
	Status     any          `json:"status,omitempty"`
}

type printObjMeta struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func printObject(obj kclient.Object, w io.Writer, withStatus bool) error {
	if true {
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

	labels := obj.GetLabels()
	if len(labels) == 0 {
		labels = nil
	}

	annotations := obj.GetAnnotations()
	for key := range annotations {
		if key == "kubectl.kubernetes.io/last-applied-configuration" {
			delete(annotations, key)
		}
	}
	if len(annotations) == 0 {
		annotations = nil
	}

	p := printObj{
		APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		Kind:       obj.GetObjectKind().GroupVersionKind().Kind,
		Meta: printObjMeta{
			Name:        obj.GetName(),
			Namespace:   obj.GetNamespace(),
			Labels:      labels,
			Annotations: annotations,
		},
	}

	data := reflect.ValueOf(obj).Elem().FieldByName("Data")
	if data.IsValid() {
		p.Data = data.Interface()
	}

	spec := reflect.ValueOf(obj).Elem().FieldByName("Spec")
	if spec.IsValid() {
		p.Spec = spec.Interface()
	}

	status := reflect.ValueOf(obj).Elem().FieldByName("Status")
	if withStatus && status.IsValid() {
		p.Status = status.Interface()
	}

	buf, err := kyaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	_, err = w.Write(buf)
	if err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	return nil
}

func loadObjects(scheme *runtime.Scheme, r io.Reader) (kclient.Client, error) {
	res := []kclient.Object{}

	decoder := serializer.NewCodecFactory(scheme, serializer.EnableStrict).UniversalDeserializer()
	multidocReader := utilyaml.NewYAMLReader(bufio.NewReader(r))

	for idx := 1; ; idx++ {
		buf, err := multidocReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("object %d: reading: %w", idx, err)
		}

		rObj, _, err := decoder.Decode(buf, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("object %d: decoding: %w", idx, err)
		}

		kind := rObj.GetObjectKind().GroupVersionKind().Kind

		obj, ok := rObj.(kclient.Object)
		if !ok {
			return nil, fmt.Errorf("object %d: %s: not a client.Object", idx, kind) //nolint:goerr113
		}

		if obj.GetNamespace() == "" {
			obj.SetNamespace(kmetav1.NamespaceDefault)
		}

		res = append(res, obj)
	}

	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(res...).
		Build()

	return kube, nil
}
