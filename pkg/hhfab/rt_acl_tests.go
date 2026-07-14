// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
)

// Gateway peering ACL tests. Each test establishes a single peering between the
// first two VPCs with a specific PeeringACL and asserts the resulting
// per-protocol/per-port connectivity, reusing the natTestSpec/runNATTest driver.
//
// After Repopulate, populateConnectivityMatrix marks the peered pair Allow at
// the default ProtoPort{} purely from subnet presence — it does not read the
// ACL. So every ACL test's Overlay must set EXPLICIT protocol/port-scoped
// verdicts via setVPCToVPCProtoVerdict; runMatrixProtoPortPhase then owns all
// probing for those pairs (the legacy phase skips them), including ICMP.
//
// Scope semantics (per the gateway design):
//   - flow (default): stateful matching that relies on the dataplane keeping
//     connection state. It is ONLY accepted on a peering that has stateful NAT
//     (masquerade, possibly port-forward) — that is where the flow/conntrack
//     information exists. A flow-scoped rule on a NAT-free peering is invalid.
//   - packet: stateless, per-direction matching; valid on any peering, no NAT
//     required.
//
// Because most ACL cases here run on NAT-free peerings, their rules use explicit
// Scope:packet (the API default is flow, which those peerings could not accept).
// The flow scope is exercised by a dedicated masquerade-backed case.
//
// Return-path reality: a probe (ping, TCP handshake, iperf3) only succeeds when
// BOTH directions of the flow are permitted. With packet (stateless) scope the
// reply is matched by its own rule, and because a reply has the ports swapped, a
// destination-port allow needs a matching source-port allow for the reverse
// direction. A single one-way packet rule therefore yields NO connectivity (the
// reply is dropped). Cases that need a working allowed flow either permit both
// directions with return-compatible matches (protocol-only, subnet-only, or a
// dst-port + src-port pair) or use flow scope + masquerade, where conntrack
// permits the return automatically.
//
// NOTE: these tests encode the intended behavior ahead of the dataplane
// implementation, so they are expected to fail until ACL enforcement lands.
// Rule precedence is assumed first-match-wins and ICMP is assumed to fall to the
// default action (it matches neither tcp nor udp rules); the precedence and
// protocol tests will reveal these if the dataplane differs.

const (
	// aclProbePort is served by the always-on iperf3 daemon (TCP+UDP), so
	// tests probing it need no on-demand listener.
	aclProbePort uint16 = 5201
	// aclAltPort exercises the on-demand listener path for arbitrary-port ACLs.
	aclAltPort uint16 = 6201
	// aclAltPortRange is a port range containing aclAltPort (6201) but not
	// aclProbePort (5201); used to exercise range matching in the ACL rules.
	aclAltPortRange = "6000-6500"
	// aclUnprobedPort is a port no probe ever targets. The dataplane rejects
	// rule-less ACLs, so default-action tests carry a single narrow rule on
	// this port; because nothing probes it, the default action still governs
	// every path the matrix actually exercises.
	aclUnprobedPort uint16 = 9999
)

// setACLDirVerdicts sets the icmp, tcp/aclProbePort and udp/aclProbePort
// expectations for a single (srcVPC → dstVPC) direction. It is the common
// three-probe shape most ACL cases assert.
func setACLDirVerdicts(m *ConnectivityMatrix, srcVPC, dstVPC string, icmp, tcp, udp ConnectivityVerdict) {
	setVPCToVPCProtoVerdict(m, srcVPC, dstVPC, ProtoPort{Protocol: "icmp"}, icmp)
	setVPCToVPCProtoVerdict(m, srcVPC, dstVPC, ProtoPort{Protocol: "tcp", Port: aclProbePort}, tcp)
	setVPCToVPCProtoVerdict(m, srcVPC, dstVPC, ProtoPort{Protocol: "udp", Port: aclProbePort}, udp)
}

// gatewayACLDefaultDenyTest: default=deny blocks all traffic despite the subnets
// being exposed. The ACL carries one narrow allow rule on an unprobed port
// (rule-less ACLs are rejected by the dataplane); every probed path still falls
// to the default deny.
func gatewayACLDefaultDenyTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL default deny",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{{
					Name: "allow-unprobed", From: vpc1.Name, To: vpc2.Name,
					Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
					Match: gwapi.PeeringACLMatch{
						Protocol:    gwapi.ACLMatchProtocolTCP,
						Destination: []gwapi.PeeringACLMatchEndpoint{{Ports: []string{fmt.Sprintf("%d", aclUnprobedPort)}}},
					},
				}},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictDeny, VerdictDeny, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictDeny, VerdictDeny, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLDenyUnlessExposedTest: default=deny-unless-exposed keeps exposed
// subnets reachable, while an explicit deny rule carves out a slice. Here the
// carve-out denies UDP in both directions, so TCP and ICMP fall to the default
// (exposed ⇒ allowed) and UDP is blocked. This is both the permissive-default
// positive control AND coverage of a Protocol:udp deny rule. UDP denial is
// verifiable because TCP stays allowed, so the iperf3 -u TCP control channel
// still establishes before the datagrams are dropped.
func gatewayACLDenyUnlessExposedTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL deny-unless-exposed with udp carve-out",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDenyUnlessExposed,
				Rules: []gwapi.PeeringACLRule{
					{Name: "deny-udp-fwd", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionDeny, Scope: gwapi.ACLScopePacket, Match: gwapi.PeeringACLMatch{Protocol: gwapi.ACLMatchProtocolUDP}},
					{Name: "deny-udp-rev", From: vpc2.Name, To: vpc1.Name, Action: gwapi.ACLActionDeny, Scope: gwapi.ACLScopePacket, Match: gwapi.PeeringACLMatch{Protocol: gwapi.ACLMatchProtocolUDP}},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			// TCP/ICMP: exposed ⇒ allowed both ways. UDP: denied both ways.
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictAllow, VerdictAllow, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictAllow, VerdictAllow, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLExplicitAllowTest: default=deny plus explicit allow rules for both
// directions restore full connectivity (contrasts the default-deny case).
func gatewayACLExplicitAllowTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL explicit allow rule",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{Name: "allow-fwd", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket},
					{Name: "allow-rev", From: vpc2.Name, To: vpc1.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictAllow, VerdictAllow, VerdictAllow)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictAllow, VerdictAllow, VerdictAllow)

			return nil
		},
	})
}

// gatewayACLProtocolScopingTest: allow TCP in both directions; UDP and ICMP fall
// to the default deny.
func gatewayACLProtocolScopingTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL protocol scoping",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{Name: "allow-tcp-fwd", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket, Match: gwapi.PeeringACLMatch{Protocol: gwapi.ACLMatchProtocolTCP}},
					{Name: "allow-tcp-rev", From: vpc2.Name, To: vpc1.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket, Match: gwapi.PeeringACLMatch{Protocol: gwapi.ACLMatchProtocolTCP}},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictDeny, VerdictAllow, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictDeny, VerdictAllow, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLPacketOneWayTest: a single packet-scoped From:vpc1,To:vpc2 allow rule
// permits only the forward packets; the reply (vpc2→vpc1) has no matching rule
// and hits the default deny, so no probe can complete a handshake or get an ICMP
// reply. This locks in the stateless return-path requirement: a one-way packet
// rule yields NO connectivity in either direction. (Working directional allow is
// covered by the flow+masquerade case, which is stateful.)
func gatewayACLPacketOneWayTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL packet one-way (no return)",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{Name: "allow-fwd", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			// Forward flow can't complete without its return, so both
			// directions are unreachable.
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictDeny, VerdictDeny, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictDeny, VerdictDeny, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLFlowScopeMasqueradeTest: a flow-scoped rule is only valid on a
// peering that has stateful NAT (where the dataplane keeps flow/conntrack
// state), so this case pairs a masquerade NAT on VPC1 with a flow-scoped
// From:vpc1,To:vpc2 allow rule. Masquerade SNAT lets VPC1 reach VPC2's real
// IPs and the flow rule permits that stateful flow (and its return traffic);
// VPC2 cannot initiate (masquerade blocks unsolicited inbound and no reverse
// rule exists). Verifies flow scope is accepted and enforced with masquerade.
func gatewayACLFlowScopeMasqueradeTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	const vpc1NATCIDR = "192.168.81.0/24"

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL flow scope with masquerade",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{Name: "allow-flow", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopeFlow},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR: []string{vpc1NATCIDR},
				VPC1NATMode: NATModeMasquerade,
				ACL:         acl,
			})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			// vpc1→vpc2 rides masquerade SNAT against vpc2's real IPs and is
			// permitted by the flow rule. vpc2→vpc1 is blocked both by
			// masquerade (stateful, no unsolicited inbound) and by the ACL
			// default deny.
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictAllow, VerdictAllow, VerdictAllow)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictDeny, VerdictDeny, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLSubnetScopingTest: packet-scoped rules matching source and
// destination in BOTH directions so the reply is permitted too. The forward rule
// selects by VPCSubnet name and the reverse rule by CIDR, so a single test
// exercises both endpoint selectors. With one subnet per VPC each match covers
// the whole VPC, so this validates the subnet/CIDR match plumbing (with a working
// flow) rather than discriminating between subnets.
func gatewayACLSubnetScopingTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL subnet/CIDR scoping",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			vpc1CIDR, err := vpcFirstSubnetCIDR(vpc1)
			if err != nil {
				return specs, err
			}
			vpc2CIDR, err := vpcFirstSubnetCIDR(vpc2)
			if err != nil {
				return specs, err
			}
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					// forward: select the subnets by VPC subnet name
					{
						Name: "allow-subnet-fwd", From: vpc1.Name, To: vpc2.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Source:      []gwapi.PeeringACLMatchEndpoint{{VPCSubnet: "subnet-01"}},
							Destination: []gwapi.PeeringACLMatchEndpoint{{VPCSubnet: "subnet-01"}},
						},
					},
					// reverse: select the same subnets by CIDR (return path)
					{
						Name: "allow-cidr-rev", From: vpc2.Name, To: vpc1.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Source:      []gwapi.PeeringACLMatchEndpoint{{CIDR: vpc2CIDR}},
							Destination: []gwapi.PeeringACLMatchEndpoint{{CIDR: vpc1CIDR}},
						},
					},
				},
			}
			err = appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			// Neither match carries a port/protocol constraint, so returns
			// match the reverse rule and every protocol flows both ways.
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictAllow, VerdictAllow, VerdictAllow)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictAllow, VerdictAllow, VerdictAllow)

			return nil
		},
	})
}

// gatewayACLPortScopingTest: allow a vpc1→vpc2 TCP flow whose port falls in
// aclAltPortRange. The forward rule matches a destination-port RANGE; the reverse
// rule matches the same SOURCE-port range so the server's replies (whose source
// port is the listener port) are permitted and the handshake completes. The probe
// hits aclAltPort (in range) → reachable forward (exercising the on-demand
// listener); TCP/aclProbePort (out of range), UDP, ICMP, and any vpc2-initiated
// flow fall to the default deny. Covers both port-range matching and src/dst-port
// selectors in one case.
func gatewayACLPortScopingTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL port range scoping",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{
						Name: "allow-alt-fwd", From: vpc1.Name, To: vpc2.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Protocol:    gwapi.ACLMatchProtocolTCP,
							Destination: []gwapi.PeeringACLMatchEndpoint{{Ports: []string{aclAltPortRange}}},
						},
					},
					{
						Name: "allow-alt-ret", From: vpc2.Name, To: vpc1.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Protocol: gwapi.ACLMatchProtocolTCP,
							Source:   []gwapi.PeeringACLMatchEndpoint{{Ports: []string{aclAltPortRange}}},
						},
					},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			// Forward: only TCP/aclAltPort completes (reply rides the src-port
			// return rule). Reverse: a vpc2-initiated connect to aclAltPort has
			// dst=aclAltPort/src=ephemeral, matching neither rule → default deny.
			setVPCToVPCProtoVerdict(matrix, vpc1.Name, vpc2.Name, ProtoPort{Protocol: "tcp", Port: aclAltPort}, VerdictAllow)
			setVPCToVPCProtoVerdict(matrix, vpc1.Name, vpc2.Name, ProtoPort{Protocol: "tcp", Port: aclProbePort}, VerdictDeny)
			setVPCToVPCProtoVerdict(matrix, vpc1.Name, vpc2.Name, ProtoPort{Protocol: "udp", Port: aclProbePort}, VerdictDeny)
			setVPCToVPCProtoVerdict(matrix, vpc1.Name, vpc2.Name, ProtoPort{Protocol: "icmp"}, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictDeny, VerdictDeny, VerdictDeny)
			setVPCToVPCProtoVerdict(matrix, vpc2.Name, vpc1.Name, ProtoPort{Protocol: "tcp", Port: aclAltPort}, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLPrecedenceAllowThenDenyTest: an allow for TCP/aclProbePort ahead of a
// broad deny (packet-scoped, vpc1→vpc2). Under first-match-wins TCP/aclProbePort
// is allowed forward while everything else is denied. A reverse source-port rule
// lets the server's replies through so the allowed flow completes; the reverse
// direction otherwise follows the default deny.
func gatewayACLPrecedenceAllowThenDenyTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	probePortStr := fmt.Sprintf("%d", aclProbePort)

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL precedence allow-then-deny",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{
						Name: "allow-tcp", From: vpc1.Name, To: vpc2.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Protocol:    gwapi.ACLMatchProtocolTCP,
							Destination: []gwapi.PeeringACLMatchEndpoint{{Ports: []string{probePortStr}}},
						},
					},
					{Name: "deny-all", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionDeny, Scope: gwapi.ACLScopePacket},
					{
						Name: "allow-tcp-ret", From: vpc2.Name, To: vpc1.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Protocol: gwapi.ACLMatchProtocolTCP,
							Source:   []gwapi.PeeringACLMatchEndpoint{{Ports: []string{probePortStr}}},
						},
					},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictDeny, VerdictAllow, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictDeny, VerdictDeny, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLPrecedenceDenyThenAllowTest: the reverse rule order of the previous
// test (same rule set, including the source-port return rule — only the order of
// the forward allow/deny is swapped). Under first-match-wins the broad deny
// matches first, so even TCP/aclProbePort is denied and nothing gets through.
func gatewayACLPrecedenceDenyThenAllowTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	probePortStr := fmt.Sprintf("%d", aclProbePort)

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL precedence deny-then-allow",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{Name: "deny-all", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionDeny, Scope: gwapi.ACLScopePacket},
					{
						Name: "allow-tcp", From: vpc1.Name, To: vpc2.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Protocol:    gwapi.ACLMatchProtocolTCP,
							Destination: []gwapi.PeeringACLMatchEndpoint{{Ports: []string{probePortStr}}},
						},
					},
					{
						Name: "allow-tcp-ret", From: vpc2.Name, To: vpc1.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Protocol: gwapi.ACLMatchProtocolTCP,
							Source:   []gwapi.PeeringACLMatchEndpoint{{Ports: []string{probePortStr}}},
						},
					},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictDeny, VerdictDeny, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictDeny, VerdictDeny, VerdictDeny)

			return nil
		},
	})
}

// getACLTestCases returns the gateway peering ACL test cases added to the
// multi-VPC single-subnet suite.
func getACLTestCases() []JUnitTestCase {
	return []JUnitTestCase{
		{Name: "Gateway Peering ACL Default Deny", F: gatewayACLDefaultDenyTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Deny-Unless-Exposed UDP Carve-Out", F: gatewayACLDenyUnlessExposedTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Explicit Allow", F: gatewayACLExplicitAllowTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Protocol Scoping", F: gatewayACLProtocolScopingTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Packet One-Way", F: gatewayACLPacketOneWayTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Flow Scope Masquerade", F: gatewayACLFlowScopeMasqueradeTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Subnet/CIDR Scoping", F: gatewayACLSubnetScopingTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Port Range Scoping", F: gatewayACLPortScopingTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Precedence Allow-Then-Deny", F: gatewayACLPrecedenceAllowThenDenyTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Precedence Deny-Then-Allow", F: gatewayACLPrecedenceDenyThenAllowTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
	}
}
