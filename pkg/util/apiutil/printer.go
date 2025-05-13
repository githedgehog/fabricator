// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"context"
	"fmt"
	"io"
	"reflect"

	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

func printKubeObjects(ctx context.Context, kube kclient.Reader, w io.Writer, objLists ...kclient.ObjectList) error {
	objs := 0

	for _, objList := range objLists {
		gvk := objList.GetObjectKind().GroupVersionKind()

		if err := kube.List(ctx, objList); err != nil {
			return fmt.Errorf("listing %T: %w", objList, err)
		}

		items := reflect.ValueOf(objList).Elem().FieldByName("Items")
		if items.Kind() != reflect.Slice {
			return fmt.Errorf("items field is not a slice in %s", gvk.String()) //nolint:goerr113
		}

		itemsLen := items.Len()
		if itemsLen > 0 {
			_, err := fmt.Fprintf(w, "#\n# %s\n#\n", reflect.TypeOf(objList).Elem().Name())
			if err != nil {
				return fmt.Errorf("writing comment: %w", err)
			}
		}

		for idx := 0; idx < itemsLen; idx++ {
			itemValue, ok := items.Index(idx).Addr().Interface().(kclient.Object)
			if !ok {
				return fmt.Errorf("item %d of %s is not an object", idx, gvk.String()) //nolint:goerr113
			}

			if objs > 0 {
				if _, err := fmt.Fprintf(w, "---\n"); err != nil {
					return fmt.Errorf("writing separator: %w", err)
				}
			}
			objs++

			if err := PrintKubeObject(itemValue, w, false); err != nil {
				return fmt.Errorf("printing item %d of %s: %w", idx, gvk.String(), err)
			}
		}
	}

	return nil
}

type printObj struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Meta       printObjMeta `json:"metadata,omitempty"`
	Spec       any          `json:"spec,omitempty"`
	Status     any          `json:"status,omitempty"`
}

type printObjMeta struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func PrintKubeObject(obj kclient.Object, w io.Writer, withStatus bool) error {
	annotations := obj.GetAnnotations()
	delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
	if len(annotations) == 0 {
		annotations = nil
	}

	labels := obj.GetLabels()
	wiringapi.CleanupFabricLabels(labels)
	// TODO: do we have some other labels to clean up?
	if len(labels) == 0 {
		labels = nil
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
		Spec: reflect.ValueOf(obj).Elem().FieldByName("Spec").Interface(),
	}

	if withStatus {
		p.Status = reflect.ValueOf(obj).Elem().FieldByName("Status").Interface()
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
