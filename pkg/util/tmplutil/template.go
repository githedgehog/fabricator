// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package tmplutil

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

// noValue is what text/template prints for a missing map key under missingkey=zero.
const noValue = "<no value>"

// FromTemplate renders tmplText with sprig functions. Referencing a key that isn't present
// in data is an error.
func FromTemplate(name, tmplText string, data any) (string, error) {
	return fromTemplate(name, tmplText, data, "missingkey=error")
}

// FromTemplateLenient renders tmplText with sprig functions using Helm-like semantics:
// referencing a key that isn't present in data yields an empty string instead of an error,
// so the `default` and `if .Values.x` idioms work.
func FromTemplateLenient(name, tmplText string, data any) (string, error) {
	out, err := fromTemplate(name, tmplText, data, "missingkey=zero")
	if err != nil {
		return "", err
	}

	// missingkey=zero still prints a literal "<no value>" for a missing key, which would
	// otherwise end up in the rendered output as a value. Helm does the same replacement.
	return strings.ReplaceAll(out, noValue, ""), nil
}

func fromTemplate(name, tmplText string, data any, missingKey string) (string, error) {
	tmpl, err := template.New(name).Funcs(sprig.FuncMap()).Option(missingKey).Parse(tmplText)
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
