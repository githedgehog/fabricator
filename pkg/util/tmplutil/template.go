// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package tmplutil

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

const (
	// leftDelim starts every action, incl. comments and the trimming forms, as the default
	// delimiters are never changed
	leftDelim = "{{"
	// noValue is what text/template prints for a missing map key under missingkey=zero.
	noValue = "<no value>"
)

// FromTemplate renders tmplText with sprig functions. Referencing a key that isn't present
// in data is an error.
func FromTemplate(name, tmplText string, data any) (string, error) {
	return fromTemplate(name, tmplText, data, "missingkey=error")
}

// FromTemplateLenient renders tmplText with sprig functions using Helm-like semantics:
// referencing a key that isn't present in data yields an empty string instead of an error,
// so the `default` and `if .Values.x` idioms work, and `required` makes a value mandatory.
func FromTemplateLenient(name, tmplText string, data any) (string, error) {
	// text without any actions renders as is, returning it right away keeps it byte for byte
	// as the "<no value>" cleanup below would otherwise drop it from the text itself
	if !strings.Contains(tmplText, leftDelim) {
		return tmplText, nil
	}

	out, err := fromTemplate(name, tmplText, data, "missingkey=zero")
	if err != nil {
		return "", err
	}

	// missingkey=zero still prints a literal "<no value>" for a missing key, which would
	// otherwise end up in the rendered output as a value. Helm does the same replacement.
	return strings.ReplaceAll(out, noValue, ""), nil
}

// required is Helm's function of the same name, it isn't part of sprig: it fails rendering
// with msg if val is missing (nil) or an empty string, and passes it through otherwise.
func required(msg string, val any) (any, error) {
	if val == nil {
		return nil, errors.New(msg) //nolint:err113
	}
	if str, ok := val.(string); ok && str == "" {
		return nil, errors.New(msg) //nolint:err113
	}

	return val, nil
}

func fromTemplate(name, tmplText string, data any, missingKey string) (string, error) {
	funcs := sprig.FuncMap()
	funcs["required"] = required

	tmpl, err := template.New(name).Funcs(funcs).Option(missingKey).Parse(tmplText)
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
