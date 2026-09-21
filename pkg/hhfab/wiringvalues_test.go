// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/util/apiutil"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLoadWiringValues(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		files    map[string]string // file name -> content, written to a temp dir
		order    []string          // order files are passed in, required when there's >1
		sets     []string
		expected map[string]any
		err      bool
	}{
		{
			name:     "no values at all returns non-nil empty map",
			expected: map[string]any{},
		},
		{
			name:     "single file",
			files:    map[string]string{"a.yaml": "leafCount: 4\nname: fabric\n"},
			expected: map[string]any{"leafCount": 4, "name": "fabric"},
		},
		{
			name:     "empty file",
			files:    map[string]string{"a.yaml": ""},
			expected: map[string]any{},
		},
		{
			name: "two files merged left to right",
			files: map[string]string{
				"a.yaml": "leafCount: 4\nspineCount: 2\n",
				"b.yaml": "leafCount: 8\n",
			},
			order:    []string{"a.yaml", "b.yaml"},
			expected: map[string]any{"leafCount": 8, "spineCount": 2},
		},
		{
			name: "nested maps are deep merged",
			files: map[string]string{
				"a.yaml": "fabric:\n  leaves:\n    count: 4\n    profile: vs\n",
				"b.yaml": "fabric:\n  leaves:\n    count: 8\n  spines:\n    count: 2\n",
			},
			order: []string{"a.yaml", "b.yaml"},
			expected: map[string]any{
				"fabric": map[string]any{
					"leaves": map[string]any{"count": 8, "profile": "vs"},
					"spines": map[string]any{"count": 2},
				},
			},
		},
		{
			name: "slices are replaced wholesale, not merged",
			files: map[string]string{
				"a.yaml": "names: [a, b, c]\n",
				"b.yaml": "names: [x]\n",
			},
			order:    []string{"a.yaml", "b.yaml"},
			expected: map[string]any{"names": []any{"x"}},
		},
		{
			name:     "set with a flat key",
			sets:     []string{"leafCount=4"},
			expected: map[string]any{"leafCount": 4},
		},
		{
			name: "set with a dotted key creates nesting",
			sets: []string{"fabric.leaves.count=4"},
			expected: map[string]any{
				"fabric": map[string]any{"leaves": map[string]any{"count": 4}},
			},
		},
		{
			name: "multiple sets are deep merged",
			sets: []string{"fabric.leaves.count=4", "fabric.spines.count=2"},
			expected: map[string]any{
				"fabric": map[string]any{
					"leaves": map[string]any{"count": 4},
					"spines": map[string]any{"count": 2},
				},
			},
		},
		{
			name:     "set overrides a file value",
			files:    map[string]string{"a.yaml": "leafCount: 4\nname: fabric\n"},
			sets:     []string{"leafCount=8"},
			expected: map[string]any{"leafCount": 8, "name": "fabric"},
		},
		{
			name: "set scalars are coerced by type",
			sets: []string{"num=4", "float=1.5", "yes=true", "str=foo", `quoted="4"`},
			expected: map[string]any{
				"num": 4, "float": 1.5, "yes": true, "str": "foo", "quoted": "4",
			},
		},
		{
			name:     "set with an empty value",
			sets:     []string{"name="},
			expected: map[string]any{"name": nil},
		},
		{
			name:     "set value may contain =",
			sets:     []string{"expr=a=b"},
			expected: map[string]any{"expr": "a=b"},
		},
		{
			// kyaml round-trips through JSON, so without normalizeNumbers these would be
			// float64 and 1000000 would render as "1e+06"
			name:     "numbers are normalized to int, not float64",
			files:    map[string]string{"a.yaml": "big: 1000000\nnested:\n  list: [1, 2]\n"},
			expected: map[string]any{"big": 1000000, "nested": map[string]any{"list": []any{1, 2}}},
		},
		{
			name:     "non-integral floats stay floats",
			sets:     []string{"ratio=1.5"},
			expected: map[string]any{"ratio": 1.5},
		},
		{
			// kyaml is YAML 1.1, so these coerce the same way they would in a values file.
			// This diverges from Helm's --set (which keeps them strings) but matches Helm's
			// values files. Quoting is the escape hatch, covered below.
			name: "YAML 1.1 coercion applies to sets",
			sets: []string{"flag=yes", "oct=0755", "ver=1.10"},
			expected: map[string]any{
				"flag": true, "oct": 493, "ver": 1.1,
			},
		},
		{
			name: "quoting opts out of YAML 1.1 coercion",
			sets: []string{`flag="yes"`, `oct="0755"`, `ver="1.10"`},
			expected: map[string]any{
				"flag": "yes", "oct": "0755", "ver": "1.10",
			},
		},
		{name: "set without =", sets: []string{"leafCount"}, err: true},
		{name: "set with an empty key", sets: []string{"=4"}, err: true},
		{name: "set with an empty key part", sets: []string{"a..b=4"}, err: true},
		{name: "set with a trailing dot", sets: []string{"a.=4"}, err: true},
		{name: "values file that isn't a map", files: map[string]string{"a.yaml": "- 1\n- 2\n"}, err: true},
		{name: "malformed values file", files: map[string]string{"a.yaml": "a: [1\n"}, err: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			order := test.order
			if order == nil {
				require.LessOrEqual(t, len(test.files), 1, "test setup: order is required for >1 file")
				for name := range test.files {
					order = append(order, name)
				}
			}
			require.Len(t, order, len(test.files), "test setup: order must cover every file")

			var files []string
			for _, name := range order {
				content, ok := test.files[name]
				require.True(t, ok, "test setup: unknown file %q in order", name)

				path := filepath.Join(dir, name)
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
				files = append(files, path)
			}

			actual, err := LoadWiringValues(files, test.sets)
			if test.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, actual)
			require.Equal(t, test.expected, actual)
		})
	}
}

func TestLoadWiringValuesMissingFile(t *testing.T) {
	t.Parallel()

	_, err := LoadWiringValues([]string{filepath.Join(t.TempDir(), "nope.yaml")}, nil)
	require.ErrorIs(t, err, ErrNotExist)
}

func TestWiringImportName(t *testing.T) {
	t.Parallel()

	for in, expected := range map[string]string{
		"topo.yaml":      "topo.yaml",
		"topo.tmpl.yaml": "topo.yaml",
		"topo.yaml.tmpl": "topo.yaml",
		"topo.tmpl":      "topo.tmpl", // not a wiring source, left alone
		"tmpl.yaml":      "tmpl.yaml",
	} {
		require.Equal(t, expected, wiringImportName(in), "for %q", in)
	}
}

func TestIsWiringSource(t *testing.T) {
	t.Parallel()

	for in, expected := range map[string]bool{
		"topo.yaml":      true,
		"topo.tmpl.yaml": true,
		"topo.yaml.tmpl": true,
		"topo.yml":       false,
		"topo.tmpl":      false,
		"topo":           false,
	} {
		require.Equal(t, expected, isWiringSource(in), "for %q", in)
	}
}

const switchTmpl = `{{- range $i := until (int .Values.leafCount) }}
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-{{ $i }}
spec:
  role: server-leaf
  profile: {{ $.Values.profile | default "vs" }}
---
{{- end }}
`

func TestImportFabricGatewayTemplated(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		sourceName   string
		importName   string // when set, imported as <source>:<importName>
		values       map[string]any
		expectedFile string
		expectedObjs []string
		errContains  string
	}{
		{
			name:         "tmpl.yaml is rendered and the .tmpl is stripped",
			sourceName:   "topo.tmpl.yaml",
			values:       map[string]any{"leafCount": 2},
			expectedFile: "topo.yaml",
			expectedObjs: []string{"leaf-0", "leaf-1"},
		},
		{
			name:         "yaml.tmpl is rendered and the .tmpl is stripped",
			sourceName:   "topo.yaml.tmpl",
			values:       map[string]any{"leafCount": 1},
			expectedFile: "topo.yaml",
			expectedObjs: []string{"leaf-0"},
		},
		{
			name:         "plain .yaml is templated too",
			sourceName:   "topo.yaml",
			values:       map[string]any{"leafCount": 3},
			expectedFile: "topo.yaml",
			expectedObjs: []string{"leaf-0", "leaf-1", "leaf-2"},
		},
		{
			name:         "explicit import name wins and is also stripped",
			sourceName:   "topo.tmpl.yaml",
			importName:   "custom.tmpl.yaml",
			values:       map[string]any{"leafCount": 1},
			expectedFile: "custom.yaml",
			expectedObjs: []string{"leaf-0"},
		},
		{
			name:         "values are used, not ignored",
			sourceName:   "topo.tmpl.yaml",
			values:       map[string]any{"leafCount": 1, "profile": "custom-profile"},
			expectedFile: "topo.yaml",
			expectedObjs: []string{"leaf-0"},
		},
		{
			// lenient rendering makes `int nil` 0, so `until 0` emits nothing at all and
			// the loader rejects the empty document -- it still fails loudly, just not
			// where you'd first expect
			name:        "missing leafCount renders nothing and fails to load",
			sourceName:  "topo.tmpl.yaml",
			values:      map[string]any{},
			errContains: "Kind' is missing",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			// importFabricGateway assumes the include dir exists, Init creates it
			require.NoError(t, os.MkdirAll(filepath.Join(dir, IncludeDir), 0o700))

			source := filepath.Join(dir, test.sourceName)
			require.NoError(t, os.WriteFile(source, []byte(switchTmpl), 0o600))

			wiring := source
			if test.importName != "" {
				wiring = source + ":" + test.importName
			}

			err := importFabricGateway(InitConfig{WorkDir: dir, Wiring: []string{wiring}}, WiringTemplateData{Values: test.values})
			if test.errContains != "" {
				require.ErrorContains(t, err, test.errContains)

				return
			}
			require.NoError(t, err)

			data, err := os.ReadFile(filepath.Join(dir, IncludeDir, test.expectedFile))
			require.NoError(t, err)

			out := string(data)
			require.NotContains(t, out, "{{", "template actions left unrendered")
			require.NotContains(t, out, "<no value>", "missing key placeholder leaked into output")
			if profile, ok := test.values["profile"]; ok {
				require.Contains(t, out, profile)
			}

			objs, err := apiutil.NewLoader().Load(apiutil.FabricGatewayGVKs, data)
			require.NoError(t, err)

			names := make([]string, 0, len(objs))
			for _, obj := range objs {
				names = append(names, obj.GetName())
			}
			require.Equal(t, test.expectedObjs, names)
		})
	}
}

func TestImportFabricGatewayLenient(t *testing.T) {
	t.Parallel()

	const tmpl = `apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-{{ .Values.missing }}{{ .Values.index | default 7 }}
spec:
  role: server-leaf
{{- if .Values.optional }}
  description: {{ .Values.optional }}
{{- end }}
`

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, IncludeDir), 0o700))

	source := filepath.Join(dir, "topo.tmpl.yaml")
	require.NoError(t, os.WriteFile(source, []byte(tmpl), 0o600))

	// with no values at all: `default` and `if` must work, and a bare missing key must
	// render as empty rather than a literal "<no value>"
	require.NoError(t, importFabricGateway(InitConfig{WorkDir: dir, Wiring: []string{source}}, WiringTemplateData{Values: map[string]any{}}))

	data, err := os.ReadFile(filepath.Join(dir, IncludeDir, "topo.yaml"))
	require.NoError(t, err)

	require.NotContains(t, string(data), "<no value>")
	require.NotContains(t, string(data), "description:")

	objs, err := apiutil.NewLoader().Load(apiutil.FabricGatewayGVKs, data)
	require.NoError(t, err)
	require.Len(t, objs, 1)
	require.Equal(t, "leaf-7", objs[0].GetName())
}

func TestImportFabricGatewayPlainYAMLUnchanged(t *testing.T) {
	t.Parallel()

	const plain = `apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: leaf-1
spec:
  role: server-leaf
  profile: vs
`

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, IncludeDir), 0o700))

	source := filepath.Join(dir, "topo.yaml")
	require.NoError(t, os.WriteFile(source, []byte(plain), 0o600))

	require.NoError(t, importFabricGateway(InitConfig{WorkDir: dir, Wiring: []string{source}}, WiringTemplateData{}))

	data, err := os.ReadFile(filepath.Join(dir, IncludeDir, "topo.yaml"))
	require.NoError(t, err)
	require.Equal(t, plain, string(data), "a wiring file with no template actions must round-trip byte for byte")
}

func TestImportFabricGatewayContext(t *testing.T) {
	t.Parallel()

	// exercises every part of the context: .Version, .Fab (struct fields), .Controls keyed
	// lookup and deterministic map iteration, and .Nodes
	const tmpl = `apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: sw
  annotations:
    hhfab: "{{ .Version }}"
    release: "{{ .Release }}"
    mode: "{{ .Fab.Spec.Config.Fabric.Mode }}"
    fabName: "{{ .Fab.Name }}"
    byName: "{{ (index .Controls "control-1").Spec.Bootstrap.Disk }}"
    allControls: "{{ range $name, $c := .Controls }}{{ $name }},{{ end }}"
    allNodes: "{{ range $name := .Nodes }}{{ $name.Name }},{{ end }}"
spec:
  role: server-leaf
`

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, IncludeDir), 0o700))

	source := filepath.Join(dir, "ctx.tmpl.yaml")
	require.NoError(t, os.WriteFile(source, []byte(tmpl), 0o600))

	tmplData := WiringTemplateData{Values: map[string]any{}, Version: "v1.2.3", Release: "26.04.0"}
	tmplData.Fab.Name = "default"
	tmplData.Fab.Spec.Config.Fabric.Mode = meta.FabricModeSpineLeaf
	// deliberately out of order, to prove range iterates in key order
	tmplData.setNodes(
		[]fabapi.ControlNode{
			{ObjectMeta: kmetav1.ObjectMeta{Name: "control-2"}},
			{ObjectMeta: kmetav1.ObjectMeta{Name: "control-1"}, Spec: fabapi.ControlNodeSpec{
				Bootstrap: fabapi.ControlNodeBootstrap{Disk: "/dev/sda"},
			}},
		},
		[]fabapi.FabNode{
			{ObjectMeta: kmetav1.ObjectMeta{Name: "node-b"}},
			{ObjectMeta: kmetav1.ObjectMeta{Name: "node-a"}},
		},
	)

	require.NoError(t, importFabricGateway(InitConfig{WorkDir: dir, Wiring: []string{source}}, tmplData))

	data, err := os.ReadFile(filepath.Join(dir, IncludeDir, "ctx.yaml"))
	require.NoError(t, err)

	out := string(data)
	require.Contains(t, out, `hhfab: "v1.2.3"`)
	require.Contains(t, out, `release: "26.04.0"`)
	require.Contains(t, out, `mode: "spine-leaf"`)
	require.Contains(t, out, `fabName: "default"`)
	require.Contains(t, out, `byName: "/dev/sda"`, "index .Controls by name")
	require.Contains(t, out, `allControls: "control-1,control-2,"`, "map range must be in key order")
	require.Contains(t, out, `allNodes: "node-a,node-b,"`)

	objs, err := apiutil.NewLoader().Load(apiutil.FabricGatewayGVKs, data)
	require.NoError(t, err)
	require.Len(t, objs, 1)
}

func TestImportFabricGatewayRejectsBadExtension(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, IncludeDir), 0o700))

	source := filepath.Join(dir, "topo.yml")
	require.NoError(t, os.WriteFile(source, []byte("{}\n"), 0o600))

	err := importFabricGateway(InitConfig{WorkDir: dir, Wiring: []string{source}}, WiringTemplateData{})
	require.ErrorContains(t, err, "extension")
	require.NotContains(t, err.Error(), "reading", "should be rejected before reading")
}
