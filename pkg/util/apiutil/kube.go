// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"iter"
	"reflect"

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
