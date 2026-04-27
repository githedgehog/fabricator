# Testing Infrastructure: VLAB and HLAB

This document describes the virtual and hardware lab infrastructure used for
end-to-end testing of the Hedgehog Open Network Fabric.

## VLAB (Virtual Lab)

A VLAB is a fully virtualized Hedgehog fabric running on a single KVM-capable host.
All components — control nodes, switches, gateways, servers, and externals — run as
QEMU/KVM virtual machines managed by the `hhfab` CLI.

### Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  VLAB Host (GitHub Actions runner or developer workstation)  │
│                                                              │
│  ┌──────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐         │
│  │ control  │  │ spine-1 │  │ spine-2 │  │  ...    │         │
│  │  (K3s)   │  │  (VS)   │  │  (VS)   │  │         │         │
│  └────┬─────┘  └────┬────┘  └────┬────┘  └────┬────┘         │
│       │             │            │            │              │
│  ┌────┴─────┐  ┌────┴────┐   ┌───┴─────┐  ┌───┴─────┐        │
│  │  leaf-1  │  │ leaf-2  │   │ leaf-3  │  │  ...    │        │
│  │  (VS)    │  │  (VS)   │   │  (VS)   │  │         │        │
│  └────┬─────┘  └────┬────┘   └────┬────┘  └────┬────┘        │
│       │             │             │            │             │
│  ┌────┴─────┐  ┌────┴────┐    ┌───┴─────┐  ┌───┴─────┐       │
│  │ server-1 │  │server-2 │    │  gw-1   │  │external │       │
│  │(toolbox) │  │(toolbox)│    │(dp+FRR) │  │  (FRR)  │       │
│  └──────────┘  └─────────┘    └─────────┘  └─────────┘       │
│                                                              │
│  ┌──────────────────────────────────────────────┐            │
│  │  Local OCI Registry (zot @ 127.0.0.1:30000)  │            │
│  └──────────────────────────────────────────────┘            │
└──────────────────────────────────────────────────────────────┘

VS = Virtual Switch (SONiC Virtual Switch profile)
```

### VM Types

| VM Type | OS / Image | Role |
|---------|-----------|------|
| **Control** | Flatcar Linux + K3s | Runs fabric controller, DHCP, webhooks |
| **Switch** | SONiC VS (Virtual Switch) | Simulates leaf/spine switches |
| **Gateway** | Flatcar Linux + dataplane + FRR | Runs gateway packet processing |
| **Server** | Linux + toolbox | Workload endpoint for connectivity tests |
| **External** | Linux + FRR | Simulates external BGP peer or static endpoint |

### VLAB Lifecycle

```
hhfab init        →  Initialize working directory, download artifacts
hhfab vlab gen    →  Generate wiring diagram for virtual topology
hhfab vlab up     →  Build installer, create VMs, boot, wait for ready,
                     execute --ready commands, collect artifacts
```

#### Topology Generation (`hhfab vlab gen`)

The `VLABBuilderDefault` generates topologies with configurable parameters:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `--spines-count` | 2 | Number of spine switches |
| `--fabric-links-count` | 2 | Links per spine-leaf pair |
| `--mesh-links-count` | 0 | Leaf-to-leaf mesh links (0 = spine-leaf mode) |
| `--mclag-leafs-count` | 2 | Leaves in MCLAG pairs |
| `--eslag-leaf-groups` | "2" | ESLAG groups (comma-separated sizes, 2-4 each) |
| `--orphan-leafs-count` | 1 | Non-redundant standalone leaves |
| `--gateway-uplinks` | 2 | Gateway uplink count (0 = no gateway) |
| `--gateway-driver` | `dpdk` | Gateway driver: `kernel` or `dpdk` |
| `--externals-bgp` | 1 | BGP external peers |
| `--externals-static` | 1 | Static external peers |
| `--externals-static-proxy` | 0 | Static externals with proxy-ARP |

There is also an experimental `VLABBuilderGPURail` for GPU rail-optimized topologies.

#### Build Modes

| Mode | Description |
|------|-------------|
| `manual` | Direct VM creation without installer (fastest, default for CI) |
| `iso` | Builds ISO installer image (~7.5 GB), boots from it |
| `usb` | Builds USB installer image, boots from it |

#### OnReady Commands

When the VLAB is ready (all VMs booted, K3s running), `--ready` commands execute
sequentially:

```bash
hhfab vlab up \
  --ready=switch-reinstall \   # ONIE reinstall (HLAB, upgrade tests)
  --ready=inspect \            # Cluster + switch health checks
  --ready=setup-vpcs \         # Create VPCs, subnets, attachments
  --ready=setup-peerings \     # Create peerings (VPC, external, gateway)
  --ready=test-connectivity \  # Ping, iPerf3, curl between servers
  --ready=release-test \       # Full release test suites
  --ready=exit                 # Clean shutdown
```

### VLAB Access and Debugging

| Command | Description |
|---------|-------------|
| `hhfab vlab ssh <vm>` | SSH into any VLAB VM |
| `hhfab vlab scp <src> <dst>` | Copy files to/from VM |
| `hhfab vlab serial <vm>` | Attach to VM serial console |
| `hhfab vlab show-tech` | Collect diagnostic bundles from all nodes |

In CI, `--pause-on-failure` pauses for 60 minutes on test failure, allowing
connection via Actions Runner Controller (ARC) host for live debugging.

### VLAB Resource Configuration

VM resources can be overridden via CLI flags or environment variables:

| Variable | Controls |
|----------|---------|
| `HHFAB_VLAB_CONTROL_CPUS/RAM/DISK` | Control node VM sizing |
| `HHFAB_VLAB_GW_CPUS/RAM/DISK` | Gateway VM sizing |
| `HHFAB_VLAB_SERVER_CPUS/RAM` | Server VM sizing |

---

## HLAB (Hardware/Hybrid Lab)

An HLAB uses **physical switches** connected to a dedicated test server, while control
nodes, gateways, servers, and externals still run as VMs on the server. This tests the
real SONiC stack, ASIC behavior, and physical port configurations.

### Architecture

```
┌─────────────────────────────────────────────────────┐
│  HLAB Server (GitHub Actions 'hlab' runner)         │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐           │
│  │ control  │  │ gateway  │  │ servers  │  (VMs)    │
│  │  (K3s)   │  │ (dp+frr) │  │(toolbox) │           │
│  └─────┬────┘  └─────┬────┘  └─────┬────┘           │
│        │             │             │                │
│  ══════╪═════════════╪═════════════╪═══ (NICs) ════ │
└────────┼─────────────┼─────────────┼────────────────┘
         │             │             │
    ┌────┴────┐    ┌───┴────┐    ┌───┴────┐
    │ DS4000  │    │ DS3000 │    │ DS3000 │   Physical
    │ (spine) │    │ (leaf) │    │ (leaf) │   Switches
    └─────────┘    └────────┘    └────────┘
```
NOTE: oversimplified

### HLAB Environments

Wiring files for HLAB environments live in two repos:

- **`lab-ci/`**: Automated CI environments (currently `env-ci-1.l`)
- **`lab/`**: Manually operated environments for development and testing

#### CI Environment: `env-ci-1.l`

The only environment currently integrated into the CI pipeline.

| Component | Hardware |
|-----------|---------|
| Spines | 2x Celestica DS4000 |
| Leaves | 4x Celestica DS3000 (+ 1x Supermicro SSE-C4632) |
| Redundancy | MCLAG (ds3000-01 + ds3000-02), ESLAG (ds3000-03 + sse-c4632-01) |
| Externals | 2 BGP external peers on border leaves |
| Gateway | Optional (via `wiring-gateway.yaml`) |
| IP Range | 10.20.0.0/16 |

Configuration loaded from `lab-ci/envs/env-ci-1.l/`:
- `wiring.yaml` — switch and server physical wiring
- `wiring-gateway.yaml` — gateway-specific wiring (optional)
- `source.sh` — environment variables (TLS SANs, fabric mode, VM sizing)

#### Development/Manual Environments

| Environment | Hardware | Spines | Leaves | Features |
|-------------|----------|--------|--------|----------|
| **env-1** | Dell S5232F/S5248F | 2 | 4 | MCLAG+ESLAG, 9 servers, 2 modes: spine-leaf and mesh+gateway |
| **env-3** | Mixed (Edgecore AS7712, Dell S5248F, Supermicro SSE-C4632, Edgecore DCS203/EPS203) | 2 | 4 | Heterogeneous hardware validation |
| **env-4** | Celestica DS3000/DS4000, Supermicro SSE-C4632 | 2 | 5 | MCLAG+ESLAG |
| **env-5** | Celestica DS5000/DS3000 | 1 | 4 | **400G** links, 2 gateway nodes, spine-leaf + mesh + ESLAG modes |

Each environment supports multiple topologies by combining base wiring files:

**Example — env-1:**

```bash
# Spine-Leaf mode
hhfab init -f -c hhfab.yaml \
  -w wiring-0-base.yaml -w wiring-1-spine-leaf.yaml

# Mesh + Gateway mode
hhfab init -f -c hhfab-gw.yaml \
  -w wiring-mesh.yaml -w wiring-gateway.yaml
```

### HLAB vs VLAB Comparison

| Aspect | VLAB | HLAB |
|--------|------|------|
| Switches | Virtual (SONiC VS) | Physical (SONiC on ASIC) |
| Topology | Generated (`vlab gen`) | From wiring YAML files |
| Speed | Faster (no real hardware) | Slower (real boot, ONIE) |
| iPerf validation | Disabled (no min speed) | Enforced min throughput |
| CI timeout | 60-150 min | 90-320 min |
| Failover tests | Skipped (VS limitation) | Executed on real hardware |
| Runner label | `vlab` | `hlab` |
| Breakout/RoCE tests | Skipped | Executed if hardware supports |
| Cost | Compute only | Dedicated switch hardware |

### HLAB Operational Status

Currently, only `env-ci-1.l` runs as an automated CI ephemeral runner. All other HLAB
environments (env-1 through env-5) require **manual orchestration**: an operator runs
`hhfab` commands directly and interprets results. There is an open discussion about
converting all environments to Kubernetes-based ephemeral runners similar to `env-ci-1.l`.

### Wiring File Structure

Wiring files are YAML documents containing Kubernetes-style resources:

```yaml
apiVersion: wiring.githedgehog.com/v1beta1
kind: Switch
metadata:
  name: ds3000-01
spec:
  profile: celestica-ds3000
  role: server-leaf
  asn: 65101
  ip: 172.30.1.1/32
  vtepIP: 10.99.1.1/32
  protocolIP: 172.30.255.1/32
  groups:
    - mclag-1
  portBreakouts:
    E1/55: "4x25G"
---
apiVersion: wiring.githedgehog.com/v1beta1
kind: Connection
metadata:
  name: ds3000-01--ds4000-01--fabric
spec:
  fabric:
    links:
      - leaf:
          port: ds3000-01/E1/49
          ip: 172.30.10.1/31
        spine:
          port: ds4000-01/E1/1
          ip: 172.30.10.0/31
```

Files are modular — a base file defines common resources (namespaces, IP ranges) and
topology-specific files add switches, connections, and servers. Multiple `-w` flags to
`hhfab init` merge them together.

### Diagnostic Collection

Both VLAB and HLAB collect diagnostics via `hhfab vlab show-tech`, which runs
per-node-type scripts:

| Node Type | Diagnostics Collected |
|-----------|----------------------|
| Control | K8s pod status, logs, fabric controller state |
| Switch (SONiC) | Running config, BGP state, VXLAN tunnels, interface counters |
| Gateway | Dataplane stats, FRR routing table, NAT flow tables |
| Server | Interface state, routing table, bond status, DHCP leases |

In CI, diagnostics are always collected (`HHFAB_VLAB_COLLECT=true`) and uploaded as
GitHub Actions artifacts.

### VLAB Failure Analysis

The `lab` repo includes a **run-vlab-analyzer** tool — a 3-tier analysis framework for
diagnosing CI/CD failures:

1. **Level 1**: Fast job enumeration and status summary
2. **Level 2**: Pattern detection in serial logs and show-tech output
3. **Level 3**: Root cause analysis via artifact correlation
