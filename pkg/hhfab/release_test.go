// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"regexp"
	"slices"
	"testing"
)

func TestRegexpSelection(t *testing.T) {
	tsr := JUnitTestSuite{
		Name: "TestSuite1",
		TestCases: []JUnitTestCase{
			{
				Name: "No Restrictions",
			},
			{
				Name: "Single VPC with Restrictions",
			},
			{
				Name: "DNS/NTP/MTU",
			},
			{
				Name: "Static External",
			},
			{
				Name: "MCLAG",
			},
			{
				Name: "ESLAG",
			},
		},
	}

	// Test cases
	tests := []struct {
		name        string
		regexes     []string
		invertRegex bool
		indexes     []int
	}{
		{
			name:        "No Regexes",
			regexes:     []string{},
			invertRegex: false,
			indexes:     []int{0, 1, 2, 3, 4, 5},
		},
		{
			name:        "No Regexes Inverted (no effect)",
			regexes:     []string{},
			invertRegex: true,
			indexes:     []int{0, 1, 2, 3, 4, 5},
		},
		{
			name:        "LAG",
			regexes:     []string{"LAG$"},
			invertRegex: false,
			indexes:     []int{4, 5},
		},
		{
			name:        "LAG Inverted",
			regexes:     []string{"LAG$"},
			invertRegex: true,
			indexes:     []int{0, 1, 2, 3},
		},
		{
			name:        "Restrictions + LAG",
			regexes:     []string{"LAG$", "Restrictions"},
			invertRegex: false,
			indexes:     []int{0, 1, 4, 5},
		},
		{
			name:        "Restrictions + LAG Inverted",
			regexes:     []string{"LAG$", "Restrictions"},
			invertRegex: true,
			indexes:     []int{2, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newSuite := &JUnitTestSuite{
				Name:      tsr.Name,
				TestCases: make([]JUnitTestCase, len(tsr.TestCases)),
			}
			copy(newSuite.TestCases, tsr.TestCases)
			var regexes []*regexp.Regexp
			for _, regex := range test.regexes {
				r, err := regexp.Compile(regex)
				if err != nil {
					t.Errorf("Error compiling regex %s: %v", regex, err)
				}
				regexes = append(regexes, r)
			}
			newSuite = regexpSelection(regexes, test.invertRegex, newSuite)
			for i, tc := range newSuite.TestCases {
				if slices.Contains(test.indexes, i) {
					if tc.Skipped != nil {
						t.Errorf("Test case %s should not be skipped", tc.Name)
					}
				} else {
					if tc.Skipped == nil {
						t.Errorf("Test case %s should be skipped", tc.Name)
					}
				}
			}
		})
	}
}
