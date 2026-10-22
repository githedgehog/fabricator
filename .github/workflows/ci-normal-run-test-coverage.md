# CI Normal Run — Test Coverage

This describes what is tested when `releasetest=false` (the default for non-release CI runs).

## What runs

With `releasetest=false`, the workflow passes `--release-test-on-ready-only`, which runs **only** the `OnReady Suite` (a single compact test — `New VLAB OnReady Test`) rather than the full release test matrix.

## VLAB topology generated

The topology is parameterized by `vpcmode` and `mesh`:

| Input | ESLAG groups | Orphan leaves | Externals |
|---|---|---|---|
| `vpcmode=l2vni,mesh=false` (default) | 2 | 1 | 1 BGP + 1 static |
| `vpcmode=l3vni,mesh=false` | 0 | 3 | 1 BGP + 1 static |
| `vpcmode=l2vni,mesh=true` | 2 | 1 | 1 BGP + 1 static |
| `vpcmode=l3vni,mesh=true` | 0 | 3 | 1 BGP + 1 static |

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
| `ort-b` | 1 hostBGP subnet | 1 unbundled | Runs `host-bgp` docker container; waits for VIP acquisition |
| `ort-c` | 1 regular DHCP subnet | 2 servers on **different switches** | Tests multi-switch attachment to the same subnet |
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
| `vpcmode=l3vni` | ESLAG servers skipped; extra route propagation wait |
| `gateway=true` | GatewayPeerings created between consecutive `ort-d2..N` VPCs; `NoGateway` skip lifted |
| `gateway=false` | Gateway peerings skipped; test still passes as long as other preconditions are met |
| `mesh=true` | mesh-only switch connections (does not affect the on-ready test logic directly) |

## The add-fabric job

The `v-iso-l2vni-mf` job (`addfabric=true`) does not run the OnReady Suite or release tests, whatever `releasetest` is. It generates two Fabrics with `--add-fabric --interconnect-fabrics`: the default Fabric with 2 orphan leaves and the extra Fabric with 2 mesh leaves, one server per leaf, 1 BGP + 1 static external, no ESLAG and no gateways. The extra Fabric is left out of the install: its VMs boot and its switches wait for their Switch objects.

The ready steps then run in this order:

- `add-fabric` creates the objects of the extra Fabric and waits for all switches, including the two new leaves, to be ready
- `inspect` and `inspect-pods`
- `setup-vpcs` creates the VPCs and attachments of all four servers, and checks that each server is configured and reaches its gateway through its leaf
- `setup-peerings` creates a VPC peering inside each Fabric and an External peering from the first VPC of each Fabric to its interconnect External, built from the cluster

Traffic between the two Fabrics over the interconnect is not asserted yet, as the on-ready peerings and the connectivity model are not Fabric-aware.
