// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"fmt"
	"iter"
	"reflect"

	"k8s.io/apimachinery/pkg/runtime"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func KubeListItems(objList kclient.ObjectList) iter.Seq2[int, kclient.Object] {
	items := reflect.ValueOf(objList).Elem().FieldByName("Items")
	if items.Kind() != reflect.Slice {
		return nil
	}
	itemsLen := items.Len()

	return func(yield func(int, kclient.Object) bool) {
		for i := 0; i < itemsLen; i++ {
			if !yield(i, items.Index(i).Addr().Interface().(kclient.Object)) {
				break
			}
		}
	}
}

func EnsureKind(obj kclient.Object, scheme *runtime.Scheme) error {
	if obj.GetObjectKind().GroupVersionKind().Kind == "" {
		if scheme == nil {
			return fmt.Errorf("empty object kind while scheme not provided") //nolint:err113
		}

		gvks, _ /* unversioned */, err := scheme.ObjectKinds(obj)
		if err != nil {
			return fmt.Errorf("getting object kinds for %T: %w", obj, err)
		}

		if len(gvks) == 0 {
			return fmt.Errorf("no object kinds found for %T", obj) //nolint:err113
		}
		if len(gvks) > 1 {
			return fmt.Errorf("multiple object kinds found for %T", obj) //nolint:err113
		}

		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
	}

	return nil
}
