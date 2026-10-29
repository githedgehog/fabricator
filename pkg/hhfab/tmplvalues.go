// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/samber/lo"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	"go.githedgehog.com/fabricator/pkg/version"
	kyaml "sigs.k8s.io/yaml"
)

// TemplateData is the context available to a templated config or wiring file. Values is a
// map, so referencing an absent key yields an empty string (Helm-like); the rest are Go
// structs, where an unknown field is a template error.
type TemplateData struct {
	// Values is what --values and --set produced
	Values map[string]any
	// Version is the hhfab version, component versions live under Fab.Status.Versions
	Version string
	// Release is the user-facing release version of the whole project, e.g. "26.04.0"
	Release string
	// Fab is the fabricator, with defaults merged in and versions calculated. For the config
	// it only holds the defaults, for the wiring it's the config after evaluation.
	Fab fabapi.Fabricator
	// Controls and Nodes are the control and fab nodes from the same config, keyed by name,
	// and empty for the config itself. Look one up with `index .Controls "control-1"` and
	// iterate with `range $name, $c := .Controls` -- text/template ranges maps in key order,
	// so the iteration is deterministic.
	Controls map[string]fabapi.ControlNode
	Nodes    map[string]fabapi.FabNode
}

// defaultsTemplateData is the context for rendering the config itself: it can't depend on
// the config being rendered, so Fab only holds the default config and release versions.
func defaultsTemplateData(values map[string]any) (TemplateData, error) {
	data := TemplateData{Values: values, Version: version.Version, Release: fab.Release}
	data.Fab.Name = fabapi.FabName
	data.Fab.Namespace = fabapi.FabNamespace
	data.Fab.Spec.Config = *fab.DefaultConfig.DeepCopy()
	if err := data.Fab.CalculateVersions(fab.Versions); err != nil {
		return TemplateData{}, fmt.Errorf("calculating default versions: %w", err)
	}
	data.setNodes(nil, nil)

	return data, nil
}

// setNodes fills in Controls and Nodes, keying the slices GetFabAndNodes returns by name.
func (d *TemplateData) setNodes(controls []fabapi.ControlNode, nodes []fabapi.FabNode) {
	d.Controls = lo.SliceToMap(controls, func(c fabapi.ControlNode) (string, fabapi.ControlNode) {
		return c.Name, c
	})
	d.Nodes = lo.SliceToMap(nodes, func(n fabapi.FabNode) (string, fabapi.FabNode) {
		return n.Name, n
	})
}

// LoadTemplateValues builds the template values used to render the imported config and
// wiring files. Values files are merged left-to-right, then the k=v entries from sets are
// applied on top. The result is never nil.
func LoadTemplateValues(valuesFiles, sets []string) (map[string]any, error) {
	values := map[string]any{}

	for _, valuesFile := range valuesFiles {
		data, err := os.ReadFile(valuesFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("values %q: %w", valuesFile, ErrNotExist)
			}

			return nil, fmt.Errorf("values %q: reading: %w", valuesFile, err)
		}

		parsed := map[string]any{}
		if err := kyaml.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("values %q: parsing: %w", valuesFile, err)
		}

		normalizeNumbers(parsed)
		mergeValues(values, parsed)
	}

	for _, set := range sets {
		parsed, err := parseSet(set)
		if err != nil {
			return nil, err
		}

		mergeValues(values, parsed)
	}

	return values, nil
}

// parseSet turns a single --set entry into a (possibly nested) map, e.g.
// "fabric.leaves.count=4" becomes {"fabric": {"leaves": {"count": 4}}}.
func parseSet(set string) (map[string]any, error) {
	key, value, found := strings.Cut(set, "=")
	if !found {
		return nil, fmt.Errorf("set %q: should be '<key>=<value>'", set) //nolint:err113
	}

	path := strings.Split(key, ".")
	if slices.Contains(path, "") {
		return nil, fmt.Errorf("set %q: empty key part", set) //nolint:err113
	}

	var parsed any
	if err := kyaml.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, fmt.Errorf("set %q: parsing value: %w", set, err)
	}

	res := map[string]any{}
	curr := res
	for _, part := range path[:len(path)-1] {
		next := map[string]any{}
		curr[part] = next
		curr = next
	}
	curr[path[len(path)-1]] = normalizeNumbers(parsed)

	return res, nil
}

// mergeValues deep-merges src into dst: nested maps are merged recursively, everything else
// (including slices) is replaced wholesale, matching Helm's values merging.
func mergeValues(dst, src map[string]any) {
	for k, srcVal := range src {
		srcMap, srcIsMap := srcVal.(map[string]any)
		dstMap, dstIsMap := dst[k].(map[string]any)

		if srcIsMap && dstIsMap {
			mergeValues(dstMap, srcMap)

			continue
		}

		dst[k] = srcVal
	}
}

// normalizeNumbers converts integral float64 values to int throughout the tree. Values are
// parsed by sigs.k8s.io/yaml, which round-trips through JSON and so decodes every number as
// a float64. That breaks `until .Values.x` ("expected int; got float64") and makes large
// values render in scientific notation (1000000 -> "1e+06").
func normalizeNumbers(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			v[k] = normalizeNumbers(item)
		}

		return v
	case []any:
		for idx, item := range v {
			v[idx] = normalizeNumbers(item)
		}

		return v
	case float64:
		// the bounds check keeps the int() conversion defined for huge values
		if v == math.Trunc(v) && v >= math.MinInt64 && v <= math.MaxInt64 {
			if asInt := int(v); float64(asInt) == v {
				return asInt
			}
		}

		return v
	default:
		return value
	}
}
