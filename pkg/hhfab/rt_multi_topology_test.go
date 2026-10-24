// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

func TestIsMultiTopology(t *testing.T) {
	fabric := func(domains ...string) wiringapi.Fabric {
		spec := wiringapi.FabricSpec{Domains: map[string]wiringapi.FabricDomainSpec{}}
		for _, domain := range domains {
			spec.Domains[domain] = wiringapi.FabricDomainSpec{}
		}

		return wiringapi.Fabric{Spec: spec}
	}

	require.False(t, isMultiTopology(nil))
	require.False(t, isMultiTopology([]wiringapi.Fabric{fabric("default")}))
	require.True(t, isMultiTopology([]wiringapi.Fabric{fabric("default"), fabric("default")}))
	require.True(t, isMultiTopology([]wiringapi.Fabric{fabric("default", "domain-b")}))
}

func TestMultiTopologySkipsSuitesThatDoNotSupportIt(t *testing.T) {
	suite := &JUnitTestSuite{
		Name:  "single fabric",
		Tests: 2,
		TestCases: []JUnitTestCase{
			{Name: "one"},
			{Name: "two"},
		},
	}

	suite, err := selectAndRunSuite(context.Background(), nil, suite, nil, false, SkipFlags{MultiTopology: true})
	require.NoError(t, err)
	require.Equal(t, 2, suite.Skipped)
	for _, test := range suite.TestCases {
		require.NotNil(t, test.Skipped, test.Name)
		require.Contains(t, test.Skipped.Message, "several Fabrics or domains")
	}
}
