# Test Gaps and Improvement Roadmap

This document identifies gaps in the current testing strategy and proposes improvements.
It is organized by priority: critical gaps that affect confidence in releases, then
structural improvements that enable better testing over time.

## Context

The current test automation is a first iteration, evolved from a spreadsheet-based
test plan into a CI-based strategy. Key constraints:

- Only `env-ci-1.l` runs as an automated CI ephemeral runner
- Other HLAB environments (env-1 through env-5) require manual orchestration
- Gateway and dataplane testing is relatively new and evolving
- Release tests are only run on schedule or with explicit labels, not on every PR

---

## Gap 1: Virtual Switch Skip Gap

**Impact: High** — Many tests are skipped on virtual switches, meaning they only run
on the single HLAB CI environment.

Tests that ONLY run on HLAB (`VirtualSwitch` skip flag):

- All failover tests (MCLAG, ESLAG, Bundled, Spine) — 4 tests
- VPC restrictions / isolation — 2 tests
- Multi-subnet filtering — 1 test
- StaticExternal — 1 test
- Breakout ports — 1 test
- RoCE — 1 test (also requires RoCE-capable hardware)

**10 out of 33 tests (30%)** only execute on physical hardware, but HLAB CI runs
only on one topology configuration (spine-leaf + gateway + l2vni).

**Recommendations:**
- [ ] Evaluate which VirtualSwitch-skipped tests could be adapted to run on VS
      with relaxed assertions (e.g., failover tests could verify control-plane
      convergence even if data-plane failover behavior differs on VS)
- [ ] Prioritize automating additional HLAB environments to increase hardware test
      coverage across different topologies and vendors

---

## Gap 2: HLAB Environment Automation

**Impact: High** — Only 1 of 5 HLAB environments is automated in CI.

| Environment | Unique Coverage | Automation Status |
|-------------|----------------|-------------------|
| env-ci-1.l | Celestica DS3000/DS4000, MCLAG+ESLAG | **Automated** |
| env-1 | Dell S5232/S5248, mesh+gateway | Manual |
| env-3 | Mixed vendor (Edgecore, Dell, Supermicro) | Manual |
| env-4 | Celestica DS3000/DS4000/Supermicro | Manual |
| env-5 | **400G**, DS5000, 2 gateways | Manual |

**Recommendations:**
- [ ] Convert env-1 to ephemeral K8s runner — adds Dell hardware and mesh+gateway
      coverage to CI
- [ ] Convert env-5 to ephemeral K8s runner — adds 400G and multi-gateway coverage
- [ ] Convert env-3 to ephemeral K8s runner — adds mixed-vendor coverage
- [ ] Define a minimal CI matrix per HLAB environment (not all 9 configs need to run
      on all environments)
- [ ] Implement a scheduling strategy: nightly full matrix on all HLABs, PR-triggered
      on env-ci-1.l only

---

## Gap 3: Gateway and Dataplane Test Depth

**Impact: High → Medium** — Gateway/dataplane coverage has improved significantly with
PR [#1579](https://github.com/githedgehog/fabricator/pull/1579) but resilience and
performance testing remain thin.

Current gateway E2E coverage (post-#1579):
- Gateway VPC-to-VPC peering (simple, loop, mixed) — 4 tests
- Gateway failover — 1 test
- VPC-to-VPC NAT (masquerade, static, bidirectional, overlap, port-fwd, masq+port-fwd) — 6 tests
- **NEW**: BGP external NAT (no-NAT, static, masquerade, port-fwd, masq+port-fwd) — 5 tests
- **NEW**: Static external NAT (no-NAT, static, masquerade, port-fwd, masq+port-fwd) — 5 tests

**Total: 21 gateway tests** covering the full NAT mode × external type matrix.

Remaining gaps:
- **Performance under load**: No sustained throughput testing through gateway NAT paths
- **Flow table exhaustion**: No test for gateway behavior when flow tables fill up
- **Gateway scaling**: Only 2 gateways tested; no coverage for larger gateway groups
- **Dataplane crash recovery**: No test for dataplane process restart/recovery
- **FRR failover**: No test for FRR process restart while dataplane continues
- **Mesh+GW+L3VNI**: This specific combination is commented out in CI matrix due to an
  ESLAG limitation. GW+L3VNI *is* tested via the `GW+ONIE ISO/l3vni` spine-leaf config.
- **Gateway metrics validation**: No tests verify dataplane/FRR metrics are correctly
  exported

**Recommendations:**
- [x] ~~Add port-forwarding NAT release test~~ (done in #1579)
- [x] ~~Add gateway + external peering release test~~ (done in #1579, full matrix)
- [ ] Add gateway process crash/restart resilience test
- [ ] Add basic gateway throughput benchmark (iPerf through NAT path with min threshold)
- [ ] Track ESLAG limitation resolution and re-enable mesh+GW+L3VNI CI config
- [ ] Add gateway metrics validation test (check Prometheus endpoints return expected
      counters)

---

## Gap 4: Upgrade Test Coverage

**Impact: Medium** — Upgrade tests only validate smoke/connectivity, not release tests.

Current state:
- 3 upgrade configs: L2VNI, L3VNI, mesh L2VNI (all from 26.01)
- Only runs: setup-vpcs → setup-peerings → test-connectivity (smoke)
- No release tests executed during upgrade path
- No gateway upgrade path tested

**Recommendations:**
- [ ] Run at least a subset of release tests after upgrade (e.g., the Single VPC suite)
- [ ] Add upgrade test with gateway enabled (upgrade gateway dataplane + FRR together)
- [ ] Test upgrade from N-2 version (not just N-1) for customers on older releases
- [ ] Add rollback test: verify fabric survives a failed/partial upgrade

---

## Gap 5: Mesh Topology Coverage

**Impact: Medium** — Mesh mode has limited test depth.

Current state:
- 1 VLAB CI config tests mesh (mesh+GW, l2vni)
- Mesh Failover test exists but is skipped on VLAB (VirtualSwitch flag)
- No mesh+L3VNI (commented out due to ESLAG limitation)
- No mesh without gateway in CI
- env-1 and env-5 support mesh mode but are manual only

**Recommendations:**
- [ ] Add mesh-only (no gateway) VLAB CI config for basic mesh validation
- [ ] Automate env-1 mesh+gateway mode as HLAB CI to get mesh failover coverage
- [ ] Track ESLAG limitation and re-enable mesh+L3VNI when resolved

---

## Gap 6: Observability and Monitoring Tests

**Impact: Medium** — Only basic Loki and Prometheus reachability is tested.

Current state:
- 2 tests: Loki Observability, Prometheus Observability (in No VPCs suite)
- Tests verify endpoints are reachable, not that correct data flows through

Missing:
- **End-to-end log pipeline**: Verify switch logs appear in Loki with correct labels
- **Metrics correctness**: Verify specific metrics (interface counters, BGP session
  counts) are present and accurate
- **Alert validation**: No tests for alerting rules or Grafana dashboard queries
- **Fabric Proxy**: No test validates Alloy → Fabric Proxy → external LGTM pipeline

**Recommendations:**
- [ ] Extend Loki test to query for specific switch log patterns
- [ ] Extend Prometheus test to validate key metrics exist with expected labels
- [ ] Add Fabric Proxy pipeline test if external LGTM stack available in test env

---

## Gap 7: HostBGP and Advanced Server Features

**Impact: Low-Medium → Being addressed** by PR
[fabricator#1648](https://github.com/githedgehog/fabricator/pull/1648).

Current state:
- No release test for HostBGP (unnumbered BGP on host servers) — **PR #1648 adds 5 tests**
- Setup code has `HostBGPSubnet` option but no test exercises it — **PR #1648 exercises it**

PR #1648 adds a full Host-BGP Suite: VPC peering, external peering, full mesh, gateway
peering, and peering lifecycle (add/remove/re-add). See [In-Flight Work](#in-flight-work)
for details.

**Remaining after #1648:**
- [ ] HostBGP with L3VNI mode
- [ ] HostBGP failover (link down on host BGP interface)

---

## Gap 8: L3VNI-Specific Testing

**Impact: Low-Medium** — L3VNI has its own constraints but limited dedicated tests.

Current state:
- L3VNI runs through the same release tests as L2VNI
- `RequireAllServers` is set to false for L3VNI (ESLAG servers skipped)
- Extra 10-second sleep added for route convergence in L3VNI mode
- No tests specific to L3VNI behavior (e.g., /32 host routes, arp_notify requirement)

**Recommendations:**
- [ ] Add L3VNI-specific test that validates /32 routes are installed correctly
- [ ] Verify L3VNI fails gracefully when arp_notify is not configured on hosts
- [ ] Test L3VNI with different switch profiles (Celestica DS5000 is the primary
      L3VNI use case)

---

## Gap 9: Negative and Fault-Injection Testing

**Impact: Low-Medium** — Tests primarily validate happy paths.

Missing:
- **Invalid configuration rejection**: No test applies invalid CRDs and verifies
  webhook rejection
- **Resource exhaustion**: No test for VNI/VLAN namespace exhaustion
- **Split-brain scenarios**: No test for control plane network partition
- **Switch reboot during operation**: No test for switch reboot mid-traffic

**Recommendations:**
- [ ] Add negative API tests (invalid VPC, overlapping subnets, etc.) — these could
      run in the `test-api-auto` Kind cluster without needing VLAB
- [ ] Add switch reboot resilience test in HLAB (reboot leaf, verify recovery)

---

## Gap 10: Test Reporting and Visibility

**Impact: Medium → Partially addressed** by CI ARC dashboard and lab PR #364.

Current state:
- JUnit XML published via EnricoMi/publish-unit-test-result-action on GitHub
- Debug artifacts uploaded per run
- **CI ARC dashboard** ([ci.hhdev.io](http://ci.hhdev.io/)) provides runner queue times
  and workflow-level trend visibility
- **Lab PR #364** introduces PoC scripts for flakiness detection and show-tech deep
  inspection
- **Inception** analyzer planned for proactive anomaly detection from show-tech data
- Manual environments still lack standardized reporting

**Recommendations:**
- [ ] Productize lab PR #364 analyzer into a CI-integrated tool
- [ ] Build Inception analyzer for show-tech anomaly detection (BGP flaps, memory
      pressure, unexpected route counts that don't trigger failures)
- [ ] Add test-level metrics to CI ARC dashboard (pass/fail/skip trends per test)
- [ ] Standardize reporting for manual HLAB runs (same JUnit XML upload path)
- [ ] Add test duration tracking to detect performance regressions in test execution

---

## Gap 11: Tiered Test Strategy for Faster PR Feedback

**Impact: Medium-High** — As the test suite grows (now 45 tests), full release-test
runs take 41-66 min on VLAB and ~3h on HLAB. Every PR waiting for the full matrix
slows development velocity.

Current state:
- The `--release-test-regexes` and `--release-test-regexes-invert` flags already exist
  and allow filtering tests by name pattern
- CI does not use this filtering — every release-test run executes all non-skipped tests
- Adding 12 gateway+external NAT tests (PR #1579) added ~3-4 min to GW configs with no
  measurable overhead on non-GW configs (tests are skip-flagged)

**Proposed tiered strategy:**

| Tier | When | What | Target Time |
|------|------|------|-------------|
| **Smoke** | Every PR | Connectivity tests only (no release tests) | ~20-30 min |
| **PR Release** | PR with `ci:+release` label | Core subset: Single VPC + basic peering | ~30-40 min |
| **Nightly Full** | Scheduled (`0 6 * * *`) | All 45 release tests, all configs | ~66 min VLAB, ~3h HLAB |
| **Extended** | Manual or weekly | `--extended` flag, longer iPerf, more curls | ~90 min |

PR [fabricator#1290](https://github.com/githedgehog/fabricator/pull/1290) lays the
groundwork by introducing a structured `OnReady Suite` that replaces the ad-hoc
`setup-vpcs` / `setup-peerings` / `test-connectivity` commands. This makes the
smoke-vs-release boundary explicit and enables regex-based tier selection.

**Recommendations:**
- [ ] Land PR #1290 (OnReady suite) as the foundation for tiered execution
- [ ] Define a "core" regex that covers the most critical tests for PR feedback
      (e.g., `"No restrictions|DNS|Gateway Peering$|Masquerade Source NAT"`)
- [ ] Add `releasetest_regex` to CI matrix for PR-triggered runs
- [ ] Keep nightly/scheduled runs with the full suite
- [ ] Tag tests with estimated duration to inform tier selection
- [ ] Consider splitting Suite 4 (27 tests) into sub-suites that can be independently
      selected

---

## Gap 13: Switch Profile Coverage Breadth

**Impact: Medium** — Each HLAB tests only 1-2 leaf profiles; some profiles have no
HLAB coverage at all.

Profiles with **no dedicated HLAB coverage**:
- `edgecore-dcs204` — not present in any environment
- `edgecore-dcs501` — not present in any environment
- `celestica-ds4101` (TH5, ECMP QPN) — not present in any environment

Profiles with **limited coverage** (single environment, manual only):
- `celestica-ds5000` — env-5 only (manual), the only L3VNI-only leaf
- `edgecore-eps203` (campus) — env-3 only (manual)
- `dell-s5232f-on` / `dell-s5248f-on` — env-1 only (manual)

The CI-automated HLAB (`env-ci-1.l`) only covers `celestica-ds3000` and
`supermicro-sse-c4632sb` leaves with `celestica-ds4000` spines. This means CI never
exercises Dell, Edgecore, DS5000, or campus profiles.

**Recommendations:**
- [ ] Prioritize automating env-3 (most diverse: 4 leaf profiles including campus)
- [ ] Prioritize automating env-5 (only DS5000 / L3VNI-only / 400G coverage)
- [ ] Track which profiles have zero automated test coverage as a release-gate metric
- [ ] Consider adding profile-specific CI configs that target known capability gaps
      (e.g., a DS5000 L3VNI-only run)

---

## In-Flight Work

Active PRs and initiatives that address gaps identified above.

### PR [fabricator#1290](https://github.com/githedgehog/fabricator/pull/1290) — OnReady Smoke Test Refactor (Draft)

**Addresses:** Gap 11 (Tiered Strategy), smoke test reliability

Replaces the current `setup-vpcs` / `setup-peerings` / `test-connectivity` on-ready
sequence with a single structured release test scenario (`OnReady Suite`). Key changes:

- New `rt_on_ready_suite.go` (+705 lines) implementing a comprehensive smoke test
  that exercises VPC setup, peering, and connectivity as one atomic test case
- Adds `--on-ready-only` flag to run only the OnReady suite (skipping full release tests)
- Designed for VLAB only — may skip in HLAB where server/external counts differ
- Makes the smoke-vs-release distinction explicit in CI: upgrade jobs can now produce
  JUnit XML even when running smoke-only
- Still in draft: gateway coverage being added, discussion on extending VLAB-specific coverage

**Status:** Open (draft), authored by @edipascale, tested on VLAB CI.

### PR [fabricator#1648](https://github.com/githedgehog/fabricator/pull/1648) — Host-BGP Release Test Suite

**Addresses:** Gap 7 (HostBGP coverage)

Adds a new **Host-BGP Suite** with 5 test cases:

| Test Case | Skip Conditions | What It Validates |
|-----------|----------------|-------------------|
| HostBGP VPC peered with regular VPC | NoUnbundledNonMclag, NoServers | BGP host advertises VIPs, reachable from peered VPC |
| HostBGP VPC peered with BGP external | NoUnbundledNonMclag, NoExternals, NoServers | Host VIPs reachable from external peer |
| HostBGP VPC in full mesh with other VPCs | NoUnbundledNonMclag, NoServers | Multiple VPCs with HostBGP in full mesh peering |
| HostBGP VPC with gateway peering | NoUnbundledNonMclag, NoGateway, NoServers | HostBGP through gateway NAT path |
| HostBGP VPC peering removal and re-add | NoUnbundledNonMclag, NoServers | Peering lifecycle: add → verify → remove → re-add |

Introduces new skip flag `NoUnbundledNonMclag` — HostBGP requires unbundled connections
not in MCLAG pairs (uses unnumbered BGP on the host interface).

**Status:** Open, authored by @pau-hedgehog, labeled `ci:+release` + `ci:+hlab`.

### PR [lab#364](https://github.com/githedgehog/lab/pull/364) — VLAB CI Analyzer Scripts PoC

**Addresses:** Gap 10 (Reporting/Visibility), Flakiness Detection

Proof-of-concept for automated analysis of CI failures and flaky runs:

- Scripts to enumerate and compare workflow runs across branches
- Show-tech deep inspection: parses show-tech from CI artifacts to identify
root cause when not evident in workflow logs
- Timing analysis per script


**Status:** Open, authored by @pau-hedgehog. Large PR (+15k lines including).

### Inception: Show-Tech Advanced Analyzer

**Addresses:** Gap 10, proactive anomaly detection

A planned tool to analyze show-tech output from CI runs to:
- Detect anomalies that don't trigger test failures (e.g., BGP flaps that self-heal,
  memory pressure, unexpected route counts)
- Correlate across multiple runs to identify patterns
- Surface issues before they become test failures or flakes
- Complement the existing run-vlab-analyzer's 3-tier analysis framework

**Status:** Conceptual / early design.

### CI ARC Dashboard (Internal)

**Addresses:** Gap 10 (Reporting/Visibility)

Live dashboard at [ci.hhdev.io](http://ci.hhdev.io/) providing:
- Overview of running workflows across all runners
- Queue times and runner utilization
- Historical trends for CI infrastructure capacity planning

**Status:** Operational.

---

## Prioritized Roadmap

### Recently Completed

- [x] Gateway external NAT test matrix (PR #1579) — 12 new tests covering
  BGP/static external × all NAT modes (no-NAT, static, masquerade, port-fwd, masq+port-fwd)
- [x] Port-forwarding NAT release test for VPC-to-VPC peering (PR #1579)
- [x] Refactored external selection to separately track BGP and static externals (PR #1579)

### In Progress

- [ ] OnReady smoke test refactor (PR #1290) — structured smoke test replacing ad-hoc
      on-ready commands, enables tiered test strategy
- [ ] Host-BGP release test suite (PR #1648) — 5 new tests for HostBGP feature
- [ ] VLAB CI flakiness analyzer PoC (lab PR #364) — scripts for failure analysis and
      show-tech deep inspection
- [ ] Inception show-tech advanced analyzer — proactive anomaly detection

### Phase 1: Quick Wins (Low Effort, High Value)

1. Implement tiered release-test strategy (builds on PR #1290's OnReady suite to
   define smoke/core/full tiers with regex filtering)
2. Run subset of release tests in upgrade configurations
3. Add mesh-only (no gateway) VLAB CI config

### Phase 2: HLAB Automation (Medium Effort, High Value)

4. Convert env-1 to ephemeral K8s runner (Dell hardware, mesh+gateway)
5. Convert env-5 to ephemeral K8s runner (DS5000 L3VNI-only, 400G, multi-gateway)
6. Convert env-3 to ephemeral K8s runner (most diverse profile mix including campus)
7. Define per-environment CI matrix and scheduling strategy
8. Standardize reporting across all environments

### Phase 3: Depth and Resilience (Higher Effort)

9. Gateway crash/restart resilience test
10. Switch reboot resilience test (HLAB)
11. Negative API test suite (Kind cluster)
12. Extend observability tests to validate data content
13. L3VNI-specific validation tests

### Phase 4: Continuous Improvement

14. Productize Inception analyzer from PoC into CI-integrated tool
15. Flakiness detection and test dashboard (builds on lab PR #364)
16. Historical trend analysis (complement CI ARC dashboard with test-level data)
17. Performance regression detection
18. Convert remaining HLAB (env-4)
19. Evaluate relaxed-assertion versions of VS-skipped tests for VLAB
20. Profile coverage tracking as release-gate metric
