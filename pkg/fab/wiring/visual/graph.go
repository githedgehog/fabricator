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
