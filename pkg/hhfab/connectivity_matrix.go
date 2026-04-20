// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"net/netip"
	"sort"
)

// Additional reachability reasons used by the connectivity matrix. The base
// set (IntraVPC, SwitchPeering, GatewayPeering) is declared in testing.go for
// backward compatibility with the legacy Reachability struct.
const (
	ReachabilityReasonExternalPeering ReachabilityReason = "external-peering"
	ReachabilityReasonImplicitDeny    ReachabilityReason = "implicit-deny"
)

// ConnectivityVerdict describes what should happen to traffic on a specific path.
type ConnectivityVerdict string

const (
	VerdictAllow ConnectivityVerdict = "allow"
	VerdictDeny  ConnectivityVerdict = "deny"
)

// ConnectivityDirection describes which direction(s) traffic should flow.
type ConnectivityDirection string

const (
	DirectionBidirectional ConnectivityDirection = "bidirectional"
	DirectionForward       ConnectivityDirection = "forward" // source → destination only
	DirectionReverse       ConnectivityDirection = "reverse" // destination → source only
)

// ProtoPort is a protocol + port tuple used for firewall/port-forward expectations.
type ProtoPort struct {
	Protocol string // "tcp", "udp"
	Port     uint16
}

// TranslatedAddress captures the NAT translation expected on a path.
type TranslatedAddress struct {
	// SourceIP is the source IP the destination should see after SNAT.
	// When the NAT pool is a CIDR wider than /32 (masquerade), SourceIP holds
	// the pool network address and SourcePool is set for prefix containment checks.
	SourceIP   netip.Addr
	SourcePool netip.Prefix

	// DestinationIP is the IP the source must target to reach the destination
	// (DNAT). Empty means the destination's real IP is the target.
	DestinationIP netip.Addr

	// PortForwards maps external port → internal port for port-forward NAT.
	// Phase 1 executor ignores this field; it is populated for later phases.
	PortForwards map[ProtoPort]ProtoPort
}

// EndpointKey uniquely identifies one logical endpoint. A server attached to
// multiple VPC subnets becomes multiple EndpointKeys — one per (server, subnet).
type EndpointKey struct {
	Server string // server name, or external name for External endpoints
	Subnet string // full VPC subnet name "<vpc>/<subnet>", empty for externals
}

func (k EndpointKey) String() string {
	if k.Subnet == "" {
		return k.Server
	}

	return k.Server + "@" + k.Subnet
}

// EndpointPair is a directional (source, destination) key.
type EndpointPair struct {
	Source      EndpointKey
	Destination EndpointKey
}

// Endpoint is a test endpoint: a (server, subnet) slot with a resolved IP.
type Endpoint struct {
	Server  string
	Subnet  string
	IP      netip.Addr
	HostBGP bool

	// External is true when this endpoint represents an external destination
	// (not a fabric-attached server). Phase 1 keeps externals out of the matrix
	// and handles them via the existing curl path.
	External     bool
	ExternalName string
}

func (e Endpoint) Key() EndpointKey {
	return EndpointKey{Server: e.Server, Subnet: e.Subnet}
}

// ConnectivityExpectation describes what should happen between two endpoints.
type ConnectivityExpectation struct {
	Source      EndpointKey
	Destination EndpointKey

	Verdict   ConnectivityVerdict
	Direction ConnectivityDirection

	// NAT, when set, describes the address translation on this path.
	NAT *TranslatedAddress

	// Reason records why the expectation exists. Reuses ReachabilityReason so
	// the existing IsServerReachable shim can surface it unchanged.
	Reason ReachabilityReason

	// Peering is the CRD name that produced this expectation ("" for intra-VPC).
	Peering string

	// ProtoPort, when set, restricts the expectation to a specific L4 tuple.
	// Nil means the expectation applies to ICMP + TCP/any (Phase 1 default).
	ProtoPort *ProtoPort
}

// Match reports whether src/dst/proto match this expectation.
func (e ConnectivityExpectation) Match(src, dst EndpointKey, pp *ProtoPort) bool {
	if e.Source != src || e.Destination != dst {
		return false
	}
	if e.ProtoPort == nil || pp == nil {
		return true
	}

	return *e.ProtoPort == *pp
}

// ConnectivityMatrix holds the complete set of expectations for a topology.
type ConnectivityMatrix struct {
	entries      map[EndpointPair][]ConnectivityExpectation
	allEndpoints []Endpoint
}

// NewConnectivityMatrix returns an empty matrix initialised with the given
// endpoints. Endpoints are sorted by key for stable iteration order.
func NewConnectivityMatrix(endpoints []Endpoint) *ConnectivityMatrix {
	ep := append([]Endpoint(nil), endpoints...)
	sort.Slice(ep, func(i, j int) bool {
		return ep[i].Key().String() < ep[j].Key().String()
	})

	return &ConnectivityMatrix{
		entries:      map[EndpointPair][]ConnectivityExpectation{},
		allEndpoints: ep,
	}
}

// Endpoints returns the ordered list of all endpoints in the matrix.
func (m *ConnectivityMatrix) Endpoints() []Endpoint {
	return m.allEndpoints
}

// Add appends an expectation to the matrix.
func (m *ConnectivityMatrix) Add(e ConnectivityExpectation) {
	pair := EndpointPair{Source: e.Source, Destination: e.Destination}
	m.entries[pair] = append(m.entries[pair], e)
}

// Get returns all expectations recorded for the (source, destination) pair.
// Callers receive an empty slice when no explicit entry exists; the executor
// treats absent entries as implicit DENY.
func (m *ConnectivityMatrix) Get(src, dst EndpointKey) []ConnectivityExpectation {
	return m.entries[EndpointPair{Source: src, Destination: dst}]
}

// GetForProto returns the expectation that best matches the given protocol/port,
// falling back to the default (proto-agnostic) entry if no proto-specific one
// matches. When no expectation exists at all, returns an implicit-DENY record.
func (m *ConnectivityMatrix) GetForProto(src, dst EndpointKey, pp ProtoPort) ConnectivityExpectation {
	var fallback *ConnectivityExpectation
	for _, e := range m.entries[EndpointPair{Source: src, Destination: dst}] {
		if e.ProtoPort != nil && *e.ProtoPort == pp {
			return e
		}
		if e.ProtoPort == nil {
			fallback = &e
		}
	}
	if fallback != nil {
		return *fallback
	}

	return ConnectivityExpectation{
		Source:      src,
		Destination: dst,
		Verdict:     VerdictDeny,
		Direction:   DirectionBidirectional,
		Reason:      ReachabilityReasonImplicitDeny,
	}
}
