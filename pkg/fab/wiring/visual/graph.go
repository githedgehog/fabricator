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

package visual

import (
	"bytes"
	"strings"
	"text/template"

	"github.com/pkg/errors"
)

type Graph struct {
	Devices []Device
	Links   []Link
}

type Device struct {
	ID         string
	Name       string
	Properties map[string]string
	Endpoints  []Endpoint
}

type Endpoint struct {
	ID         string
	Name       string
	Properties map[string]string
}

type Link struct {
	From  string
	To    string
	Color string
}

func New() *Graph {
	return &Graph{
		Devices: []Device{},
		Links:   []Link{},
	}
}

func (g *Graph) Dot() (string, error) {
	return executeTemplate(tmplGraphDot, g)
}

func executeTemplate(tmplText string, data any) (string, error) {
	tmplText = strings.TrimPrefix(tmplText, "\n")
	tmplText = strings.TrimSpace(tmplText)

	tmpl, err := template.New("tmpl").Parse(tmplText)
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

const tmplGraphDot = `
graph {
	graph [pad="0.5", nodesep="0.1", ranksep="2"];
	node [shape = "Mrecord"];

	overlap = false;
    splines = true;

	{{ range .Devices }}
	subgraph "cluster_{{ .ID }}" {
		label = <<b>{{ .Name }}</b>{{ range $k, $v := .Properties }}{{ if $v }}, {{ $k }}={{ $v }}{{ end }}{{ end }}>;

		{{ range .Endpoints }}
		"{{ .ID }}" [label = <<b>{{ .Name }}</b>{{ range $k, $v := .Properties }}{{ if $v }}<br/>{{ $k }}={{ $v }}{{ end }}{{ end }}> ];
		{{ end }}
	}
	{{ end }}


	{{ range .Links }}
	"{{ .From }}" -- "{{ .To }}" {{ if .Color }}[color = {{ .Color }}]{{ end }};
	{{ end }}
  }
`
