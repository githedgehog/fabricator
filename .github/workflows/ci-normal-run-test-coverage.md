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

The `v-gw-iso-l2vni-mf` job (`addfabric=true`, `gateway=true`) does not run the OnReady Suite or release tests, whatever `releasetest` is. It generates two Fabrics with `--add-fabric --interconnect-fabrics`: the default Fabric with 2 orphan leaves and the extra Fabric with 2 mesh leaves, one server per leaf, 1 BGP + 1 static external, no ESLAG, and the 2 gateways in the default Fabric only. The extra Fabric is left out of the install: its VMs boot and its switches wait for their Switch objects.

The ready steps then run in this order:

- `add-fabric` creates the objects of the extra Fabric and waits for all switches, including the two new leaves, to be ready
- `inspect` and `inspect-pods`
- `setup-vpcs` creates the VPCs and attachments of all four servers, and checks that each server is configured and reaches its gateway through its leaf
- `setup-peerings` creates a VPC peering inside each Fabric and an External peering from the first VPC of each Fabric to its interconnect External, built from the cluster. The extra Fabric has no gateway, so the default Fabric also gets a gateway peering between its interconnect External and the BGP External (masquerading into the External's NAT pool), and the VPC of the extra Fabric peers its interconnect External with `0.0.0.0/0`
- `test-connectivity` checks every server pair and External against the connectivity matrix. A pair in different Fabrics must reach each other when both VPCs have an External peering to the interconnect External of their own Fabric, one covering the other's subnet, and nothing else carries traffic between Fabrics. A server in the extra Fabric reaches the External only when that gateway peering and the VPC's `0.0.0.0/0` peering both exist, and the matrix expects it denied without them. Interconnect Externals are not probe targets

## The multi-domain job

The `v-iso-l2vni-md` job (`multidomain=true`) does not run the OnReady Suite or release tests either. It generates one Fabric with a second domain: `--extra-domain --shared-leafs-count=1` with 3 orphan leaves, one server per leaf, 1 BGP + 1 static external, no ESLAG and no gateways. The orphan leaves and the externals are in the second domain, `domain-b`, and the third leaf is shared by `default` and `domain-b`. The default domain has two spines and no leaf of its own, which is why no VPC of the default domain alone is created.

The ready steps run in this order: `inspect`, `inspect-pods`, `setup-vpcs`, `setup-peerings`, `test-connectivity`.

- `setup-vpcs` creates a VPC in `domain-b` for the two orphan-leaf servers and a VPC in both domains for the server on the shared leaf
- `setup-peerings` peers the two VPCs, which share `domain-b`, and peers the first VPC of `domain-b` with the first External of that domain
- `test-connectivity` checks every pair against the peerings: the servers of the `domain-b` VPC and the shared-leaf server reach each other, and only the peered subnet reaches the External
