// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cnc

import (
	"bytes"
	"log/slog"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/coreos/butane/config"
	"github.com/coreos/butane/config/common"
	helm "github.com/k3s-io/helm-controller/pkg/apis/helm.cattle.io/v1"
	"github.com/pkg/errors"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type ContentGenerator func() (string, error)

func FromValue(value string) ContentGenerator {
	return func() (string, error) {
		return value, nil
	}
}

func FromTemplate(tmplText string, dataBuilder ...any) ContentGenerator {
	return func() (string, error) {
		data := map[string]any{}

		if len(dataBuilder)%2 != 0 {
			return "", errors.New("dataBuilder should be key-value pairs")
		}

		for idx := 0; idx < len(dataBuilder); idx += 2 {
			key, ok := dataBuilder[idx].(string)
			if !ok {
				return "", errors.Errorf("dataBuilder key at index %d is not string", idx)
			}

			data[key] = dataBuilder[idx+1]
		}

		tmpl, err := template.New("tmpl").Funcs(sprig.FuncMap()).Parse(tmplText)
		if err != nil {
			return "", errors.Wrapf(err, "error parsing template")
		}

		buf := bytes.NewBuffer(nil)
		err = tmpl.Execute(buf, data)
		if err != nil {
			return "", errors.Wrapf(err, "error executing template")
		}

		return buf.String(), nil
	}
}

type KubeObjectProvider struct {
	Obj  meta.Object
	Skip bool
	Err  error
}

func FromKubeObjects(objs ...KubeObjectProvider) ContentGenerator {
	return func() (string, error) {
		buf := bytes.NewBuffer(nil)

		for idx, obj := range objs {
			if obj.Err != nil {
				return "", errors.Wrapf(obj.Err, "error generating kube object %d", idx)
			}

			if obj.Skip {
				continue
			}

			if obj.Obj == nil {
				return "", errors.Errorf("kube object %d is nil", idx)
			}

			data, err := yaml.Marshal(obj.Obj)
			if err != nil {
				return "", errors.Wrap(err, "error marshaling into yaml")
			}
			_, err = buf.Write(data)
			if err != nil {
				return "", errors.Wrap(err, "error writing yaml")
			}

			if idx != len(objs)-1 {
				_, err = buf.WriteString("---\n")
				if err != nil {
					return "", errors.Wrap(err, "error writing yaml separator")
				}
			}
		}

		return buf.String(), nil
	}
}

func If(cond bool, obj KubeObjectProvider) KubeObjectProvider {
	return KubeObjectProvider{
		Skip: !cond,
		Obj:  obj.Obj,
		Err:  obj.Err,
	}
}

func KubeSecret(name, ns string, data map[string]string) KubeObjectProvider {
	return KubeObjectProvider{
		Obj: &core.Secret{
			TypeMeta: meta.TypeMeta{
				APIVersion: core.SchemeGroupVersion.String(),
				Kind:       "Secret",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			StringData: data,
		},
	}
}

func KubeConfigMap(name, ns string, values ...any) KubeObjectProvider {
	if len(values)%2 != 0 {
		return KubeObjectProvider{
			Err: errors.New("values in KubeConfigMap should be key-value pairs"),
		}
	}

	data := map[string]string{}
	for idx := 0; idx < len(values); idx += 2 {
		key, ok := values[idx].(string)
		if !ok {
			return KubeObjectProvider{
				Err: errors.Errorf("values in KubeConfigMap should be key-value pairs, found key %T (expected string)", values[idx]),
			}
		}

		valueGen, ok := values[idx+1].(ContentGenerator)
		if !ok {
			return KubeObjectProvider{
				Err: errors.Errorf("values in KubeConfigMap should be key-value pairs, found value %T (expected ContentGenerator)", values[idx+1]),
			}
		}

		value, err := valueGen()
		if err != nil {
			return KubeObjectProvider{
				Err: errors.Wrapf(err, "error generating value for key %s", key),
			}
		}

		data[key] = value
	}

	return KubeObjectProvider{
		Obj: &core.ConfigMap{
			TypeMeta: meta.TypeMeta{
				APIVersion: core.SchemeGroupVersion.String(),
				Kind:       "ConfigMap",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Data: data,
		},
	}
}

func KubeService(name, ns string, spec core.ServiceSpec) KubeObjectProvider {
	return KubeObjectProvider{
		Obj: &core.Service{
			TypeMeta: meta.TypeMeta{
				APIVersion: core.SchemeGroupVersion.String(),
				Kind:       "Service",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: spec,
		},
	}
}

func KubeHelmChart(name, ns string, spec helm.HelmChartSpec, valuesGenerator ...ContentGenerator) KubeObjectProvider {
	if len(valuesGenerator) > 1 {
		return KubeObjectProvider{
			Err: errors.New("more than one values generator specified"),
		}
	}

	if len(valuesGenerator) == 1 {
		values, err := valuesGenerator[0]()
		if err != nil {
			return KubeObjectProvider{
				Err: err,
			}
		}
		spec.ValuesContent = values
	} else {
		return KubeObjectProvider{
			Err: errors.New("exactly one values generator required"),
		}
	}

	return KubeObjectProvider{
		Obj: &helm.HelmChart{
			TypeMeta: meta.TypeMeta{
				APIVersion: helm.SchemeGroupVersion.String(),
				Kind:       "HelmChart",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: spec,
		},
	}
}

func IgnitionFromButaneTemplate(tmplText string, dataBuilder ...any) ContentGenerator {
	return func() (string, error) {
		butane, err := FromTemplate(tmplText, dataBuilder...)()
		if err != nil {
			return "", err
		}

		options := common.TranslateBytesOptions{}
		options.NoResourceAutoCompression = true
		options.Pretty = true

		strict := true // fail on any warnings

		data, report, err := config.TranslateBytes([]byte(butane), options)
		if err != nil {
			return "", errors.Wrap(err, "error translating config")
		}
		if strict && len(report.Entries) > 0 {
			slog.Warn("butane", "report", report.String())

			return "", errors.New("butane produced warnings and strict mode is enabled")
		}

		return string(data), nil
	}
}

func YAMLFrom(obj any) ContentGenerator {
	return func() (string, error) {
		data, err := yaml.Marshal(obj)
		if err != nil {
			return "", errors.Wrap(err, "error marshaling into yaml")
		}

		return string(data), nil
	}
}
