# Test Coverage Matrix

This document maps what is tested across the Hedgehog fabric, including
release test suites, CI matrix configurations, switch profile capabilities,
and per-environment coverage.

## Release Test Suites

45 test cases across 4 suites, executed via `hhfab vlab release-test`.

> **Note:** Test count reflects PR [#1579](https://github.com/githedgehog/fabricator/pull/1579)
> which adds 12 gateway+external NAT tests covering the full NAT mode × external type matrix.

### Suite 1: No VPCs (3 tests)

Tests that run without any VPC configuration, validating base fabric functionality.

| Test Case | Skip Conditions | What It Validates |
|-----------|----------------|-------------------|
| Breakout ports | VirtualSwitch | Port breakout configuration on physical switches |
| Loki Observability | NoLoki | Log collection pipeline (switch → Alloy → Fabric Proxy → Loki) |
| Prometheus Observability | NoProm | Metrics collection pipeline |

### Suite 2: Single VPC (11 tests)

Setup: 3 subnets per VPC, ~3 servers per subnet. Validates intra-VPC behavior.

| Test Case | Skip Conditions | What It Validates |
|-----------|----------------|-------------------|
| No restrictions | NoServers | Basic L2/L3 connectivity within a VPC |
| Single VPC with restrictions | VirtualSwitch, NoServers | Isolated/restricted subnet enforcement |
| DNS/NTP/MTU/DHCP lease | — | DHCP options propagation (DNS, NTP, MTU, lease) |
| DHCP renewal | — | DHCP lease renewal cycle |
| DHCP static lease | — | Static MAC→IP DHCP binding |
| MCLAG Failover | VirtualSwitch, NoServers | Traffic survives MCLAG member link failure |
| ESLAG Failover | VirtualSwitch, NoServers | Traffic survives ESLAG member link failure |
| Bundled Failover | VirtualSwitch, NoServers | Traffic survives bundled (LAG) link failure |
| Spine Failover | VirtualSwitch, NoFabricLink, NoServers | Traffic survives spine link failure |
| Mesh Failover | VirtualSwitch, NoMeshLink, NoServers | Traffic survives mesh link failure |
| RoCE flag and basic traffic marking | RoCE, NoServers | RoCE QoS configuration and DSCP marking |

### Suite 3: Multi-VPC Multi-Subnet (4 tests)

Setup: 1 server per subnet, 3 subnets per VPC. Validates multi-tenant isolation.

| Test Case | Skip Conditions | What It Validates |
|-----------|----------------|-------------------|
| Multi-Subnets no restrictions | — | Cross-subnet connectivity within same VPC |
| Multi-Subnets isolation | VirtualSwitch, NoServers | Subnet isolation enforcement across VPCs |
| Multi-Subnets with filtering | VirtualSwitch, SubInterfaces, NoServers | Permit-list based subnet filtering |
| StaticExternal | VirtualSwitch, NoServers | Static external peering with multi-subnet VPCs |

### Suite 4: Multi-VPC Single-Subnet (27 tests)

Setup: 1 subnet per VPC, wipe between tests for isolation. Validates inter-VPC and
gateway peering.

| Test Case | Skip Conditions | What It Validates |
|-----------|----------------|-------------------|
| **VPC Peering** | | |
| Starter Test | NoBGPExternals, SubInterfaces, NoServers | Basic VPC peering + external connectivity |
| Only Externals | NoBGPExternals, SubInterfaces, NoServers | External-only peering (no inter-VPC) |
| Full Mesh All Externals | SubInterfaces, NoServers | All VPCs peered with all externals |
| Full Loop All Externals | SubInterfaces, NoServers | Circular VPC peering topology |
| Sergei's Special Test | NoBGPExternals, SubInterfaces, NoServers | Edge-case peering configuration |
| **Gateway VPC-to-VPC Peering** | | |
| Gateway Peering | NoGateway, NoServers | VPC-to-VPC via gateway (no NAT) |
| Gateway Failover | NoGateway, NoServers | Traffic survives gateway node failure |
| Gateway Peering Loop | NoGateway, NoServers | Circular gateway peering topology |
| Mixed VPC and Gateway Peering Loop | NoGateway, NoServers | Combined fabric + gateway peering |
| Mixed Gateway and Fabric External Peering | NoBGPExternals, NoGateway, NoServers | Gateway + fabric external peering coexistence |
| **Static External** | | |
| Static External Peering | NoStaticExternals, SubInterfaces, NoServers | Static external attachment with peering |
| **VPC-to-VPC NAT** | | |
| Gateway Peering Masquerade Source NAT | NoGateway | Stateful source NAT (masquerade) through gateway |
| Gateway Peering Static Source NAT | NoGateway | Stateless 1:1 NAT mapping |
| Gateway Peering Bidirectional Static NAT | NoGateway | Bidirectional static NAT |
| Gateway Peering Overlap NAT | NoGateway | NAT with overlapping address spaces |
| Gateway Peering Port Forward NAT | NoGateway | Inbound port forwarding through gateway |
| Gateway Peering Masquerade and Port Forward NAT | NoGateway | Combined masquerade + port forwarding |
| **Gateway External NAT (BGP)** | | |
| GW Peering BGP External No NAT | NoGateway, NoBGPExternals | Baseline: VPC→BGP external without NAT |
| GW Peering BGP External Static NAT | NoGateway, NoBGPExternals | 1:1 static NAT to BGP external |
| GW Peering BGP External Masquerade NAT | NoGateway, NoBGPExternals | Masquerade NAT to BGP external |
| GW Peering BGP External Port Forward NAT | NoGateway, NoBGPExternals | Port forwarding from BGP external |
| GW Peering BGP External Masq+PortFwd NAT | NoGateway, NoBGPExternals | Combined masquerade + port-fwd to BGP external |
| **Gateway External NAT (Static)** | | |
| GW Peering Static External No NAT | NoGateway, NoStaticExternals | Baseline: VPC→static external without NAT |
| GW Peering Static External Static NAT | NoGateway, NoStaticExternals | 1:1 static NAT to static external |
| GW Peering Static External Masquerade NAT | NoGateway, NoStaticExternals | Masquerade NAT to static external |
| GW Peering Static External Port Forward NAT | NoGateway, NoStaticExternals | Port forwarding from static external |
| GW Peering Static External Masq+PortFwd NAT | NoGateway, NoStaticExternals | Combined masquerade + port-fwd to static external |

The gateway external NAT tests (PR [#1579](https://github.com/githedgehog/fabricator/pull/1579))
cover the full **external type × NAT mode** matrix:

| NAT Mode | BGP External | Static External |
|----------|-------------|-----------------|
| No NAT | yes | yes |
| Static 1:1 | yes | yes |
| Masquerade | yes | yes |
| Port Forward | yes | yes |
| Masquerade + Port Forward | yes | yes |

These tests use per-environment NAT pool CIDRs (via annotations on External objects) so
multiple HLAB environments can test against the shared DS2000 edge device simultaneously
without route conflicts.

### Skip Condition Reference

| Flag | Meaning | When Set |
|------|---------|----------|
| `VirtualSwitch` | Any switch uses SONiC VS profile | Always in VLAB |
| `NoBGPExternals` | No viable BGP external peers | No BGP externals with attachments |
| `NoStaticExternals` | No viable static external peers | No static externals with static attachments |
| `NoGateway` | Gateway not enabled or no gateways | No `--gateway` flag |
| `NoFabricLink` | No spine-leaf links | Mesh-only topology |
| `NoMeshLink` | No leaf-leaf links | Spine-leaf-only topology |
| `RoCE` | No RoCE-capable leaf switches | VLAB or non-RoCE hardware |
| `SubInterfaces` | Some switches lack subinterface support | Hardware limitation (see profile matrix) |
| `NoLoki` | Loki not configured | No observability backend |
| `NoProm` | Prometheus not configured | No observability backend |
| `NoServers` | No servers in fabric | No workload endpoints |
| `ExtendedOnly` | `--extended` flag not passed | Default runs |

External selection (as of PR #1579) uses a 2-pass algorithm: pass 1 prefers hardware
externals, pass 2 accepts virtual externals whose attachments all go through hardware
connections (same data path, only the peer device is emulated).

---

## CI Matrix Coverage

### What Each CI Configuration Exercises

| CI Config | Topology | Switches | Release Tests? | Unique Coverage |
|-----------|----------|----------|---------------|-----------------|
| **Base (manual/l2vni)** | Spine-leaf | Virtual | On schedule/label | Fastest feedback; default L2VNI path |
| **ISO/l3vni** | Spine-leaf | Virtual | On schedule/label | L3VNI (routes-only) VPC mode; ISO installer |
| **GW+ONIE USB/l2vni** | Spine-leaf+GW | Virtual | On schedule/label | Gateway + ONIE provisioning + USB installer |
| **GW+ONIE ISO/l3vni** | Spine-leaf+GW | Virtual | On schedule/label | Gateway + L3VNI + ISO |
| **HLAB GW/l2vni** | Spine-leaf+GW | **Physical** | On schedule/label | Real ASIC behavior, failover, breakout, RoCE |
| **Mesh GW/l2vni** | Mesh+GW | Virtual | On schedule/label | Mesh topology + gateway |
| **Upgrade L2** | Spine-leaf | Virtual | No | Upgrade path 26.01→current, L2VNI |
| **Upgrade L3** | Spine-leaf | Virtual | No | Upgrade path 26.01→current, L3VNI |
| **Upgrade Mesh** | Mesh | Virtual | No | Upgrade path 26.01→current, mesh |

> **Note:** Mesh+GW+L3VNI is commented out due to an ESLAG connection limitation.

### Test × CI Config Matrix (Release Tests)

Which tests actually execute in each CI configuration (accounting for skip conditions).
Upgrade configs are excluded — they run smoke/connectivity only, not release tests.

| Test | Base | L3VNI | GW USB | GW ISO L3 | HLAB | Mesh GW |
|------|------|-------|--------|-----------|------|---------|
| **No VPCs Suite** |
| Breakout ports | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| Loki Observability | run | run | run | run | run | run |
| Prometheus Observability | run | run | run | run | run | run |
| **Single VPC Suite** |
| No restrictions | run | run | run | run | **run** | run |
| VPC with restrictions | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| DNS/NTP/MTU/DHCP | run | run | run | run | run | run |
| DHCP renewal | run | run | run | run | run | run |
| DHCP static lease | run | run | run | run | run | run |
| MCLAG Failover | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| ESLAG Failover | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| Bundled Failover | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| Spine Failover | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| Mesh Failover | skip^NM | skip^NM | skip^NM | skip^NM | skip^NM | skip^VS |
| RoCE marking | skip^VS | skip^VS | skip^VS | skip^VS | run* | skip^VS |
| **Multi-VPC Multi-Subnet** |
| Multi-Sub no restrict | run | run | run | run | run | run |
| Multi-Sub isolation | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| Multi-Sub filtering | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| StaticExternal | skip^VS | skip^VS | skip^VS | skip^VS | **run** | skip^VS |
| **Multi-VPC Single-Subnet** |
| Starter Test | run | run | run | run | **run** | run |
| Only Externals | run | run | run | run | **run** | run |
| Full Mesh Externals | run | run | run | run | run | run |
| Full Loop Externals | run | run | run | run | run | run |
| Sergei's Special | run | run | run | run | **run** | run |
| Gateway Peering | skip^GW | skip^GW | run | run | run | run |
| Gateway Failover | skip^GW | skip^GW | run | run | run | run |
| Gateway Peering Loop | skip^GW | skip^GW | run | run | run | run |
| Mixed VPC+GW Loop | skip^GW | skip^GW | run | run | run | run |
| Mixed GW+Ext Peering | skip^GW | skip^GW | run | run | run | run |
| Static Ext Peering | run | run | run | run | **run** | run |
| VPC-to-VPC Masquerade NAT | skip^GW | skip^GW | run | run | run | run |
| VPC-to-VPC Static NAT | skip^GW | skip^GW | run | run | run | run |
| VPC-to-VPC Bidir Static NAT | skip^GW | skip^GW | run | run | run | run |
| VPC-to-VPC Overlap NAT | skip^GW | skip^GW | run | run | run | run |
| VPC-to-VPC Port Forward NAT | skip^GW | skip^GW | run | run | run | run |
| VPC-to-VPC Masq+PortFwd NAT | skip^GW | skip^GW | run | run | run | run |
| BGP Ext No NAT | skip^GW | skip^GW | run† | run† | run | run† |
| BGP Ext Static NAT | skip^GW | skip^GW | run† | run† | run | run† |
| BGP Ext Masquerade NAT | skip^GW | skip^GW | run† | run† | run | run† |
| BGP Ext Port Forward NAT | skip^GW | skip^GW | run† | run† | run | run† |
| BGP Ext Masq+PortFwd NAT | skip^GW | skip^GW | run† | run† | run | run† |
| Static Ext No NAT | skip^GW | skip^GW | run‡ | run‡ | run | run‡ |
| Static Ext Static NAT | skip^GW | skip^GW | run‡ | run‡ | run | run‡ |
| Static Ext Masquerade NAT | skip^GW | skip^GW | run‡ | run‡ | run | run‡ |
| Static Ext Port Forward NAT | skip^GW | skip^GW | run‡ | run‡ | run | run‡ |
| Static Ext Masq+PortFwd NAT | skip^GW | skip^GW | run‡ | run‡ | run | run‡ |

**Legend:** skip^VS = skipped (virtual switches), skip^GW = skipped (no gateway),
skip^NM = skipped (no mesh links), run* = depends on hardware RoCE support,
**run** = uniquely exercised on HLAB,
run† = requires BGP external with NAT pool annotation,
run‡ = requires static external with NAT pool annotation

### Upgrade Test Coverage

Upgrade tests (`upgradefrom: "26.01"`) run a 3-phase process:

1. **Before**: Install old version, setup VPCs + peerings, verify connectivity
2. **Current**: Upgrade to current version with `--upgrade`, verify connectivity
3. **After**: Re-verify post-upgrade stability

Release tests are NOT run during upgrade configurations — only smoke/connectivity
checks are performed.

---

## Switch Profile Capability Matrix

Test behavior depends on the switch profile's capabilities. Features like subinterfaces,
RoCE, L2VNI, and MCLAG/ESLAG are profile-specific — a test that runs on one switch model
may be skipped on another. VLAB always uses the `vs` (Virtual Switch) profile, which has
broad feature flags but limited functional fidelity.

### Profile Capabilities

| Profile | Silicon | Subinterfaces | L2VNI | L3VNI | RoCE | MCLAG | ESLAG | ACLs | Notes |
|---------|---------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|-------|
| **vs** (Virtual) | VS | yes | yes | yes | yes | yes | yes | no | VLAB default; limited fidelity |
| **dell-s5248f-on** | TD3-X7 | yes | yes | yes | yes | yes | yes | yes | Full-featured |
| **dell-s5232f-on** | TD3-X5 | yes | yes | yes | yes | yes | yes | yes | Full-featured |
| **dell-z9332f-on** | TH3 | no | no | no | yes | no | no | yes | Spine-only (no VNI/MCLAG) |
| **celestica-ds2000** | TD3-X7 | yes | yes | yes | no | yes | yes | yes | No RoCE |
| **celestica-ds3000** | TD3-X3 | yes | yes | yes | yes | yes | yes | yes | Full-featured |
| **celestica-ds4000** | TH4G | no | no | no | yes | no | no | yes | Spine-only (no VNI/MCLAG) |
| **celestica-ds4101** | TH5 | no | no | no | yes | no | no | yes | ECMP RoCE QPN |
| **celestica-ds5000** | TH | yes | no | yes | yes | no | no | yes | L3VNI only, ECMP RoCE QPN |
| **edgecore-dcs203** | TD3-X5 | yes | yes | yes | yes | yes | yes | yes | Full-featured |
| **edgecore-dcs204** | TD3-X3 | yes | yes | yes | yes | yes | yes | yes | Full-featured |
| **edgecore-dcs501** | TH | no | no | no | no | no | no | yes | Most limited |
| **edgecore-eps203** | TD3-X3 | no | yes | yes | no | yes | yes | yes | Campus profile, no subinterfaces |
| **supermicro-sse-c4632sb** | TD3-X3 | yes | yes | yes | yes | yes | yes | yes | Full-featured (mirrors DS3000) |

CLS+ variants (`*-clsp`) have the same capability flags as their base counterparts.

### Capability Groups and Test Impact

| Capability Group | Profiles | Test Impact |
|-----------------|----------|-------------|
| **Full-featured** (L2VNI + subinterfaces + MCLAG/ESLAG) | dell-s5248f, dell-s5232f, celestica-ds3000, edgecore-dcs203, edgecore-dcs204, supermicro-sse-c4632sb | All tests can run |
| **L3VNI only** (no L2VNI) | celestica-ds5000 | Must use `--vpc-mode l3vni`; ESLAG servers skipped |
| **Spine-only** (no VNI, no MCLAG/ESLAG) | dell-z9332f, celestica-ds4000, celestica-ds4101, edgecore-dcs501 | Cannot serve as leaves in VPC topologies |
| **No subinterfaces** | dell-z9332f, celestica-ds4000, celestica-ds4101, edgecore-dcs501, edgecore-eps203 | SubInterfaces skip flag set → VPC peering + external tests skipped |
| **No RoCE** | celestica-ds2000, edgecore-dcs501, edgecore-eps203 | RoCE tests skipped |
| **Campus** | edgecore-eps203 | Different NOS (SONiC BCM Campus), no subinterfaces/RoCE |

### Per-Environment Switch Profile Coverage

| Environment | Leaf Profiles | Spine Profiles | Unique Profile Coverage |
|-------------|--------------|----------------|------------------------|
| **VLAB** (all configs) | vs | vs | Virtual only — broad flags, limited fidelity |
| **env-ci-1.l** | celestica-ds3000, supermicro-sse-c4632sb | celestica-ds4000 | TD3-X3 leaf, TH4G spine |
| **env-1** | dell-s5248f-on | dell-s5232f-on | Dell TD3-X7 leaf, TD3-X5 spine |
| **env-3** | dell-s5248f, edgecore-dcs203, supermicro-sse-c4632sb, edgecore-eps203 | edgecore-as7712 | **Most diverse**: 4 leaf profiles including campus |
| **env-4** | celestica-ds3000, supermicro-sse-c4632sb | celestica-ds4000 | Similar to env-ci-1.l |
| **env-5** | celestica-ds5000, celestica-ds3000 | celestica-ds5000 | **Only L3VNI-only leaf** (ds5000), **400G**, ECMP QPN |

### VPC Mode × Profile Constraints

| VPC Mode | Compatible Leaf Profiles | Incompatible | Notes |
|----------|------------------------|--------------|-------|
| **L2VNI** | All profiles with `L2VNI: yes` | ds5000, z9332f, ds4000, ds4101, dcs501 | Default mode; broadest feature coverage |
| **L3VNI** | All profiles with `L3VNI: yes` | z9332f, ds4000, ds4101, dcs501 | Required for ds5000; ESLAG not supported |

---

## Per-Environment HLAB Coverage

| Feature | env-ci-1.l (CI) | env-1 | env-3 | env-4 | env-5 |
|---------|----------------|-------|-------|-------|-------|
| Automated CI | **Yes** | No | No | No | No |
| Spine-leaf | Yes | Yes | Yes | Yes | Yes |
| Mesh | No | Yes | No | No | Yes |
| Gateway | Optional | Yes (mesh mode) | No | No | Yes |
| MCLAG | Yes | Yes | Yes | Yes | No |
| ESLAG | Yes | Yes | No | Yes | Yes |
| 400G links | No | No | No | No | **Yes** |
| Mixed vendors | No | No | **Yes** | No | No |
| BGP externals | Yes | Yes | Yes | Yes | Yes |
| L3VNI | CI only | Manual | Manual | Manual | Manual |
| RoCE | Depends | Depends | Depends | Depends | Depends |
| Release tests | Yes (label/schedule) | Manual | Manual | Manual | Manual |

---

## CI Runtime Characteristics

Observed release-test (-rt) durations (as of April 2025):

| CI Config | VLAB Duration | Notes |
|-----------|:------------:|-------|
| v-manual-l2vni-rt | ~41 min | Fastest: no installer build |
| v-iso-l3vni-rt | ~42 min | |
| v-mesh-gw-iso-l2vni-rt | ~51 min | Gateway adds ~10 min |
| v-up26.01-mesh-iso-l2vni-rt | ~45 min | 3-phase upgrade |
| v-up26.01-iso-l3vni-rt | ~50 min | 3-phase upgrade |
| v-up26.01-iso-l2vni-rt | ~59 min | 3-phase upgrade |
| v-gw-onie-iso-l3vni-rt | ~63 min | ONIE + GW adds build time |
| v-gw-onie-usb-l2vni-rt | ~66 min | USB build is slowest |
| **h-gw-iso-l2vni-rt** | **~191 min** | HLAB: real switch boot + ONIE |

Total wall-clock for a full CI matrix with release tests: **~3+ hours** on the HLAB runner,
**~66 min** worst-case on VLAB runners (runs in parallel).

As the test suite grows (currently 45 tests), runtime becomes a concern for PR feedback
loops. The existing `--release-test-regexes` filtering mechanism enables running a subset
of tests, but the CI pipeline does not yet use it to create fast/full tiers.

See [Test Gaps and Roadmap](testing-gaps-and-roadmap.md) for the proposed tiered test
strategy.

---

## Connectivity Test Details

The `test-connectivity` command validates reachability between all server pairs using:

| Method | Default Count | Extended Count | Min Speed (HLAB) | Notes |
|--------|--------------|---------------|-------------------|-------|
| **Ping** | 5 | 5 | — | ICMP reachability |
| **iPerf3** | 3 sec | 10 sec | 8.2 Gbps | Throughput validation (disabled on VS) |
| **Curl** | 1 | 3 | — | HTTP via toolbox demo server |

In VLAB (virtual switches), iPerf minimum speed is set to 0 since virtual networking
cannot match physical throughput.
