# CI Normal Run — Test Coverage

This describes what is tested when `releasetest=false` (the default for non-release CI runs).

## What runs

With `releasetest=false`, the workflow passes `--release-test-on-ready-only`, which runs **only** the `OnReady Suite` (a single compact test — `New VLAB OnReady Test`) rather than the full release test matrix.

## VLAB topology generated

The topology is parameterized by `vpcmode` and `mesh`:

| Input | MCLAG leaves | ESLAG groups | Orphan leaves | Unbundled servers | Externals |
|---|---|---|---|---|---|
| `vpcmode=l2vni,mesh=false` (default) | 2 | 2 | 1 | 1 | 1 BGP + 1 static |
| `vpcmode=l3vni,mesh=false` | 0 | 0 | 3 | 2 | 1 BGP + 1 static |
| `vpcmode=l2vni,mesh=true` | 0 | 2 | 1 | 2 | 1 BGP + 1 static |
| `vpcmode=l3vni,mesh=true` | 0 | 0 | 3 | 2 | 1 BGP + 1 static |

All topologies include one external BGP attachment and one non-proxy static external attachment (via `--externals-bgp=1 --externals-static=1 --external-orphan-connections=1`).

Gateway (`--gateways=2 / 0`) is controlled independently by the `gateway` input.

## What the OnReady test covers

### Preconditions (test fails if not met)
- At least 7 eligible servers (ESLAG servers excluded in non-l2vni mode)
- At least one BGP external with an attachment
- At least one static external with a non-proxy attachment

### VPC and attachment setup

| VPC | Subnets | Servers | Notes |
|---|---|---|---|
| `ort-a` | 2 regular DHCP subnets | 1 per subnet | Any available servers |
| `ort-b` | 1 hostBGP subnet | 1 unbundled, non-MCLAG | Runs `host-bgp` docker container; waits for VIP acquisition |
| `ort-c` | 1 regular DHCP subnet | 2 non-MCLAG servers on **different switches** | Tests multi-switch attachment to the same subnet |
| `ort-d1..N` | 1 regular DHCP subnet each | 1 server each | All remaining servers |

All VPCs are created with the active `vpcmode`. Server networking is configured via `hhnet`; L3VNI/L3Flat mode gets an extra 10 s propagation wait after server setup.

### Peerings applied

| Type | Participants |
|---|---|
| ExternalPeering | `ort-d1` → BGP external (subnet-01, 0.0.0.0/0) |
| ExternalPeering | `ort-d2` → static non-proxy external (subnet-01, 0.0.0.0/0) |
| VPCPeering | `ort-b` ↔ `ort-a` (hostBGP VPC ↔ regular dual-subnet VPC) |
| GatewayPeering *(gateway only)* | Consecutive pairs: `ort-d2` ↔ `ort-d3`, `ort-d3` ↔ `ort-d4`, … |

`ort-d1` (BGP external peer) is intentionally excluded from gateway peerings to avoid an LPM route asymmetry with the static external VRF.

### Connectivity test

After all peerings are applied, `DoVLABTestConnectivity` runs against the configured topology.

### Cleanup

All created VPCs, peerings, and server network configurations are torn down regardless of test outcome.

## Effect of key CI inputs on test behaviour

| Input | Effect |
|---|---|
| `vpcmode=l3vni` | ESLAG servers skipped; no MCLAG topology; extra route propagation wait |
| `gateway=true` | GatewayPeerings created between consecutive `ort-d2..N` VPCs; `NoGateway` skip lifted |
| `gateway=false` | Gateway peerings skipped; test still passes as long as other preconditions are met |
| `mesh=true` | No MCLAG topology; mesh-only switch connections (does not affect the on-ready test logic directly) |
