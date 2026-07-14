# Gateway Peering ACL release tests

Documents the ACL cases in `rt_acl_tests.go`: the probe infrastructure they run
on, what each case asserts, and the coverage they add. Intended for reviewers
assessing whether the coverage is correct and sufficient.

## Probe infrastructure

ACL enforcement is protocol/port-specific, so the tests drive the
`ConnectivityMatrix` (`matrix.go`) keyed by `ProtoPort{Protocol, Port}` rather
than the legacy default check.

- **Expectations** are stamped on the matrix via `setVPCToVPCProtoVerdict(m,
  srcVPC, dstVPC, ProtoPort, verdict)` (and the `setACLDirVerdicts` helper, which
  sets the common icmp + tcp/5201 + udp/5201 triple for one direction).
- A pair carrying any non-zero `ProtoPort` entry is owned entirely by
  `runMatrixProtoPortPhase` and skipped by the legacy server-server phase
  (`HasProtoPortEntries` gate), so every expected verdict — including ICMP — is an
  explicit entry.
- Each case is a `natTestSpec` run through `runNATTest`: `BuildSpec` attaches the
  `PeeringACL` to the gateway peering (via `GwPeeringOptions.ACL`), then
  `DoSetupPeerings → WaitReady → matrix.Repopulate → Overlay →
  DoVLABTestConnectivityWithMatrix`.

### Per-protocol probes

| Protocol | Helper | Wire probe | Verdict signal |
|---|---|---|---|
| `icmp` | `checkPing` | `ping -c N` | all replies received = allow; zero = deny |
| `tcp`  | `checkTCPPort` | `nc -zw2 <ip> <port>; echo NCRC=$?` | rc 0 = allow, rc 1 = deny; anything else / no marker / SSH error = surfaced as infra failure |
| `udp`  | `checkUDPPort` | `iperf3 -u -J -c <ip> -p <port>` | datagrams delivered (loss < 90%) = allow; control-channel error / 0 packets / loss ≥ 99% = deny; unparseable output = surfaced |

Port 5201 is served by the always-on `iperf3 -s` daemon (TCP+UDP). Any other port
(e.g. the 6xxx range) gets an on-demand `iperf3 -s -p <port>` listener started by
`startMatrixProtoPortListeners` and torn down at the end of the run.

### Two facts that shape every case

1. **Return path** — a working probe (ping reply, TCP handshake, iperf3 control
   channel) needs *both* directions permitted. With `packet` (stateless) scope
   the reply must be matched by its own rule, and because the reply has ports
   swapped, a destination-port allow needs a matching **source-port** allow for
   the reverse direction. A single one-way packet rule yields **no** connectivity.
2. **`flow` scope requires stateful NAT** — the dataplane only keeps
   flow/conntrack state where masquerade (or port-forward) NAT is present, so a
   `flow`-scoped rule is only valid on such a peering; there conntrack permits the
   return automatically. NAT-free cases therefore use explicit `packet` scope.

## Test cases

All cases peer the first two VPCs (`vpc1`, `vpc2`), default action `deny` unless
noted, and run with `SkipFlags{NoGateway, NoServers}`.

| Test | ACL under test | Expected (fwd = vpc1→vpc2, rev = vpc2→vpc1) |
|---|---|---|
| **Default Deny** | `deny` default + one allow rule on an unprobed port | all deny both ways (probed traffic hits the default) |
| **Deny-Unless-Exposed UDP Carve-Out** | `deny-unless-exposed` default + `deny udp` both dirs | tcp+icmp allow both ways (exposed); udp deny both ways |
| **Explicit Allow** | `allow` rules both dirs (any proto) | all allow both ways |
| **Protocol Scoping** | `allow tcp` both dirs | tcp allow; udp+icmp deny (fall to default) |
| **Packet One-Way** | single `allow` rule, one direction only, `packet` | all deny both ways — reply is dropped, so nothing completes |
| **Flow Scope Masquerade** | masquerade NAT on vpc1 + `flow allow` vpc1→vpc2 | fwd allow; rev deny (masquerade blocks inbound + default deny) |
| **Subnet/CIDR Scoping** | allow both dirs; fwd matched by `VPCSubnet`, rev by `CIDR` | all allow both ways |
| **Port Range Scoping** | allow tcp; fwd dst-port range `6000-6500`, rev src-port range | fwd tcp/6201 allow; tcp/5201 + udp + icmp + all rev = deny |
| **Precedence Allow-Then-Deny** | `[allow tcp/5201, deny-all]` fwd + src-port return rule | fwd tcp/5201 allow; everything else deny |
| **Precedence Deny-Then-Allow** | same rules, `deny-all` first | all deny (first match wins) |

## Coverage

| Dimension | Covered by |
|---|---|
| Default action `deny` / `deny-unless-exposed` | Default Deny / Deny-Unless-Exposed |
| Rule action `allow` / `deny` | all allow cases / carve-out + precedence |
| Protocol `tcp` / `udp` / any | Protocol Scoping, Port Range / UDP Carve-Out / Explicit Allow, Subnet |
| Selector `VPCSubnet` / `CIDR` | Subnet/CIDR Scoping (both, one per direction) |
| Ports single / range, dst / src side | Precedence (single dst+src) / Port Range (range dst+src) |
| Scope `packet` / `flow` | all packet cases / Flow Scope Masquerade |
| Rule precedence (first-match) | Precedence Allow-Then-Deny + Deny-Then-Allow |
| Stateless return-path requirement | Packet One-Way (negative) |

### Known limitations / assumptions

- **UDP is verifiable only as "denied while TCP allowed."** The `iperf3 -u` probe
  opens a TCP control channel on the same port, so "deny TCP + allow UDP on one
  port" cannot be distinguished from a UDP block — no case relies on it.
- **ICMP falls to the default action** (it matches neither `tcp` nor `udp`
  rules). Numeric-protocol matching (e.g. proto `1` for ICMP) is **not** covered.
