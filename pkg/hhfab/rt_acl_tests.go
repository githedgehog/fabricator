// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"fmt"

	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
)

const (
	// aclProbePort is served by the always-on iperf3 daemon (TCP+UDP), so
	// tests probing it need no on-demand listener.
	aclProbePort    uint16 = persistentIperf3Port
	aclAltPort      uint16 = 6201
	aclAltPortRange        = "6000-6500"
	aclUnprobedPort uint16 = 9999

	// the dataplane has no icmp keyword yet, a numeric protocol is the only
	// way to match ICMP explicitly
	aclProtoICMP gwapi.ACLMatchProtocol = "1"
)

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
// subnets reachable, while an explicit deny rule carves out a slice.
// This is both the permissive-default positive control AND coverage of a
// Protocol:udp deny rule.
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
// directions restore full connectivity
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
// and hits the default deny, so no probe can complete a handshake or get an ICMP reply.
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
// exercises both endpoint selectors.
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
			vpc1Subnet, err := vpcFirstSubnetName(vpc1)
			if err != nil {
				return specs, err
			}
			vpc2Subnet, err := vpcFirstSubnetName(vpc2)
			if err != nil {
				return specs, err
			}
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{
						Name: "allow-subnet-fwd", From: vpc1.Name, To: vpc2.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Source:      []gwapi.PeeringACLMatchEndpoint{{VPCSubnet: vpc1Subnet}},
							Destination: []gwapi.PeeringACLMatchEndpoint{{VPCSubnet: vpc2Subnet}},
						},
					},
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
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictAllow, VerdictAllow, VerdictAllow)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictAllow, VerdictAllow, VerdictAllow)

			return nil
		},
	})
}

// gatewayACLPortScopingTest: allow a vpc1→vpc2 TCP flow whose port falls in
// aclAltPortRange. The forward rule matches a destination-port RANGE; the reverse
// rule matches the same SOURCE-port range so the server's replies (whose source
// port is the listener port) are permitted and the handshake completes.
// Covers both port-range matching and src/dst-port selectors in one case.
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
// test. Under first-match-wins the broad deny
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

// gatewayACLNumericProtocolICMPTest: match ICMP explicitly via the numeric
// protocol 1 (there is no icmp keyword yet). Every other ACL case only ever
// sees ICMP fall through to the default action, so this is the one that asserts
// a rule can match it. TCP/UDP hit the default deny.
func gatewayACLNumericProtocolICMPTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL numeric protocol ICMP",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{Name: "allow-icmp-fwd", From: vpc1.Name, To: vpc2.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket, Match: gwapi.PeeringACLMatch{Protocol: aclProtoICMP}},
					{Name: "allow-icmp-rev", From: vpc2.Name, To: vpc1.Name, Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket, Match: gwapi.PeeringACLMatch{Protocol: aclProtoICMP}},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{ACL: acl})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictAllow, VerdictDeny, VerdictDeny)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictAllow, VerdictDeny, VerdictDeny)

			return nil
		},
	})
}

// gatewayACLPreNATDestinationTest: ACL rules are evaluated before NAT, so
// match.dst is compared against the address the initiator dialed — the peer's
// advertised (expose "as") pool — not the destination's native IP. Both
// directions get bidirectional static NAT and a rule matching the peer's NAT
// pool; an implementation that matched post-NAT would deny everything.
func gatewayACLPreNATDestinationTest(ctx context.Context, testCtx *VPCPeeringTestCtx, matrix *ConnectivityMatrix) (bool, []RevertFunc, error) {
	const (
		vpc1NATCIDR = "192.168.91.0/24"
		vpc2NATCIDR = "192.168.92.0/24"
	)

	return testCtx.runNATTest(ctx, matrix, natTestSpec{
		Name: "gateway ACL pre-NAT destination match",
		BuildSpec: func(vpc1, vpc2 *vpcapi.VPC) (peeringSpecs, error) {
			specs := emptyPeeringSpecs()
			acl := &gwapi.PeeringACL{
				Default: gwapi.ACLDefaultDeny,
				Rules: []gwapi.PeeringACLRule{
					{
						Name: "allow-as-fwd", From: vpc1.Name, To: vpc2.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Destination: []gwapi.PeeringACLMatchEndpoint{{CIDR: vpc2NATCIDR}},
						},
					},
					{
						Name: "allow-as-rev", From: vpc2.Name, To: vpc1.Name,
						Action: gwapi.ACLActionAllow, Scope: gwapi.ACLScopePacket,
						Match: gwapi.PeeringACLMatch{
							Destination: []gwapi.PeeringACLMatchEndpoint{{CIDR: vpc1NATCIDR}},
						},
					},
				},
			}
			err := appendGwPeeringSpec(specs.Gateway, vpc1, vpc2, &GwPeeringOptions{
				VPC1NATCIDR: []string{vpc1NATCIDR},
				VPC2NATCIDR: []string{vpc2NATCIDR},
				ACL:         acl,
			})

			return specs, err
		},
		Overlay: func(vpc1, vpc2 *vpcapi.VPC, matrix *ConnectivityMatrix) error {
			vpc1CIDR, err := vpcFirstSubnetCIDR(vpc1)
			if err != nil {
				return err
			}
			vpc2CIDR, err := vpcFirstSubnetCIDR(vpc2)
			if err != nil {
				return err
			}
			// static NAT is bidirectional: each side only knows the other by
			// its advertised pool, so every probe targets the DNAT address
			if err := overlayVPCToVPCStaticDNAT(matrix, vpc1.Name, vpc2.Name, vpc2CIDR, vpc2NATCIDR); err != nil {
				return fmt.Errorf("overlaying vpc2 static DNAT: %w", err)
			}
			if err := overlayVPCToVPCStaticDNAT(matrix, vpc2.Name, vpc1.Name, vpc1CIDR, vpc1NATCIDR); err != nil {
				return fmt.Errorf("overlaying vpc1 static DNAT: %w", err)
			}
			setACLDirVerdicts(matrix, vpc1.Name, vpc2.Name, VerdictAllow, VerdictAllow, VerdictAllow)
			setACLDirVerdicts(matrix, vpc2.Name, vpc1.Name, VerdictAllow, VerdictAllow, VerdictAllow)

			return nil
		},
	})
}

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
		{Name: "Gateway Peering ACL Numeric Protocol ICMP", F: gatewayACLNumericProtocolICMPTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
		{Name: "Gateway Peering ACL Pre-NAT Destination Match", F: gatewayACLPreNATDestinationTest, SkipFlags: SkipFlags{NoGateway: true, NoServers: true}},
	}
}
