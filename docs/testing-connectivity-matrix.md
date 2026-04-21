# Connectivity Matrix: Redesigning test-connectivity

This document designs the evolution of `test-connectivity` from a model that
covers VPC and switch peering — symmetric, single IP per server, NAT-blind —
into one that also handles gateway peering with NAT, directional asymmetry,
multi-VPC attachment (VLAN trunking), HostBGP subnets, and future 5-tuple
firewall rules. Both models derive expectations exhaustively from live API
objects; the new design covers network configurations the current one cannot
represent.

## Problem Statement

The current `test-connectivity` ([`testing.go:2066-2424`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2066-L2424)) determines expected
reachability per server pair by querying VPCPeering, GatewayPeering, and
ExternalPeering CRDs. It then runs ping/iPerf/curl to confirm. This model
has several shortcomings that forced a bypass when gateway peerings were
introduced and will break entirely when 5-tuple firewall lands:

1. **NAT-unaware.** Pings target the server's real IP (`ipB.Addr()`), not the
   NATted address. Masquerade and static NAT paths are not validated — the
   release tests in `rt_nat_tests.go` handle this, but `test-connectivity`
   cannot.

2. **No directional asymmetry.** Masquerade NAT is one-directional (initiator
   only), but `test-connectivity` tests A→B and B→A symmetrically — both
   directions get the same `IsServerReachable` result since the peering CRD is
   bidirectional. This affects gateway peerings without NAT (i.e., without
   `expose.As`); NAT peerings error out at Problem 5 before reaching the ping
   stage. [`rt_nat_tests.go:293-303`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/rt_nat_tests.go#L293-L303) works around this in release tests, but
   `test-connectivity` has no concept of one-way reachability.

3. **Negative checks are implicit.** "Unreachable" means "no peering found."
   There is no positive verification that traffic is actually blocked. A
   firewall DENY rule requires proof of drop, not just absence of ALLOW.

4. **No protocol/port granularity.** The model is (serverA, serverB) → bool.
   A 5-tuple firewall needs (serverA:port, serverB:port, protocol) → action.

5. **`expose.As` unsupported.** Peerings that remap subnets to different CIDRs
   error out ([`testing.go:2722-2723`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2722-L2723)). Since `expose.As` is required
   for all NAT modes, any NAT gateway peering fails the entire test with an
   error rather than a wrong result — a separate failure mode from Problems 1
   and 2, which only manifest for gateway peerings without NAT.

6. **Single IP per server.** The IP discovery stores one address per server
   ([`ips.Store(server, addr)`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2220)) and errors if a second exists
   ([`"unexpected multiple ip addrs"`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2211), [`testing.go:2209-2220`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2209-L2220)). This blocks two
   common topologies:
   - **Multi-VPC attachment (VLAN trunking):** a server on `vpc-1` and
     `vpc-2` has two IPs on two VLAN interfaces; the code errors on the
     second one. Even if both were stored, the ping path has no way to
     select the correct per-subnet IP.
   - **HostBGP subnets:** the reachable address is a /32 VIP on the
     loopback, not a subnet-assigned interface address. IP discovery does
     not know to query `hhnet getvips`. For a simple single-attachment
     HostBGP server (unnumbered BGP, no IPs on fabric interfaces), the
     /32 VIP on `lo` passes the current filter and is picked up as the
     sole data-plane IP — which accidentally works for ping. The code
     breaks as soon as the server has any other non-management address
     (second subnet attachment, multihoming), erroring with
     `"unexpected multiple ip addrs"`, and it never treats the address
     as a HostBGP VIP regardless.

7. **Self-described as temporary.** The gateway peering function is annotated:
   "It's just a temporary function for simple check only supporting whole VPC
   subnet CIDRs" ([`testing.go:2643`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2643)).

## Current Architecture

```
test-connectivity (on-ready command)
│
├── For each (serverA, serverB) pair:
│   ├── IsServerReachable()
│   │   ├── GetAttachedSubnets(serverA), GetAttachedSubnets(serverB)
│   │   └── For each (srcSubnet, dstSubnet):
│   │       └── IsSubnetReachable()
│   │           ├── Same VPC? → intra-vpc ✓
│   │           ├── IsSubnetReachableWithSwitchPeering() → VPCPeering CRD check
│   │           └── IsSubnetReachableWithGatewayPeering() → GatewayPeering CRD check
│   │               └── isVPCSubnetPresentInPeering() → expose entry matching
│   │
│   ├── checkPing(expectedReachable.Reachable)
│   └── checkIPerf(expectedReachable)
│
└── For each serverA (curl):
    ├── IsExternalSubnetReachable()
    └── checkCurl(expectedReachable.Reachable)
```

All decisions are binary. NAT mode, direction, and protocol are ignored.

## Design Goals

1. **Derive from API objects** — same as today. No annotations or side-channel
   config. The matrix builder reads VPCPeering, GatewayPeering, ExternalPeering,
   VPC, VPCInfo, and future FirewallPolicy CRDs.

2. **All-to-all matrix** — every (source endpoint, destination endpoint) pair
   gets an explicit expectation. An endpoint is a (server, subnet) pair, so a
   server on two VPCs is two endpoints. No pair is left ambiguous.

3. **Positive and negative checks** — verify allowed traffic passes AND blocked
   traffic is dropped. Negative checks use a short timeout and expect failure.

4. **NAT-aware** — when a peering uses NAT, test against the translated address.
   Verify SNAT source appears correctly at the destination. Verify DNAT
   reaches the real backend.

5. **Directional** — masquerade is initiator-only. The matrix encodes which
   direction(s) are expected to work.

6. **Protocol/port granularity** — prepare for 5-tuple firewall without
   requiring it today. Default tests use ICMP + TCP/any. Firewall rules
   add specific port expectations.

7. **Multi-VPC and HostBGP support** — servers attached to multiple VPC
   subnets (VLAN trunking) and HostBGP servers with /32 VIPs are first-class
   endpoints. IP discovery resolves per (server, subnet), not per server.

8. **Maintain on-ready position** — test-connectivity remains an on-ready
   command. Release tests continue to consume `IsServerReachable` and related
   functions from testing.go via a backward-compatible wrapper.

9. **Incremental migration** — can be rolled out in phases. Phase 1 fixes
   the gateway peering bypass. Phase 3 adds firewall support.

## Connectivity Expectation Model

### Core Types

```go
// ConnectivityVerdict describes what should happen to traffic on a specific path.
type ConnectivityVerdict string

const (
    VerdictAllow ConnectivityVerdict = "allow"  // Traffic should pass
    VerdictDeny  ConnectivityVerdict = "deny"   // Traffic should be dropped
)

// ConnectivityDirection describes which direction(s) traffic should flow.
type ConnectivityDirection string

const (
    DirectionBidirectional ConnectivityDirection = "bidirectional"
    DirectionForward       ConnectivityDirection = "forward"  // src → dst only
    DirectionReverse       ConnectivityDirection = "reverse"  // dst → src only
)

// TranslatedAddress describes NAT translation expected on a path.
type TranslatedAddress struct {
    // SNAT: the source IP the destination should see (e.g., masquerade pool IP).
    // Empty means no SNAT or SNAT verification is not required.
    SourceIP netip.Addr

    // DNAT: the IP the source should target to reach the destination.
    // Empty means use the destination's real IP.
    DestinationIP netip.Addr

    // Port mappings for port-forward NAT.
    // Key: external port, Value: internal port.
    PortForwards map[ProtoPort]ProtoPort
}

// ProtoPort is a protocol + port tuple.
type ProtoPort struct {
    Protocol string // "tcp", "udp"
    Port     uint16
}

// ConnectivityExpectation describes what should happen between two endpoints.
type ConnectivityExpectation struct {
    Source      Endpoint
    Destination Endpoint

    // Verdict: should traffic be allowed or denied?
    Verdict ConnectivityVerdict

    // Direction: is the path symmetric or one-way?
    Direction ConnectivityDirection

    // NAT: if set, describes address translation on this path.
    NAT *TranslatedAddress

    // Reason: why this expectation exists (for diagnostics).
    Reason ReachabilityReason

    // Peering: name of the CRD that created this expectation.
    Peering string

    // ProtoPort: if set, this expectation applies only to this protocol/port.
    // If nil, applies to ICMP + TCP/any (default connectivity check).
    ProtoPort *ProtoPort
}

// Endpoint identifies a test endpoint.
type Endpoint struct {
    // Server name (e.g., "server-1") for fabric-attached servers.
    Server string

    // Subnet: full VPC subnet name (e.g., "vpc-1/default").
    Subnet string

    // IP: resolved IP address of the endpoint.
    // For regular servers: DHCP/static address on the subnet interface.
    // For HostBGP servers: the /32 VIP on the loopback, discovered at runtime.
    IP netip.Addr

    // HostBGP: if true, this server uses BGP to advertise a /32 VIP.
    // The IP field contains the VIP; connectivity targets this address, not a
    // subnet-assigned address. See HostBGP Endpoint Discovery below.
    HostBGP bool

    // External: if true, this is an external endpoint (not a fabric server).
    External bool

    // ExternalName: name of the External CRD (when External is true).
    ExternalName string
}
```

### Connectivity Matrix

```go
// ConnectivityMatrix holds the complete set of expectations for a topology.
type ConnectivityMatrix struct {
    // Expectations indexed by (source, destination) for O(1) lookup.
    entries map[EndpointPair][]ConnectivityExpectation

    // AllEndpoints: ordered list of all endpoints in the matrix.
    AllEndpoints []Endpoint
}

// EndpointKey uniquely identifies one logical endpoint.
// A single physical server attached to multiple VPC subnets (via VLAN tagging)
// becomes multiple EndpointKeys — one per (server, subnet) pair.
type EndpointKey struct {
    Server string // server name (e.g., "server-1") or external name
    Subnet string // full VPC subnet name (e.g., "vpc-1/default"), empty for externals
}

// String returns a stable key for map indexing (e.g., "server-1/vpc-1/default").
func (k EndpointKey) String() string

// EndpointPair is a directional (source, destination) key.
type EndpointPair struct {
    Source      EndpointKey
    Destination EndpointKey
}

// Get returns all expectations for a given (source, destination) pair.
// If no explicit expectation exists, returns a default DENY (isolation).
func (m *ConnectivityMatrix) Get(src, dst EndpointKey) []ConnectivityExpectation

// GetForProto returns the expectation for a specific protocol/port.
// Falls back to the general expectation if no proto-specific one exists.
func (m *ConnectivityMatrix) GetForProto(src, dst EndpointKey, pp ProtoPort) ConnectivityExpectation
```

## HostBGP Endpoint Discovery

HostBGP subnets (`VPC.spec.subnets[name].hostBGP: true`) require special
handling throughout the matrix because the reachable address is not
derivable from API objects.

### How HostBGP works

A HostBGP server runs FRR via the `ghcr.io/githedgehog/host-bgp` container.
It establishes unnumbered BGP sessions on its VPC-facing interfaces and
advertises one or more `/32` Virtual IPs (VIPs) from its loopback. The
leaves accept only /32 routes within the VPC subnet and export routes
learned from VPC peers. The server is reachable fabric-wide via its VIP,
not via a DHCP-assigned address.

The intended deployment is **active-active multihoming**: one server
connected to multiple leaves via separate unbundled connections, same VPC
subnet on each. Both paths advertise the same /32 VIP; the fabric routes
via ECMP across both leaves.

### Why API objects are not enough

The VIP is operator-chosen and live only while the FRR container is running.
It does not appear in any VPC, VPCAttachment, or VPCInfo CRD. The only
authoritative sources are:

1. The server's loopback (`ip addr show lo | grep /32`) — requires SSH.
2. The leaf's BGP table for the VPC VRF (`show ip route vrf VrfV<vpc-name>`)
   — any connected leaf will have it once BGP converges.

The endpoint discovery phase **must use runtime SSH** to the server and
query `/opt/bin/hhnet getvips` (which returns /32 prefixes from the
loopback), same as the existing on-ready setup flow at [`testing.go:1058`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L1058).

### Impact on the Endpoint struct

- `Endpoint.HostBGP = true` for servers on a HostBGP subnet.
- `Endpoint.IP` is populated from `hhnet getvips` output, not from
  `ip addr show` on data interfaces.
- The existing `TestConnectivity` IP discovery (`ip addr show` + "multiple
  IPs = error") breaks for HostBGP servers that also have other addresses
  (e.g., management or second-subnet IPs). The matrix executor must use a
  dedicated HostBGP path: filter `ip addr show lo` for /32 addresses in the
  VPC subnet range, or call `hhnet getvips` directly.

### Impact on IntraVPCProvider

When a VPC subnet has `hostBGP: true`, the subnet's servers do not have a
subnet-assigned address — they only have a /32 VIP. The provider must:

- Mark all endpoints on that subnet as `HostBGP: true`.
- **Not** attempt to derive the IP from the VPCInfo subnet CIDR or DHCP.
- Defer IP resolution to the runtime endpoint discovery phase.
- Still generate the same ALLOW/DENY expectations as for regular subnets
  (the reachability rules — peering, isolation, restriction — are
  unchanged; only the target address differs).

### Multihoming and test scope

The primary HostBGP use case is multihomed servers on the TH5 platform
(a server connected to multiple leaves via unbundled connections). A full
multihomed test requires:

1. **Single-path reachability**: each physical link can independently reach
   the VIP (test with one link up, other down — requires interface
   manipulation, not just pings).
2. **ECMP reachability**: both paths active, traffic distributed across
   both leaves — observable via traffic counters on the leaves.
3. **Failover**: simulate a leaf failure and verify the VIP remains reachable
   via the surviving path.

These are **out of scope for Phase 1**. The matrix executor validates
reachability to the VIP as an opaque /32 destination — it does not verify
which physical path traffic takes. Full multihoming validation is tracked
separately and requires leaf-side state inspection (switch API or
`show ip route`). Phase 1 coverage (single-attachment HostBGP, as in
githedgehog/fabricator#1648) is acceptable as a starting point.

### Connectivity check adjustments for HostBGP

| Aspect | Regular server | HostBGP server |
|---|---|---|
| IP source | DHCP/static on data interface | /32 VIP on loopback, discovered via SSH |
| Ping target | `Endpoint.IP` (interface address) | `Endpoint.IP` (VIP /32) |
| iPerf target | same | same (iPerf binds to any; VIP is routable) |
| socat listener | bind to interface address | bind to VIP on lo |
| SNAT verification | capture at interface | capture at loopback (`-i lo` or `-i any`) |
| Expected prefix length | matches subnet prefix | always /32 |

## Multi-VPC Attachment (VLAN Trunking)

A server can be attached to multiple VPC subnets on the same physical
connection via VLAN tagging (802.1q trunking). For example:

```
server-1 ── leaf-1
  ├── vpc-1/default (VLAN 1001) → enp2s1.1001 → 10.0.1.5/24
  └── vpc-2/default (VLAN 1002) → enp2s1.1002 → 10.0.2.5/24
```

This breaks the current `TestConnectivity` at multiple points:

1. **IP discovery** ([`testing.go:2211`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2211)): `"unexpected multiple ip addrs"` —
   `ip addr show` returns two addresses and the code errors.

2. **Server-to-IP mapping** ([`testing.go:2220`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2220)): `ips.Store(server, addr)` is
   a flat `server → single IP` map. There is no place to store a second IP.

3. **Address selection** ([`testing.go:2302`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2302)): after determining that `server-2`
   can reach `server-1` via `vpc-1/default`, the code loads `server-1`'s
   single stored IP — which might be the `vpc-2` address, not the `vpc-1`
   address. The reachability logic is correct (`IsServerReachable` iterates
   all subnet pairs); the IP it pings is wrong.

### Design impact: one server, many endpoints

A server attached to N VPC subnets produces **N logical endpoints** in the
matrix. `server-1` on `vpc-1/default` at `10.0.1.5` and `server-1` on
`vpc-2/default` at `10.0.2.5` have different reachability (different
peerings, isolation rules, NAT). This is why `EndpointPair` keys on
`EndpointKey{Server, Subnet}`, not just server name.

The relationship is:
```
VPCAttachment  ──→  (Server, Subnet) pair  ──→  Endpoint  ──→  matrix entry
```

A server with 3 VPC attachments becomes 3 `Endpoint` entries. A server
with 1 VPC attachment becomes 1 `Endpoint`. No special case.

### Endpoint discovery for multi-VPC servers

`discoverEndpoints()` creates one `Endpoint` per `(server, subnet)` by
iterating VPCAttachments (not servers). For IP resolution:

1. SSH to the server and run `ip -o -4 addr show | awk '{print $2, $4}'`
   (same as today, but collect **all** results instead of erroring on the
   second one).
2. Match each discovered IP to a VPC subnet CIDR — the IP `10.0.1.5` on
   interface `enp2s1.1001` belongs to subnet `10.0.1.0/24` which is
   `vpc-1/default`.
3. Populate `Endpoint.IP` per match.

For HostBGP subnets on the same server, use the separate `hhnet getvips`
path (see HostBGP Endpoint Discovery above). A server can have both
regular and HostBGP subnet attachments — each produces its own `Endpoint`.

### Deduplication

Two scenarios produce duplicate `(server, subnet)` VPCAttachments:

| Scenario | Example | Handling |
|---|---|---|
| Multi-VPC trunking | server-1 on vpc-1 + vpc-2 via same connection | Distinct subnets → distinct endpoints (no dedup needed) |
| HostBGP multihoming | server-1 on vpc-1/default via leaf-1 AND leaf-2 | Same (server, subnet) → deduplicate to one endpoint |

Deduplication: after collecting all `(server, subnet)` pairs from
VPCAttachments, collapse duplicates. The IP is the same for the same
(server, subnet) regardless of which leaf the attachment goes through.

## Matrix Builder

The matrix builder constructs a `ConnectivityMatrix` from live API objects.
It uses a chain of providers, each responsible for one type of connectivity.
Providers are applied in order; later providers can override earlier ones
(e.g., a firewall DENY overrides a peering ALLOW).

```go
type MatrixBuilder struct {
    kube      kclient.Reader
    providers []ConnectivityProvider
}

// ConnectivityProvider produces expectations from API objects.
type ConnectivityProvider interface {
    // Name returns the provider name for logging.
    Name() string

    // BuildExpectations returns expectations derived from API objects.
    // It receives the current matrix to allow overrides/refinements.
    BuildExpectations(ctx context.Context, kube kclient.Reader,
        endpoints []Endpoint, current *ConnectivityMatrix,
    ) ([]ConnectivityExpectation, error)
}

func NewMatrixBuilder(kube kclient.Reader, gatewayEnabled bool) *MatrixBuilder {
    providers := []ConnectivityProvider{
        &IntraVPCProvider{},
        &SwitchPeeringProvider{},
        &ExternalPeeringProvider{},
    }
    if gatewayEnabled {
        providers = append(providers, &GatewayPeeringProvider{})
    }
    // Future: append FirewallProvider when CRD exists
    return &MatrixBuilder{kube: kube, providers: providers}
}
```

### Provider: IntraVPCProvider

Derives expectations from VPC subnet configuration. Handles:
- Servers on the same subnet → ALLOW (bidirectional)
- Servers on different subnets within same VPC → ALLOW if not isolated/restricted
- Respects VPC subnet isolation and restriction flags

```go
func (p *IntraVPCProvider) BuildExpectations(ctx context.Context, kube kclient.Reader,
    endpoints []Endpoint, current *ConnectivityMatrix,
) ([]ConnectivityExpectation, error) {
    // Group endpoints by VPC
    // For each VPC, check subnet isolation/restriction settings
    // Generate ALLOW for reachable intra-VPC pairs
    // Default: pairs across VPCs with no peering get no entry (implicit DENY)
}
```

### Provider: SwitchPeeringProvider

Derives expectations from VPCPeering CRDs. Handles:
- Permit lists with optional subnet filtering
- Remote peerings (cross-switch-group)

Logic mirrors current `IsSubnetReachableWithSwitchPeering` but outputs
`ConnectivityExpectation` structs instead of a `Reachability` struct.

### Provider: GatewayPeeringProvider

This is the critical provider that fixes the current limitations. It derives
expectations from GatewayPeering CRDs and their associated VPCInfo objects.

```go
func (p *GatewayPeeringProvider) BuildExpectations(ctx context.Context, kube kclient.Reader,
    endpoints []Endpoint, current *ConnectivityMatrix,
) ([]ConnectivityExpectation, error) {
    // List all GatewayPeering CRDs
    // For each peering:
    //   1. Identify the two sides (VPC-to-VPC or VPC-to-External)
    //   2. For each side, fetch VPCInfo to get subnet CIDRs
    //   3. For each expose entry on each side, call resolveNATTranslation()
    //      to compute TranslatedAddress and Direction
    //   4. Build expectations with correct:
    //      - Direction (masquerade = forward-only from initiator side)
    //      - NAT addresses (see NAT Address Resolution below)
    //      - Port forwards (DNAT port mappings)
}
```

#### NAT Address Resolution

NAT pool CIDRs are stored on `PeeringEntryExpose.As[]`, not on VPCInfo.
VPCInfo is only needed to obtain the source subnet's network address for the
static NAT offset calculation.

```
PeeringEntryExpose
  ├── IPs[]  → real subnets being exposed from this VPC
  ├── As[]   → NAT pool CIDRs (what the OTHER side must target)
  └── NAT    → mode: nil | masquerade | static | portForward
```

The provider calls a private helper for each expose entry:

```go
// resolveNATTranslation derives the TranslatedAddress for a single server
// reachable via this expose entry.
//
//   serverIP     – real IP of the server on this VPC side
//   subnetCIDR   – VPCInfo subnet CIDR that contains serverIP
//   expose       – the PeeringEntryExpose for this VPC side
//
// Returns nil when expose.NAT is nil (no translation needed).
func resolveNATTranslation(
    expose gwapi.PeeringEntryExpose,
    serverIP netip.Addr,
    subnetCIDR netip.Prefix,
) (*TranslatedAddress, error)
```

Per NAT mode:

**Static (`expose.NAT.Static != nil`):**
- Pool start = `netip.MustParsePrefix(expose.As[0].CIDR).Masked().Addr()`
- Subnet start = `subnetCIDR.Masked().Addr()`
- Per-server NAT IP = `calculateStaticNATIP(serverIP, subnetStart, poolStart)`
  (algorithm: `pool_start + (server_ip − subnet_start)`; see [`rt_nat_tests.go:36`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/rt_nat_tests.go#L36))
- `TranslatedAddress{DestinationIP: natIP}`
- The other side pings the NAT IP, not the real IP.

**Masquerade (`expose.NAT.Masquerade != nil`):**
- No per-server destination IP mapping; the remote side pings real IPs.
- The source IP seen at the destination is dynamic (picked by the dataplane).
  It falls somewhere within the pool, but is not offset-deterministic.
- `TranslatedAddress{SourceIP: poolStart}` where `poolStart` is the network
  address of `expose.As[0].CIDR`.
- SNAT verification compares the captured source IP against the pool **prefix**
  (`prefix.Contains(capturedSrcIP)`), not by exact address equality.

**Port Forward (`expose.NAT.PortForward != nil`):**
- Pool IP = first usable address in `expose.As[0].CIDR` (or the network
  address itself if the pool is a /32).
- For each `PeeringNATPortForwardEntry{Protocol, Port, As}`:
  - External port = `entry.Port`; internal port = `entry.As`
- `TranslatedAddress{DestinationIP: poolIP, PortForwards: {extPort: intPort, ...}}`
- Direction is reverse-only: only the remote side initiates on the forwarded
  ports; other ports from the remote side are DENY.

#### NAT Mode → Expectation Mapping

| NAT field set | Direction | Destination to ping | Source seen at dest | Notes |
|---|---|---|---|---|
| `NAT == nil` | Bidirectional | Real IP | Real IP | Simple gateway peering |
| `NAT.Static` | Bidirectional | NAT IP (offset-mapped) | NAT pool IP | `calculateStaticNATIP` for each server |
| `NAT.Masquerade` | Forward only | Real IP of remote | Some IP in `As` pool | Exact SNAT IP unknown; verify with prefix check |
| `NAT.PortForward` | Reverse only (per port) | `As` pool IP at ext port | NAT pool IP | Other remote-initiated ports → DENY |
| Masq + PortFwd | Asymmetric | Fwd: real; Rev: pool IP | Fwd: pool; Rev: real | Two expectations per pair |
| Static on both sides | Bidirectional | NAT IP on each side | NAT pool IP each side | Each side has its own `calculateStaticNATIP` |

#### Masquerade Direction Logic

For a GatewayPeering between VPC-A and VPC-B where VPC-A has masquerade NAT:

```
VPC-A → VPC-B:  ALLOW (forward), SNAT — source appears as IP in VPC-A's As pool
VPC-B → VPC-A:  DENY (VPC-B cannot initiate to VPC-A through masquerade)
```

Exception: if VPC-A also has port forwards, VPC-B can initiate to those
specific ports:

```
VPC-B → VPC-A:port-fwd-port:  ALLOW (reverse), DNAT to VPC-A backend
VPC-B → VPC-A:other-port:     DENY
```

#### expose.As Support

`expose.As` is the mechanism through which all NAT pool CIDRs are expressed.
It is present on any expose entry that has `expose.NAT` set — validation
enforces that both fields are set or neither is. When `expose.As` is set:

- The other side targets addresses in the `As` CIDR, not the server's real IP.
- For static NAT, `calculateStaticNATIP` maps each real server IP to a
  deterministic address within the `As` prefix.
- For masquerade and port forward, the `As` CIDR is the pool from which the
  gateway assigns the translated address (exact IP is not pre-computable for
  masquerade).

The provider no longer returns an error on `expose.As` (fixing [`testing.go:2722-2723`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2722-L2723)).
Instead it uses the `As` CIDR as the NAT pool and sets `TranslatedAddress`
accordingly.

### Provider: ExternalPeeringProvider

Derives expectations from ExternalPeering CRDs. Handles both switch-based
(fabric) external peerings and gateway external peerings.

For gateway external peerings, delegates NAT logic to the same NAT mode
mapping as GatewayPeeringProvider (since gateway externals use the same
GatewayPeering CRD with `ext~` prefix in VPCInfo names).

### Provider: FirewallProvider (Future)

Placeholder interface for when the 5-tuple firewall CRD is defined.

```go
type FirewallProvider struct{}

func (p *FirewallProvider) BuildExpectations(ctx context.Context, kube kclient.Reader,
    endpoints []Endpoint, current *ConnectivityMatrix,
) ([]ConnectivityExpectation, error) {
    // When firewall CRD exists:
    // 1. List FirewallPolicy CRDs associated with gateway peerings
    // 2. For each rule (5-tuple match → action):
    //    - ALLOW rule: add proto/port-specific ALLOW expectation
    //    - DENY rule: add proto/port-specific DENY expectation
    // 3. Default policy: if peering has firewall with default-deny,
    //    convert existing gateway ALLOW expectations to DENY
    //    unless a specific ALLOW rule exists
    //
    // The provider runs AFTER GatewayPeeringProvider, so it can
    // override/refine gateway expectations with port-level granularity.
}
```

The provider interface is stable — when the firewall CRD is designed, only
a new provider implementation is needed. No changes to the matrix, builder,
or executor.

## Execution Engine

The execution engine takes a `ConnectivityMatrix` and runs the actual
network checks. It replaces the current inline ping/iperf/curl logic.

```go
type MatrixExecutor struct {
    opts    TestConnectivityOpts
    sshs    map[string]*sshutil.Config
    matrix  *ConnectivityMatrix
}

func (e *MatrixExecutor) Execute(ctx context.Context) error {
    // 1. Build work items from matrix
    // 2. Execute in parallel (respecting semaphores)
    // 3. Collect results, report pass/fail per expectation
}
```

### Check Types

Each expectation maps to one or more concrete checks:

| Expectation | Check | Tool | How |
|-------------|-------|------|-----|
| ALLOW, no NAT, ICMP | Positive ping | `ping` | ping dst IP, expect success |
| ALLOW, no NAT, TCP | Positive TCP connect | `socat` | `socat - TCP:dst:port,connect-timeout=3` expect success |
| ALLOW, SNAT masquerade | Positive ping + source verify | `ping` + `tcpdump` | Ping dst, capture at dst to verify source IP is NAT pool |
| ALLOW, DNAT static | Positive ping to NAT IP | `ping` | Ping NAT pool IP, expect it reaches real server |
| ALLOW, port forward | Positive TCP to fwd port | `socat` | Connect to NAT IP:external-port, expect response from internal-port |
| DENY, ICMP | Negative ping | `ping` | ping dst IP, expect 100% loss within timeout |
| DENY, TCP/port | Negative TCP connect | `socat` | `socat` connect to dst:port, expect timeout/refused |
| ALLOW, throughput | iPerf3 | `iperf3` | Same as current, with gateway speed cap |

### Using socat for Port-Level Checks

`socat` is already in the toolbox image and supports both client and server
modes. This enables TCP/UDP port-specific testing without adding new packages.

**Positive TCP check (server side):**
```bash
# Start a listener on the destination server (port 8080, echo back, one-shot)
toolbox -q socat TCP-LISTEN:8080,reuseaddr,fork EXEC:'/bin/echo HEDGEHOG_OK' &
```

**Positive TCP check (client side):**
```bash
# Connect from source server, expect response
toolbox -q timeout 5 socat - TCP:10.0.1.100:8080,connect-timeout=3
# Success: stdout contains "HEDGEHOG_OK"
```

**Negative TCP check (client side):**
```bash
# Connect from source, expect failure (timeout or connection refused)
toolbox -q timeout 3 socat - TCP:10.0.1.100:8080,connect-timeout=2
# Success: exit code != 0
```

**UDP check:**
```bash
# Server: socat UDP-LISTEN:5000,reuseaddr EXEC:'/bin/echo HEDGEHOG_OK'
# Client: echo test | socat - UDP:10.0.1.100:5000,connect-timeout=3
```

### SNAT Verification

Verifying that the destination sees the NATted source IP (not the real IP)
requires packet capture at the destination:

```bash
# On destination server, start capture:
toolbox -q timeout 10 tcpdump -c 5 -nn -i any icmp -Q in 2>/dev/null | \
    grep -oP 'IP \K[0-9.]+'
# Returns: list of source IPs seen

# On source server, send pings:
toolbox -q ping -c 3 -W 2 <nat-pool-ip>
```

The executor compares captured source IPs against the expected NAT pool
range. This is a targeted check for NAT correctness, not a general-purpose
capture.

### Execution Flow

```
MatrixExecutor.Execute()
│
├── Phase 1: Setup (serial)
│   ├── Establish SSH connections (existing logic)
│   ├── Discover endpoint IPs per (server, subnet):
│   │   ├── Regular subnets: ip addr show → match IP to VPC subnet CIDR
│   │   ├── HostBGP subnets: hhnet getvips → /32 VIP on loopback
│   │   └── Deduplicate (server, subnet) pairs from multiple VPCAttachments
│   └── Start socat listeners on endpoints that need port checks
│       (only for proto/port-specific expectations)
│
├── Phase 2: ICMP checks (parallel, bounded by pings semaphore)
│   ├── For each (src, dst) pair with ICMP-level expectation:
│   │   ├── ALLOW: ping dst (or NAT IP), expect success
│   │   └── DENY: ping dst, expect failure (100% loss)
│   └── Collect results
│
├── Phase 3: TCP/UDP checks (parallel, bounded by new semaphore)
│   ├── For each proto/port-specific expectation:
│   │   ├── ALLOW: socat connect to dst:port, expect success
│   │   └── DENY: socat connect to dst:port, expect timeout/refused
│   └── Collect results
│
├── Phase 4: Throughput checks (parallel, bounded by iperfs semaphore)
│   ├── For each ALLOW pair where iPerf is enabled:
│   │   └── Run iPerf3 (existing logic, with gateway speed cap)
│   └── Collect results
│
├── Phase 5: NAT verification (parallel, bounded)
│   ├── For each pair with SNAT expectation:
│   │   ├── Start tcpdump on destination
│   │   ├── Send traffic from source
│   │   └── Verify captured source IP matches NAT pool
│   └── Collect results
│
└── Phase 6: Cleanup
    └── Kill socat listeners
```

### Negative Check Timeout

Negative checks (verify traffic is blocked) are inherently slower than
positive checks because they must wait for a timeout. To keep total
test duration manageable:

- ICMP negative: `ping -c 2 -W 1` — 2 pings, 1s timeout each = ~3s worst case
- TCP negative: `socat ... connect-timeout=2` + `timeout 3` = ~3s worst case
- Run all negative checks in parallel to amortize the wait
- Log negative check count and estimated time at start for visibility

## Integration Points

### Shared Reachability Logic — Current State

The reachability logic is layered across two repos:

**[`fabric/pkg/util/apiutil/connectivity.go`](https://github.com/githedgehog/fabric/blob/v0.117.1/pkg/util/apiutil/connectivity.go)** — base library in the
fabric repo. Contains:
- `GetAttachedSubnets()` — server → VPC subnet attachments
- `IsServerReachable()`, `IsSubnetReachable()`, `IsSubnetReachableWithinVPC()`,
  `IsSubnetReachableBetweenVPCs()` — intra-VPC and VPCPeering checks
- `IsExternalSubnetReachable()`, `IsExternalIPReachable()`,
  `IsStaticExternalIPReachable()` — ExternalPeering checks
- **No gateway peering, NAT, or HostBGP awareness**

**[`fabricator/pkg/hhfab/testing.go`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go)** — layers on top of `apiutil` (not a
copy). Its own `IsSubnetReachable()` calls `apiutil.IsSubnetReachableWithinVPC()`
for intra-VPC and then adds [`IsSubnetReachableWithGatewayPeering()`](https://github.com/githedgehog/fabricator/blob/033b3417fe1944f2522fbec4d7e1045e02be9f08/pkg/hhfab/testing.go#L2643) — the
"temporary function" at line 2643. This is the only place gateway peering
reachability exists.

**[`fabric/pkg/hhfctl/inspect/access.go`](https://github.com/githedgehog/fabric/blob/v0.117.1/pkg/hhfctl/inspect/access.go)** — the `kubectl fabric inspect access`
command. Calls `apiutil.IsSubnetReachable()` directly, which means it has
**no gateway peering awareness**: it reports "unreachable" when a gateway
peering exists. It also cannot express NAT translation or directional
asymmetry.

The connectivity matrix's provider chain replaces the fabricator extension
layer and becomes the authoritative reachability logic. The question of
where this code should live — in the fabric repo (so `inspect access` can
also consume the provider chain) or only in fabricator — is an open design
decision. At minimum, `inspect access` should be updated to use the same
providers; otherwise it will remain incomplete as new connectivity features
are added.

### On-Ready Command

`test-connectivity` remains an on-ready command. The internal implementation
changes from direct reachability queries to matrix-based execution:

```go
func (c *Client) TestConnectivity(ctx context.Context, opts TestConnectivityOpts) error {
    // Phase 1: Discover endpoints — one Endpoint per (server, subnet) attachment.
    // Iterates VPCAttachments, SSHes to each server, resolves IPs per subnet.
    // HostBGP subnets use hhnet getvips; regular subnets use ip addr show
    // matched against VPC subnet CIDRs. Deduplicates multihomed attachments.
    endpoints, sshs := discoverEndpoints(ctx, kube, sshConfigs)

    // Phase 2: Build matrix from API objects
    builder := NewMatrixBuilder(kube, c.Fab.Spec.Config.Gateway.Enable)
    matrix, err := builder.Build(ctx, endpoints)

    // Phase 3: Execute matrix
    executor := NewMatrixExecutor(opts, sshs, matrix)
    return executor.Execute(ctx)
}
```

### Release Test Consumption

Release tests currently call `IsServerReachable()` and `checkPing()`/
`checkIPerf()` directly. These functions remain available but are now
implemented on top of the matrix:

```go
// IsServerReachable is preserved for backward compatibility.
// Release tests that build their own topologies can still use it.
func IsServerReachable(ctx context.Context, kube kclient.Reader,
    sourceServer, destServer string, checkGateway bool,
) (Reachability, error) {
    // Build a minimal matrix for just this pair
    // Return the legacy Reachability struct from the expectation
}
```

Release tests that need precise NAT testing (like `rt_nat_tests.go`) can
also use the matrix directly:

```go
// In a release test:
matrix := NewMatrixBuilder(kube, true).Build(ctx, endpoints)
src := EndpointKey{Server: srcServer, Subnet: "vpc-1/default"}
dst := EndpointKey{Server: dstServer, Subnet: "vpc-2/default"}
expectation := matrix.GetForProto(src, dst, ProtoPort{"tcp", 8080})
// Assert expectation matches test intent
```

### Reporting

The executor produces a structured result that can feed into both
on-ready logging and JUnit XML:

```go
type MatrixResult struct {
    Total     int
    Passed    int
    Failed    int
    Skipped   int
    Results   []CheckResult
}

type CheckResult struct {
    Expectation ConnectivityExpectation
    Outcome     CheckOutcome // Pass, Fail, Skip, Error
    Duration    time.Duration
    Detail      string // e.g., "ping: 5/5 received" or "socat: connection refused (expected)"
}
```

On failure, the result includes the full expectation (source, destination,
expected verdict, NAT config, reason/peering) for immediate diagnostics
without needing to cross-reference API objects.

## Migration Path

Phase order is **1 → 2 → 4 → 3**. Phase 1 lands the structural change; Phase
2 closes the genuine Phase-1 coverage gaps (no new external dependencies);
Phase 4 is reporting/DX polish that pays back during Phase 3 triage; Phase 3
is last because it is blocked on a firewall CRD that does not exist on
master yet. The original document had Phases 3 and 4 swapped; this reflects
what was learned while landing Phase 1.

### Phase 1: Foundation + Gateway Peering Fix — LANDED

**Goal:** Fix the gateway peering bypass. Make `test-connectivity` correctly
handle NAT-aware peerings with directional asymmetry.

1. Implement `ConnectivityExpectation`, `EndpointKey`, and `ConnectivityMatrix`
   types, keyed on `(server, subnet)` not just server name
2. Implement `discoverEndpoints()`:
   - Iterate VPCAttachments to build one `Endpoint` per `(server, subnet)`
   - Regular subnets: `ip addr show` → match each IP to its VPC subnet CIDR
   - HostBGP subnets: `hhnet getvips` → /32 VIP on loopback
   - Deduplicate multihomed `(server, subnet)` pairs from multiple
     VPCAttachments to the same subnet via different leaves
   - Capture the Linux interface name per endpoint for later source binding
3. Implement `MatrixBuilder` with providers:
   - `IntraVPCProvider` (port of existing intra-VPC logic, with HostBGP subnet awareness)
   - `SwitchPeeringProvider` (port of `IsSubnetReachableWithSwitchPeering`)
   - `GatewayPeeringProvider` with NAT awareness and direction
   - `ExternalPeeringProvider` (no-op stub; populated in Phase 2)
4. Implement `MatrixExecutor` with:
   - Positive/negative ICMP checks
   - NAT-target IP support (ping NATted IP instead of real IP)
   - Direction-aware testing (skip reverse for masquerade)
   - Per-endpoint IP targeting (use the subnet-specific IP, not a single
     server-level IP)
   - Source-interface binding via `ping -I <iface>` so trunked servers test
     the right egress path
   - Skip same-server pairs (different-subnet pings on the same box always
     succeed, which would false-positive implicit-DENY)
5. Wire into `TestConnectivity()` replacing the current inline logic
6. Preserve `IsServerReachable()` API for release test backward compat;
   route it through the new matrix so callers inherit NAT awareness
7. Add a `--vpcattachments-per-server` flag to `setup-vpcs` so VLAB can
   generate a multi-VPC trunking topology to exercise per-endpoint paths
8. Per-sub-interface source-based policy routing in `hhnet.sh` so replies
   on a trunked server egress the sub-interface that owns the source IP
   (main-table FIB-multipath across two DHCP defaults would otherwise
   route replies out the wrong VPC's VLAN; the leaf then drops them in
   the wrong VRF)

**What this unblocks:** Gateway peering smoke tests work without bypass.
NAT peerings (static offset, masquerade direction, `expose.As`) are
validated at the smoke-test level, not just release tests. Multi-VPC
trunking works end-to-end — each subnet attachment has its own endpoint
with its own IP and own source-bound ping. HostBGP servers are first-class
participants (githedgehog/fabricator#1648 carries the complementary
release-suite work).

### Phase 2: Externals in the matrix, per-endpoint iperf, SNAT verification

**Goal:** Close the coverage gaps Phase 1 structurally cannot — without
waiting for the firewall CRD. No new tools required.

1. Populate `ExternalPeeringProvider` so externals are matrix endpoints
   with per-prefix reachability expectations. Retire the ad-hoc curl
   loop in `TestConnectivity` (or keep it as a thin wrapper).
2. Iperf3 as a matrix-executor check type: iterate endpoint pairs, use
   `--bind <src.IP>` and target `<dst.IP>`. Phase 1's policy routing
   makes source-IP binding sufficient for deterministic egress on
   trunked servers (iperf3 has no `-I <iface>` flag; the policy rules
   route every packet with a given source IP out the owning interface).
3. SNAT source verification: start a short tcpdump at the destination,
   send pings/TCP, compare the captured source IP against the expected
   NAT pool prefix (`prefix.Contains(capturedSrc)` for masquerade;
   exact match for static).
4. Retire `rt_nat_tests.go`'s reachability-side bypass (the ping path)
   in favor of `IsServerReachable`. Keep the iperf3-on-port
   port-forward check — that is how port-forward DNAT is validated
   today (`iperf3 -c <pool-IP> -p <ext-port>` → backend on 5201),
   and it already works; Phase 2 just makes it matrix-driven.

**What this unblocks:** SNAT correctness (not just reachability),
per-external-prefix assertions, trunking iperf coverage, and removal of
the parallel NAT-reachability code in rt_nat_tests.

### Phase 4: Reporting + CI integration

**Goal:** Make failure triage fast, make the matrix a first-class CI
artifact, integrate with the tiered test strategy. Unblocked today; the
investment compounds through Phase 3.

1. Structured `MatrixResult` → JUnit XML.
2. Matrix diff on failure: expected-vs-actual rendered as an NxN grid
   with colored cells; surface only the disagreeing pairs.
3. Matrix visualization as a build artifact on each CI run (HTML or
   ASCII — doesn't matter, just needs to be readable in a PR comment).
4. Integrate with the tiered test strategy (PR #1290) so smoke vs
   release lanes consume the same matrix output.

### Phase 3: Firewall support

**Goal:** Support 5-tuple firewall rules when the CRD is defined. Blocked
on the CRD until it lands on master.

1. Implement `FirewallProvider` once the CRD exists.
2. Add per-rule proto/port expectations to the matrix.
3. Add default-policy handling (default-allow vs default-deny — flipping
   existing gateway ALLOWs to DENYs when default-deny is set, unless a
   specific ALLOW rule covers the pair).
4. Extend the executor with per-port checks for ALLOW and DENY rules.
   Implementation choice: iperf3 on specific ports (already in the
   toolbox, works for port-forward today) or socat (cleaner pass/fail
   semantics, supports UDP). This is a DX call, not a phase boundary.
5. Add stateful return-traffic verification — idle timeouts, table
   exhaustion, concurrent connection reuse.

**What this unblocks:** Firewall feature can use test-connectivity for
smoke and release testing without needing to bypass it.

### Note on socat

The original plan introduced socat in Phase 2 for TCP/UDP testing. In
practice, today's non-firewall fabric treats a peering as all-or-nothing
at L4: if ICMP works, TCP and UDP work; if ICMP fails, so do the rest.
Port-forward DNAT is the one case that needs L4 validation today, and
`rt_nat_tests.go` already covers it with iperf3 on the forwarded port.
socat only becomes necessary when per-port ALLOW/DENY differentiation
matters, which is firewall territory — so it moves into Phase 3 as an
implementation option alongside iperf3-on-port.
