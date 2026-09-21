// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package tmplutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabricator/pkg/util/tmplutil"
)

// FromTemplate must keep erroring on a missing key: every existing caller relies on it to
// catch typos in embedded templates.
func TestFromTemplateMissingKeyIsError(t *testing.T) {
	t.Parallel()

	_, err := tmplutil.FromTemplate("test", "{{ .missing }}", map[string]any{"present": 1})
	require.Error(t, err)
}

func TestFromTemplate(t *testing.T) {
	t.Parallel()

	out, err := tmplutil.FromTemplate("test", `{{ .name | upper }}-{{ .count }}`, map[string]any{
		"name": "leaf", "count": 2,
	})
	require.NoError(t, err)
	require.Equal(t, "LEAF-2", out)
}

func TestFromTemplateLenient(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		tmpl     string
		data     map[string]any
		expected string
	}{
		{
			name:     "missing key renders empty, not <no value>",
			tmpl:     "a{{ .missing }}b",
			data:     map[string]any{},
			expected: "ab",
		},
		{
			name:     "default works on a missing key",
			tmpl:     "{{ .missing | default 7 }}",
			data:     map[string]any{},
			expected: "7",
		},
		{
			name:     "default leaves a present key alone",
			tmpl:     "{{ .count | default 7 }}",
			data:     map[string]any{"count": 2},
			expected: "2",
		},
		{
			name:     "if on a missing key is false",
			tmpl:     "{{ if .missing }}yes{{ else }}no{{ end }}",
			data:     map[string]any{},
			expected: "no",
		},
		{
			name:     "if on a present key is true",
			tmpl:     "{{ if .opt }}yes{{ else }}no{{ end }}",
			data:     map[string]any{"opt": "x"},
			expected: "yes",
		},
		{
			name:     "sprig funcs are available",
			tmpl:     "{{ range $i := until 3 }}{{ $i }}{{ end }}",
			data:     map[string]any{},
			expected: "012",
		},
		{
			name:     "present keys render normally",
			tmpl:     "{{ .name | upper }}",
			data:     map[string]any{"name": "leaf"},
			expected: "LEAF",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			out, err := tmplutil.FromTemplateLenient("test", test.tmpl, test.data)
			require.NoError(t, err)
			require.Equal(t, test.expected, out)
		})
	}
}

// missingkey only covers the final lookup, so traversing through an absent key still errors.
// Helm behaves the same way, this test pins the behaviour rather than endorsing it.
func TestFromTemplateLenientNestedMissingStillErrors(t *testing.T) {
	t.Parallel()

	_, err := tmplutil.FromTemplateLenient("test", "{{ .a.b }}", map[string]any{})
	require.Error(t, err)
}

func TestFromTemplateLenientParseError(t *testing.T) {
	t.Parallel()

	_, err := tmplutil.FromTemplateLenient("test", "{{ .unclosed ", map[string]any{})
	require.ErrorContains(t, err, "parsing template")
}
