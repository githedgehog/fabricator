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
	suite.Tests = len(suite.TestCases)

	return suite
}
