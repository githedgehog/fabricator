package wiring

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/pkg/errors"
	"go.githedgehog.com/fabric/api/meta"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func init() {
	scheme := runtime.NewScheme()
	if err := wiringapi.AddToScheme(scheme); err != nil {
		panic(fmt.Sprintf("error adding wiringapi to the scheme: %#v", err))
	}
	if err := vpcapi.AddToScheme(scheme); err != nil {
		panic(fmt.Sprintf("error adding vpcapi to the scheme: %#v", err))
	}

	decoder = serializer.NewCodecFactory(scheme).UniversalDeserializer()
}

var decoder runtime.Decoder

func Load(data []byte) ([]meta.Object, error) {
	res := []meta.Object{}
	multidocReader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))

	for idx := 1; ; idx++ {
		buf, err := multidocReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("object %d: %w", idx, err)
		}

		obj, _, err := decoder.Decode(buf, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("object %d: %w", idx, err)
		}

		metaObj, ok := obj.(meta.Object)
		if !ok {
			return nil, fmt.Errorf("object %d: %T is not a meta.Object", idx, obj) //nolint:goerr113
		}

		res = append(res, metaObj)
	}

	return res, nil
}
