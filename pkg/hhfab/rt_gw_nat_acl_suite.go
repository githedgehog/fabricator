// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

// makeGatewayNATACLSuite holds the tests that only set up their own peerings on
// top of the VPCs, so it runs without a wipe between them: wiping re-leased every
// server's address and made the gateway rebuild its VRF routes moments before
// each probe (#1937).
func makeGatewayNATACLSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "Gateway NAT and ACL Suite",
	}
	suite.TestCases = append(suite.TestCases, getNATTestCases()...)
	suite.TestCases = append(suite.TestCases, getExternalNATTestCases()...)
	suite.TestCases = append(suite.TestCases, getACLTestCases()...)
	// near last: it restarts dataplane/frr pods, so a regression where the dataplane
	// does not recover breaks every subsequent gateway test
	suite.TestCases = append(suite.TestCases, getGatewayRestartTestCases()...)
	// Last on purpose: unlike the others it creates an IPv4Namespace and a VPC and
	// re-attaches a server, so without a wipe between tests anything its reverts
	// miss would be inherited by every test after it.
	suite.TestCases = append(suite.TestCases, JUnitTestCase{
		Name: "Gateway Peering Overlap NAT",
		F:    gatewayPeeringOverlapNATTest,
		SkipFlags: SkipFlags{
			NoGateway: true,
		},
	})
	suite.Tests = len(suite.TestCases)

	return suite
}
