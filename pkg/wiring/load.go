package wiring

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"

	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	kube    client.Client
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

func (l *Loader) Load(ctx context.Context, data []byte) ([]client.Object, error) {
	res := []client.Object{}
	multidocReader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))

	for idx := 1; ; idx++ {
		buf, err := multidocReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("object %d: reading: %w", idx, err)
		}

		rObj, _, err := wiringDecoder.Decode(buf, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("object %d: decoding: %w", idx, err)
		}

		obj, err := processObject(ctx, rObj)
		if err != nil {
			return nil, fmt.Errorf("object %d: %s/%s: %w", idx, obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}

		res = append(res, obj)
	}

	return res, nil
}

func processObject(ctx context.Context, rObj runtime.Object) (client.Object, error) {
	obj, ok := rObj.(client.Object)
	if !ok {
		return nil, fmt.Errorf("not a client.Object") //nolint:goerr113
	}

	if len(obj.GetName()) > 253 {
		return nil, fmt.Errorf("maximum name length is 253 characters") //nolint:goerr113
	}
	if !nameChecker.MatchString(obj.GetName()) {
		return nil, fmt.Errorf("name should match a lowercase RFC 1123 subdomain") //nolint:goerr113
	}

	if metaObj, ok := obj.(meta.Object); ok {
		metaObj.Default()
		if _, err := metaObj.Validate(ctx, nil, nil); err != nil {
			return nil, fmt.Errorf("validating: %w", err)
		}
	} else if fabObj, ok := obj.(*fabapi.Fabricator); ok {
		if obj.GetNamespace() != comp.FabNamespace {
			return nil, fmt.Errorf("fabricator should be in %q namespace", comp.FabNamespace) //nolint:goerr113
		}
		if err := fabObj.Validate(); err != nil {
			return nil, fmt.Errorf("validating: %w", err)
		}
	} else if controlObj, ok := obj.(*fabapi.ControlNode); ok {
		if obj.GetNamespace() != comp.FabNamespace {
			return nil, fmt.Errorf("control node(s) should be in %q namespace", comp.FabNamespace) //nolint:goerr113
		}
		if err := controlObj.Validate(nil); err != nil {
			return nil, fmt.Errorf("validating: %w", err)
		}
	}

	return obj, nil
}

func (l *Loader) LoadAdd(ctx context.Context, data []byte) error {
	objs, err := l.Load(ctx, data)
	if err != nil {
		return err
	}

	return l.Add(ctx, objs...)
}

func (l *Loader) Add(ctx context.Context, objs ...client.Object) error {
	for _, obj := range objs {
		obj.SetResourceVersion("")
		if err := l.kube.Create(ctx, obj); err != nil {
			return fmt.Errorf("adding %T: %w", obj, err)
		}
	}

	return nil
}

func (l *Loader) Update(ctx context.Context, objs ...client.Object) error {
	for _, obj := range objs {
		if err := l.kube.Update(ctx, obj); err != nil {
			return fmt.Errorf("updating %T: %w", obj, err)
		}
	}

	return nil
}

func (l *Loader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := l.kube.List(ctx, list, opts...); err != nil {
		return fmt.Errorf("listing %T: %w", list, err)
	}

	return nil
}
