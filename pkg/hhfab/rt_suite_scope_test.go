// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
)

func TestIsMultiFabricAndDomain(t *testing.T) {
	fabric := func(domains ...string) wiringapi.Fabric {
		spec := wiringapi.FabricSpec{Domains: map[string]wiringapi.FabricDomainSpec{}}
		for _, domain := range domains {
			spec.Domains[domain] = wiringapi.FabricDomainSpec{}
		}

		return wiringapi.Fabric{Spec: spec}
	}

	require.False(t, isMultiFabric(nil))
	require.False(t, isMultiFabric([]wiringapi.Fabric{fabric("default")}))
	require.True(t, isMultiFabric([]wiringapi.Fabric{fabric("default"), fabric("default")}))
	require.False(t, isMultiFabric([]wiringapi.Fabric{fabric("default", "domain-b")}))

	require.False(t, isMultiDomain(nil))
	require.False(t, isMultiDomain([]wiringapi.Fabric{fabric("default"), fabric("default")}))
	require.True(t, isMultiDomain([]wiringapi.Fabric{fabric("default", "domain-b")}))
}

func TestMultiFabricAndDomainSkipSuitesThatDoNotSupportThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   SkipFlags
		suite   JUnitTestSuite
		message string
	}{
		{name: "several Fabrics", flags: SkipFlags{MultiFabric: true}, message: "several Fabrics"},
		{name: "several domains", flags: SkipFlags{MultiDomain: true}, message: "several domains"},
		{name: "domains with a Fabric only suite", flags: SkipFlags{MultiDomain: true}, suite: JUnitTestSuite{MultiFabric: true}, message: "several domains"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suite := tc.suite
			suite.Name = "suite"
			suite.Tests = 2
			suite.TestCases = []JUnitTestCase{{Name: "one"}, {Name: "two"}}

			result, err := selectAndRunSuite(context.Background(), nil, &suite, nil, false, tc.flags)
			require.NoError(t, err)
			require.Equal(t, 2, result.Skipped)
			for _, test := range result.TestCases {
				require.NotNil(t, test.Skipped, test.Name)
				require.Contains(t, test.Skipped.Message, tc.message)
			}
		})
	}
}
