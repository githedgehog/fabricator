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

	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	wiringScheme  = runtime.NewScheme()
	wiringDecoder runtime.Decoder

	fabScheme  = runtime.NewScheme()
	fabDecoder runtime.Decoder

	nameChecker = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

func init() {
	if err := wiringapi.AddToScheme(wiringScheme); err != nil {
		panic(fmt.Errorf("adding wiringapi to the wiring scheme: %w", err))
	}
	if err := vpcapi.AddToScheme(wiringScheme); err != nil {
		panic(fmt.Errorf("adding vpcapi to the wiring scheme: %w", err))
	}
	wiringDecoder = serializer.NewCodecFactory(wiringScheme, serializer.EnableStrict).UniversalDeserializer()

	if err := fabapi.AddToScheme(fabScheme); err != nil {
		panic(fmt.Errorf("adding fabapi to the fab scheme: %w", err))
	}
	fabDecoder = serializer.NewCodecFactory(fabScheme, serializer.EnableStrict).UniversalDeserializer()
}

type Loader struct {
	scheme  *runtime.Scheme
	decoder runtime.Decoder
	kube    kclient.Client
}

func NewWiringLoader() *Loader {
	return &Loader{
		scheme:  wiringScheme,
		decoder: wiringDecoder,
		kube:    fake.NewClientBuilder().WithScheme(wiringScheme).Build(),
	}
}

func NewFabLoader() *Loader {
	return &Loader{
		scheme:  fabScheme,
		decoder: fabDecoder,
		kube:    fake.NewClientBuilder().WithScheme(fabScheme).Build(),
	}
}

func (l *Loader) GetClient() kclient.Client {
	return l.kube
}

func (l *Loader) Load(data []byte) ([]kclient.Object, error) {
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

		kind := rObj.GetObjectKind().GroupVersionKind().Kind

		obj, ok := rObj.(kclient.Object)
		if !ok {
			return nil, fmt.Errorf("object %d: %s: not a client.Object", idx, kind) //nolint:goerr113
		}

		if obj.GetNamespace() == "" {
			obj.SetNamespace(kmetav1.NamespaceDefault)
		}

		if err := validateObject(obj); err != nil {
			return nil, fmt.Errorf("object %d: %s/%s: %w", idx, kind, obj.GetName(), err)
		}

		res = append(res, obj)
	}

	return res, nil
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

func (l *Loader) LoadAdd(ctx context.Context, data []byte) error {
	objs, err := l.Load(data)
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

func (l *Loader) Update(ctx context.Context, objs ...kclient.Object) error {
	for _, obj := range objs {
		if err := l.kube.Update(ctx, obj); err != nil {
			return fmt.Errorf("updating %T: %w", obj, err)
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
