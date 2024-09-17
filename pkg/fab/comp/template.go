package comp

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

func FromTemplate(name, tmplText string, data map[string]any) (string, error) {
	tmpl, err := template.New(name).Funcs(sprig.FuncMap()).Option("missingkey=error").Parse(tmplText)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	buf := bytes.NewBuffer(nil)
	err = tmpl.Execute(buf, data)
	if err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}

	return buf.String(), nil
}
