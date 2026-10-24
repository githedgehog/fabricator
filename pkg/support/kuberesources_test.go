// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/libmeta/pkg/alloy"
)

const leakedValue = "leaked-secret"

// fillStrings sets every string reachable from v through exported fields, pointers, slices and maps to leakedValue,
// so a test covers the fields a type gains later too. Map keys get it as well, but they are names, not values.
func fillStrings(v reflect.Value) {
	switch v.Kind() { //nolint:exhaustive // only the kinds that can hold a string
	case reflect.String:
		v.SetString(leakedValue)
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillStrings(v.Elem())
	case reflect.Struct:
		for idx := range v.NumField() {
			if v.Type().Field(idx).IsExported() {
				fillStrings(v.Field(idx))
			}
		}
	case reflect.Slice:
		slice := reflect.MakeSlice(v.Type(), 1, 1)
		fillStrings(slice.Index(0))
		v.Set(slice)
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillStrings(key)
		val := reflect.New(v.Type().Elem()).Elem()
		fillStrings(val)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, val)
		v.Set(m)
	}
}

// leakedFields returns the sorted JSON paths of the values of obj that are still leakedValue, skipping the ones
// that go through any of the allowed keys
func leakedFields(t *testing.T, obj any, allowed ...string) []string {
	t.Helper()

	data, err := json.Marshal(obj)
	require.NoError(t, err)

	var parsed any
	require.NoError(t, json.Unmarshal(data, &parsed))

	res := []string{}
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		switch typed := v.(type) {
		case map[string]any:
			for key, val := range typed {
				walk(val, append(slices.Clone(path), key))
			}
		case []any:
			for idx, val := range typed {
				walk(val, append(slices.Clone(path), strconv.Itoa(idx)))
			}
		case string:
			if typed == leakedValue && !slices.ContainsFunc(path, func(p string) bool { return slices.Contains(allowed, p) }) {
				res = append(res, strings.Join(path, "."))
			}
		}
	}
	walk(parsed, nil)
	slices.Sort(res)

	return res
}

func TestRedactAlloyTargets(t *testing.T) {
	// everything else in a target is a credential, so a new target kind or field fails here until it's redacted
	notSecret := []string{"url", "username", "labels", "caPEM", "certPEM"}

	targets := alloy.Targets{}
	fillStrings(reflect.ValueOf(&targets).Elem())
	require.NotEmpty(t, leakedFields(t, targets, notSecret...), "targets should have credentials before redaction")

	redactAlloyTargets(&targets)
	require.Empty(t, leakedFields(t, targets, notSecret...))
}
