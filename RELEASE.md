# Releasing Fabricator

Runbook for cutting a fabricator release. Release notes for users live in the [docs repository](https://github.com/githedgehog/docs) (published at docs.hedgehog.cloud), not here.

## Release numbering

- Fabricator releases are pre-1.0 SemVer tags: `v0.MINOR.PATCH`. Pushing a tag is what triggers publishing.
- Releases are rolling: each product release ships the current fabricator line, and patch releases stabilize that line in the run-up to the product release. Going back to patch an already-shipped minor is exceptional.
- Fabricator versions are independent from Hedgehog product releases, which are named `YY.MM` (for example `26.01`). Each product release pins the single fabricator version it ships, recorded in the release notes in the docs repository (`26.01` shipped fabricator `v0.45.5`).
- The CI upgrade matrix is keyed on product releases: upgrade jobs (`*-up<release>-*`, for example `v-up26.01-iso-l2vni`) install that product release's hhfab via the public installer (`i.hhdev.io/hhfab` with `VERSION=<product release>`) and upgrade to the current ref.

## Branches

- Minor releases are tagged from `master`; `master` is always the next release candidate.
- Patch releases for the current minor may be tagged from `master` or, once `master` has diverged, from a `release/vX.Y` branch (for example `release/v0.45`).

## Component versions

Fabricator pins the versions of the components it installs in `pkg/fab/versions.go`. Bump them before tagging, after the corresponding component release exists.

`just bump` covers fabric, dataplane, and FRR:

```
just bump fabric <version> <ref>   # also updates the Go module and vendor/
just bump dataplane <version>
just bump frr <version>
```

NOS versions (`BCMSONiCVersion`, `CLSSONiCVersion`, `CumulusVersion`) are edited directly in `pkg/fab/versions.go`.

Caveat: `DataplaneVersion` tags both the dataplane and its validator image. The validator is not built for every dataplane commit; a tag without a validator image makes fabric-ctrl crash-loop. Verify both images exist before bumping.

## Release testing

The release test suite is part of the VLAB jobs (`.github/workflows/run-vlab.yaml`). Regular CI runs only the on-ready subset; full release tests run when explicitly enabled, and those jobs carry an `-rt` suffix in their name.

Full release tests run in one of three ways:

- PR labeled `ci:+release` (add `ci:+hlab` to also run the hardware lab `h-*` jobs),
- manual workflow dispatch with the `releasetest` input (the `releasetest_regex` and `releasetest_regex_invert` inputs select a subset),
- the nightly scheduled run at 06:00 UTC.

Each job uploads a `release-test.xml`; results are aggregated into a single "Release Tests" check on the run.

The tag pipeline does not gate on release tests: publishing only requires the build, bundle, and VLAB jobs on the tag itself, and the nightly schedule covers only `master`. A release ref therefore gets its full release-test coverage from a manual CI dispatch on the tag with the `releasetest` input enabled.

The suite can also be run by hand against any deployed VLAB or lab environment with `hhfab vlab release-test`. Useful flags:

- `--list-tests` prints the available tests; `--regex`/`-r` (repeatable) and `--invert-regex` select a subset.
- `--mode` sets the VPC mode the tests expect: empty means l2vni, `l3vni` for l3vni environments.
- `--pause-on-failure` stops at each failing scenario so the environment can be inspected live; show-tech diagnostics are collected on failure by default (`--show-tech`).
- `--fail-fast` stops on the first failure; `--extended` enables the extended tests.
- `--iperfs-speed` sets the minimum iperf3 speed in Mbit/s for a connectivity check to pass (default 8200; 0 disables speed checks).
- `--results-file` exports JUnit XML, which is what CI uploads.

## Gating a release

Release gating happens at the product-release level and takes about two days of work. The process is intentionally lightweight; the tracking issue described below is what makes it accountable.

The test plan is iterative, not frozen: besides the fixed release matrix, each cycle adds manual or automated coverage for the features shipping in it, and closes gaps found since the previous cycle, especially issues hit by customers or that escaped earlier rounds of testing. Testing runs continuously through the cycle rather than waiting for a release freeze, supported by existing and in-progress tooling.

### The tracking issue

For each product release, open a "YY.MM release testing" issue in the internal tracker. It is the evidence record backing the go/no-go decision, structured as:

- a checklist with three sections: the release-test matrix (below), new release tests required for features added in the cycle or for gaps found since the previous one (with manual testing as a backup while they land), and extra manual checks as the cycle requires (for example input ACLs, host BGP, documentation coverage);
- one comment per completed run, titled `<topology>@<env> (<fabricator version>)`, containing the exact commands used followed by the full release-test recap. Paste recaps verbatim; the previous cycles' issues are the reference for both format and commands.

Ownership started with the release manager and is shifting to QA; there is no formal procedure beyond this issue.

### CI on the release tag

Dispatch the CI workflow on the release tag with the `releasetest` input enabled: full matrix with the `-rt` suffix, including the upgrade and hardware lab jobs. The release-test suite has no automatic retries, and flaky jobs can block the tag's `publish-release` job, so retry failed jobs until the run is green; chasing and fixing flakes is an ongoing effort.

### Release tests on physical environments

The suite currently runs on four topology combinations across two shared lab environments (reserve the environment via the lab Slack channel topic before starting):

- `sl+l2vni@env-1`: spine-leaf, the only combination exercising MCLAG, ESLAG, bundled, and spine failover.
- `mesh+l2vni+gw@env-1`: mesh with gateways.
- `sl+l3vni+gw@env-5`: spine-leaf l3vni with gateways.
- `mesh+l3vni+gw@env-5`: mesh l3vni with gateways.

env-5 switches support l3vni mode only, which is why l3vni coverage lives there; ESLAG is incompatible with l3vni, so LAG failover coverage comes from env-1.

The shape of every run (each environment's fab config and wiring files come from the internal lab repository):

```
curl -fsSL https://i.hhdev.io/hhfab | USE_SUDO=false INSTALL_DIR=. VERSION=<product release> BINARY_NAME=hhfab-<product release> bash
./hhfab-<product release> init -f -c <env fab config> -w <env wiring files>
./hhfab-<product release> vlab up -f -m=manual -r=reinstall -r=inspect
./hhfab-<product release> vlab release-test
```

Flags vary per environment and topology (env-5 needs `--vpc-mode=l3vni -r=vpcs --controls-restricted=false` on `vlab up` and `--mode l3vni` on `release-test`); copy the exact command from the same combination in the previous cycle's tracking issue.

Known environment limitations:

- env-1 gateway nodes use NICs that are not officially supported; the Gateway Failover throughput check is inconsistent there.
- env-4 is not part of the gate: it converges more slowly and some connectivity checks fail on a first attempt and pass on retry.

### Gateway performance baseline

Each cycle gets a "Baseline YY.MM performance" issue in the internal tracker recording gateway NAT throughput on env-5, compared against the previous cycle's issue:

- Deploy two VPCs peered through the gateway, exposing the iperf3 server both through masquerade NAT and through a TCP port forward, and confirm the endpoints run at the maximum fabric-allowed MTU.
- From a server in the peering VPC, run iperf3 through the NAT path with increasing parallelism, `toolbox iperf3 -c <exposed IP> -p <forwarded port> -P <1|2|16|128>` (default 10 s runs), for both the masquerade and the port-forward path.
- Post on the issue: the deployed topology and `GatewayPeering` spec, the endpoint MTU check, each full iperf3 output, and a comparison table of throughput and retransmits per parallelism level against the previous baseline.

A regression in this comparison is a gate failure like any other: it gets investigated, bisecting the intermediate artifacts if needed, and fixed or reverted before the release ships.

### Go/no-go

The release manager asks QA, usually in a meeting; there is no formal sign-off procedure. Failures found during the gate are investigated and documented on the tracking issue; a problem known to be specific to a test environment does not have to block the release, as long as it is recorded there.

Which product releases the upgrade jobs must pass from is decided by the release manager; a written release support policy is pending.

## Pre-release checklist

- A fabricator tag exists and everything below runs against that tag, so the component combination pinned in `pkg/fab/versions.go` is exactly what is tested.
- The upgrade matrix in `ci.yaml` on `master` tests upgrades from the latest shipped product releases; rotate the `upgradefrom` entries when preparing a release (see the "ci: test upgrades from" commits).
- CI green on the tag, including all upgrade jobs.
- Full release tests pass on the tag, including upgrade and hardware lab jobs: dispatch CI on the tag with the `releasetest` input, since neither the tag pipeline nor the nightly covers it.

## Cutting the release

```
git tag vX.Y.Z <ref>
git push origin vX.Y.Z
```

The `publish-release` job in `.github/workflows/ci.yaml` then, gated on the build, bundle, and VLAB jobs passing on the tag:

- publishes images, Helm charts, and multi-arch `hhfab`/`hhfabctl` binaries to ghcr.io,
- creates the [GitHub release](https://github.com/githedgehog/fabricator/releases) with the binary tarballs attached,
- opens an automated PR against the docs repository with the regenerated Fabricator API reference.

Note: the workflow currently marks every tag's GitHub release as latest (`make_latest: true`, with a TODO to restrict it to master), including patch tags on older release branches.

## Post-release

- Merge the automated API reference PR in the docs repository; when the tag ships in a product release, record it in the release notes there.
- Create the `release/vX.Y` branch when the minor needs a patch after `master` has moved on.
- Verify the published artifacts, e.g. `hhfab init` with the new version and a VLAB bring-up.

## Patch releases

Patch releases stabilize the line being shipped: fixes are cherry-picked from `master` onto `release/vX.Y`, and component bumps on the branch follow the components' own patch releases rather than `master`. Tag the next `vX.Y.Z` from the branch; same checklist and publishing flow as above.
