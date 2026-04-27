# Testing Strategy Overview

This document describes the testing strategy for the Hedgehog Open Network Fabric.
It covers the testing layers, infrastructure, CI pipeline, coverage matrix, and known gaps.

For details on specific topics, see:

- [Testing Infrastructure (VLAB/HLAB)](testing-infrastructure.md)
- [Test Coverage Matrix](testing-coverage-matrix.md)
- [Test Gaps and Roadmap](testing-gaps-and-roadmap.md)

## Testing Layers

The Hedgehog project is composed of multiple repositories, each with their own unit and
component-level tests. End-to-end (E2E) testing is centralized in the **fabricator** repo
through a reusable GitHub Actions workflow (`run-vlab.yaml`) that is consumed across all
repos.

```
┌──────────────────────────────────────────────────────────────────────┐
│                       Testing Pyramid                                │
│                                                                      │
│                    ┌──────────────────┐                              │
│                    │  Release Tests   │ ← Full fabric/Gateway E2E    │
│                    │  (VLAB / HLAB)   │   via hhfab vlab CLI         │
│                 ┌──┴──────────────────┴──┐                           │
│                 │  Smoke / Connectivity  │ ← setup-vpcs,             │
│                 │  Tests                 │   test-connectivity       │
│              ┌──┴────────────────────────┴──┐                        │
│              │  API / Integration Tests     │ ← Kind cluster,        │
│              │                              │   agent-gNMI fixtures  │
│           ┌──┴──────────────────────────────┴──┐                     │
│           │  Unit Tests                        │ ← Per-repo:         │
│           │  (Go, Rust)                        │   go test, cargo    │
│           │                                    │   nextest, fuzz     │
│           └────────────────────────────────────┘                     │
│                                                                      │
│  ┌─────────────────────────────────────────────────────────────┐     │
│  │  Security Scanning (Trivy, govulncheck) — orthogonal layer  │     │
│  └─────────────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────┘
```

### Layer 1: Unit Tests

| Repo | Language | Framework | Notes |
|------|----------|-----------|-------|
| **fabric** | Go | `go test`, testify | Controllers, agents, managers, API types |
| **dataplane** | Rust | `cargo nextest`, bolero (fuzz) | 642+ tests, sanitizers (ASan, TSan, MSan), `cargo llvm-cov` for coverage |
| **fabricator** | Go | `go test` | CLI logic, config validation, wiring generation |
| **toolbox** | Go | — | Minimal; `go vet` / `go fmt` only |

### Layer 2: API and Integration Tests

- **fabric** `just test-api-auto`: spins up a Kind cluster and validates CRD schemas,
  webhooks, defaulting, and controller reconciliation logic.
- **fabric** `/test/integration/agent-gnmi/`: fixture-based tests that verify the agent
  produces correct gNMI configuration for SONiC virtual switches.

### Layer 3: Smoke and Connectivity Tests

These run inside a VLAB or HLAB as `--ready` commands passed to `hhfab vlab up`:

| Command | What It Does |
|---------|-------------|
| `switch-reinstall` | Reboots switches into ONIE and reinstalls NOS |
| `inspect` | Comprehensive cluster and switch readiness checks |
| `setup-vpcs` | Creates VPCs, subnets, and VPCAttachments from the wiring |
| `setup-peerings` | Creates VPCPeerings, ExternalPeerings, GatewayPeerings |
| `test-connectivity` | Validates reachability: ping, iPerf3, curl between all servers |
| `wait` | Blocks until manually released (for debugging) |

### Layer 4: Release Tests

The most comprehensive E2E tests, executed via `--ready=release-test`. These are structured
as JUnit test suites (`rt_*.go`) that:

1. Dynamically set up VPCs, peerings, externals, and gateway configs
2. Run test scenarios (connectivity, failover, NAT, isolation, DHCP, observability)
3. Revert changes after each test for isolation
4. Produce JUnit XML reports (`release-test.xml`)
5. Support regex filtering, fail-fast, pause-on-failure, and show-tech collection

See [Test Coverage Matrix](testing-coverage-matrix.md) for the full list of 45 test cases.

### Security Scanning

- **Trivy** (`trivy-security-scan.yml`): nightly container image scanning with SARIF output.
  Supports targeting control-only, gateway-only, switch-only, or all images.
- **govulncheck** (`govulncheck-scan.yaml`): weekly Go vulnerability analysis.

## CI Pipeline Architecture

### Trigger Model

| Event | What Runs |
|-------|-----------|
| PR opened/updated | All VLAB configs (unless `ci:-vlab` label). HLAB only with `ci:+hlab` label. Release tests only with `ci:+release` label. |
| Push to `master` / `release/*` | All VLAB configs |
| Tag `v*` | All VLAB + publish release |
| Scheduled (`0 6 * * *`) | All VLAB + release tests enabled |
| Scheduled (`0 10 * * *`) | All VLAB (smoke only) |
| Manual dispatch | Configurable: release tests, HLAB, debug |

### Reusable Workflow: `run-vlab.yaml`

This is the core E2E test workflow, consumed via `workflow_call` by:
- **fabricator** CI directly
- **Other repos** (fabric, dataplane, etc.) using `prebuild` to inject their changes
  (e.g., `just bump fabric v0.42.0`)

Key inputs:

| Input | Description | Values |
|-------|------------|--------|
| `fabricmode` | Fabric topology | `spine-leaf` (default) |
| `mesh` | Use mesh (leaf-to-leaf) links | `true/false` |
| `gateway` | Enable gateway VMs | `true/false` |
| `includeonie` | Include ONIE in installer | `true/false` |
| `buildmode` | Installer build method | `iso`, `usb`, `manual` |
| `vpcmode` | VPC encapsulation | `l2vni`, `l3vni` |
| `hybrid` | Use physical HLAB switches | `true/false` |
| `upgradefrom` | Test upgrade from version | e.g., `"26.01"` |
| `releasetest` | Run release test suites | `true/false` |
| `prebuild` | Script to run before build | e.g., `just bump fabric v0.42.0` |
| `custom_init_args` | Extra `hhfab init` args | string |
| `custom_gen_args` | Extra `hhfab vlab gen` args | string |

### VLAB Test Execution Flow

```
1. Checkout fabricator (+ lab-ci for HLAB)
2. Pre-populate .hhfab-cache from host
3. Setup Go + local OCI registry (zot)
4. [Upgrade only] Install old hhfab, init, gen, run "before" phase
5. Build hhfab: just push (builds + pushes OCI artifacts)
6. hhfab init (--fabric-mode, --gateways, --include-onie, custom args)
7. hhfab vlab gen (--mesh-links-count if mesh, custom args)
8. hhfab vlab up --build-mode --vpc-mode \
     --ready=switch-reinstall \
     --ready=inspect \
     --ready=setup-vpcs \
     --ready=setup-peerings \
     --ready=test-connectivity \
     --ready=release-test \     # if enabled
     --ready=exit
9. Collect debug artifacts (serial logs, show-tech, diagrams)
10. [Upgrade only] Run "after" phase for post-upgrade stability check
11. Upload release-test.xml artifacts
12. Publish test results via EnricoMi/publish-unit-test-result-action
```

### CI Matrix

9 configurations are tested (1 commented-out mesh+l3vni due to ESLAG limitation):

| Name | Mesh | Gateway | ONIE | Build | VPC | HLAB | Upgrade |
|------|------|---------|------|-------|-----|------|---------|
| Base | — | — | — | manual | l2vni | — | — |
| L3VNI | — | — | — | iso | l3vni | — | — |
| GW USB L2 | — | yes | yes | usb | l2vni | — | — |
| GW ISO L3 | — | yes | yes | iso | l3vni | — | — |
| HLAB GW L2 | — | yes | — | iso | l2vni | yes | — |
| Mesh GW L2 | yes | yes | — | iso | l2vni | — | — |
| Upgrade L2 | — | — | — | iso | l2vni | — | 26.01 |
| Upgrade L3 | — | — | — | iso | l3vni | — | 26.01 |
| Upgrade Mesh | yes | — | — | iso | l2vni | — | 26.01 |

### Runner Infrastructure

- **`vlab` runner**: KVM-capable machines running virtual switches, servers, gateways as VMs.
  Topologies are generated dynamically via `hhfab vlab gen`.
- **`hlab` runner**: Machines connected to physical Celestica DS3000/DS4000 switches.
  Wiring loaded from `lab-ci/envs/$KUBE_NODE/`. Currently only `env-ci-1.l`.
- **`lab` runner**: General build/test runner for unit tests, linting, bundling.

### Debug and Artifact Collection

Every VLAB/HLAB run collects (regardless of pass/fail):

- **Serial logs**: one per VM (control, switch, gateway, server, external)
- **Show-tech output**: switch diagnostics collected via `hhfab vlab show-tech`
- **Diagrams**: topology in drawio, dot, and mermaid formats
- **Versions**: component version information
- **Registry logs**: local OCI registry (zot) logs

Artifacts are uploaded as `fab-{run_id}-{slug}` and available for download from the
GitHub Actions run page.

## How Other Repos Consume E2E Tests

Other Hedgehog repos (e.g., `fabric`, `dataplane`) trigger E2E tests against their own
changes by calling the fabricator's `run-vlab.yaml` workflow with:

1. `fabricatorref`: pointing to a compatible fabricator branch
2. `prebuild`: a script that injects the repo's changes into the fabricator build,
   e.g., `just bump fabric v0.42.0` replaces the fabric dependency before building

This ensures that changes to any component are validated against the full fabric stack
before merging.
