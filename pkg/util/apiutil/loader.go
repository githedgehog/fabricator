// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package apiutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"

	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var nameChecker = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

type Loader struct {
	decoder runtime.Decoder
	kube    kclient.Client
}

func NewLoader() *Loader {
	return &Loader{
		decoder: decoder,
		kube:    fake.NewClientBuilder().WithScheme(scheme).Build(),
	}
}

func (l *Loader) GetClient() kclient.Client {
	return l.kube
}

func (l *Loader) Load(gvks []schema.GroupVersionKind, data []byte) ([]kclient.Object, error) {
	res := []kclient.Object{}
	multidocReader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))

	for idx := 1; ; idx++ {
		buf, err := multidocReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("object %d: reading: %w", idx, err)
		}

		rObj, _, err := l.decoder.Decode(buf, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("object %d: decoding: %w", idx, err)
		}

		gvk := rObj.GetObjectKind().GroupVersionKind()
		if !isAllowedGVK(gvk, gvks) {
			return nil, fmt.Errorf("object %d: %s: not allowed GVK", idx, gvk.String()) //nolint:goerr113
		}

		obj, ok := rObj.(kclient.Object)
		if !ok {
			return nil, fmt.Errorf("object %d: %s: not a client.Object", idx, gvk.Kind) //nolint:goerr113
		}

		if obj.GetNamespace() == "" {
			obj.SetNamespace(kmetav1.NamespaceDefault)
		}

		if err := validateObject(obj); err != nil {
			return nil, fmt.Errorf("object %d: %s/%s: %w", idx, gvk.Kind, obj.GetName(), err)
		}

		res = append(res, obj)
	}

	return res, nil
}

func (l *Loader) LoadAdd(ctx context.Context, gvks []schema.GroupVersionKind, data []byte) error {
	objs, err := l.Load(gvks, data)
	if err != nil {
		return err
	}

	return l.Add(ctx, objs...)
}

func (l *Loader) Add(ctx context.Context, objs ...kclient.Object) error {
	for _, obj := range objs {
		obj.SetResourceVersion("")

		if err := l.kube.Create(ctx, obj); err != nil {
			return fmt.Errorf("adding %T: %w", obj, err)
		}
	}

	return nil
}

func (l *Loader) List(ctx context.Context, list kclient.ObjectList, opts ...kclient.ListOption) error {
	if err := l.kube.List(ctx, list, opts...); err != nil {
		return fmt.Errorf("listing %T: %w", list, err)
	}

	return nil
}

func validateObject(obj kclient.Object) error {
	if len(obj.GetName()) > 253 {
		return fmt.Errorf("maximum name length is 253 characters") //nolint:goerr113
	}
	if !nameChecker.MatchString(obj.GetName()) {
		return fmt.Errorf("name should match a lowercase RFC 1123 subdomain") //nolint:goerr113
	}

	return nil
}

func isAllowedGVK(gvk schema.GroupVersionKind, gvks []schema.GroupVersionKind) bool {
	if len(gvks) == 0 {
		return true
	}

	allowed := false
	for _, gvkAllowed := range gvks {
		if gvkAllowed.Group != "" && gvkAllowed.Group != gvk.Group {
			continue
		}

		if gvkAllowed.Version != "" && gvkAllowed.Version != gvk.Version {
			continue
		}

		if gvkAllowed.Kind != "" && gvkAllowed.Kind != gvk.Kind {
			continue
		}

		allowed = true

		break
	}

	return allowed
}
