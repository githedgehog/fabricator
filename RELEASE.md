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

## Pre-release checklist

- Component versions in `pkg/fab/versions.go` are final.
- The upgrade matrix in `ci.yaml` on `master` tests upgrades from the latest shipped product releases; rotate the `upgradefrom` entries when preparing a release (see the "ci: test upgrades from" commits).
- CI green on the target ref, including all upgrade jobs.
- Full release tests pass on the release ref, including upgrade and hardware lab jobs: dispatch CI on the ref with the `releasetest` input, since neither the tag pipeline nor the nightly covers it.

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
